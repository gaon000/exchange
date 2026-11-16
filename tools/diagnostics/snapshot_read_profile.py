"""Profile the latest fixed-state snapshot Open, separating CPU and allocation."""
import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tarfile

from common import REPO, child, digest, read, require, save, seal, verify
from runner import source_hashes
from snapshot_profile import parse_top, pprof, prepare_output
from snapshot_tail import analyze as analyze_tail, expected_counts, require_production_head
from state_scale import budget, invoke, stage, values_summary
from wal_decoder_compare import environment

SCOPES = {
    'wholeOpen': None,
    'snapshotRead': r'github.com/gaon000/exchange/internal/snapshot\.readWithTimings',
    'snapshotDecode': r'github.com/gaon000/exchange/internal/snapshot\.decodePayloadWithTimings',
    'restore': r'github.com/gaon000/exchange/internal/engine\.RestoreWithTimings',
    'confirm': r'github.com/gaon000/exchange/internal/snapshot\.confirmWithTimings',
    'wal': r'github.com/gaon000/exchange/internal/wal\.',
}
STAGES = ('recovery.load', 'load.checksum', 'load.decode', 'decode.orders', 'decode.book',
          'decode.trades', 'decode.results', 'recovery.restore', 'recovery.replay',
          'recovery.confirmSnapshot', 'confirm.compareBytes', 'confirm.existingFileSync')


def schedule(smoke=False):
    return [dict(kind=kind, profiler=profiler, repeat=repeat)
            for profiler in ('cpu', 'alloc')
            for repeat in range(1, (1 if smoke else 20 if profiler == 'cpu' else 5)+1)
            for kind in (('active', 'history') if repeat % 2 else ('history', 'active'))]


def selected_files(n):
    return ('.lock', 'genesis.bin', 'commands.wal', f'snapshot-{n:020d}.bin')


def collect(root):
    p,b=read(root/'protocol.json'),read(root/'build.json')
    n=24 if p['smoke'] else 10000
    require(p['version']==1 and p['commands']==n and p['order']==schedule(p['smoke']) and p['allocationSampleRate']==65536,'read profile protocol mismatch')
    require(b['productionSourceIdentical'] is True and digest(root/'statescale')==b['binarySHA256'] and digest(root/'source.tar.gz')==b['sourceArchiveSHA256'],'read profile frozen build changed')
    names=selected_files(n);fixtures={}
    for kind in ('active','history'):
        f=read(root/'fixtures'/(kind+'.json'))
        require(f['Version']==1 and f['Kind']==kind and f['Commands']==n and f['Counts']==expected_counts(kind,n),'read fixture identity/composition mismatch')
        require(set(x.name for x in (root/'fixtures'/kind).iterdir())==set(names),'read fixture file set mismatch')
        for name in names:require(digest(child(root/'fixtures'/kind,name))==f['Files'][name],'read original fixture changed')
        fixtures[kind]=f
    require(sorted(x.name for x in (root/'runs').iterdir())==[f'{i:03d}' for i in range(len(p['order']))],'missing/extra read profile worker')
    groups={}
    for i,row in enumerate(p['order']):
        case=root/'runs'/f'{i:03d}';f=fixtures[row['kind']]
        require(read(case/'case.json')==row and read(case/'run/.statescale-owned')==row,'read profile case/ownership changed')
        require(set(x.name for x in (case/'run').iterdir())==set(names)|{'.statescale-owned'},'read worker file set changed')
        for name in names:require(digest(child(case/'run',name))==f['Files'][name],'read worker input/state changed')
        o=read(case/'profile/result.json');r=o['Recovery']
        require(o['Version']==1 and o['Verified'] is True and o['Phase']=='snapshot-open' and o['Profiler']==row['profiler'] and o['Commands']==n,'read profile observation mismatch')
        require(o['GoVersion']==b['go']['GOVERSION'] and o['GOMAXPROCS']==int(p['environment']['GOMAXPROCS']) and o['MemProfileRate']==(65536 if row['profiler']=='alloc' else 524288),'read profile runtime/sample mismatch')
        require(int(r['SnapshotSeq'])==n and int(r['ReplayedCommands'])==0 and r['SnapshotFile']==names[-1] and not r['Skipped'],'read profile recovery path changed')
        require(o['SnapshotSHA256']==f['Files'][names[-1]] and o['WALSHA256']==f['Files']['commands.wal'],'read profile state mismatch')
        require(o['After']['TotalAlloc']>=o['Before']['TotalAlloc'] and o['After']['Mallocs']>=o['Before']['Mallocs'],'read profile memory counter mismatch')
        files=('cpu.pprof',) if row['profiler']=='cpu' else ('alloc-before.pprof','alloc-after.pprof')
        require(set(x.name for x in (case/'profile').iterdir())=={'result.json',*files},'read profile files missing/extra')
        for name in files:require(child(case/'profile',name).is_file() and (case/'profile'/name).stat().st_size>0,'missing/empty profile')
        for name in STAGES:stage(r['Stages'],name)
        groups.setdefault((row['kind'],row['profiler']),[]).append((case,o))
    return p,b,fixtures,groups


def summarize(root, output, env):
    p,b,fixtures,groups=collect(root)
    rows=[]
    for (kind,profiler),cases in groups.items():
        dest=output/(kind+'-'+profiler);dest.mkdir();inputs=[]
        if profiler=='cpu':inputs=[case/'profile/cpu.pprof' for case,_ in cases]
        else:
            for i,(case,_) in enumerate(cases):
                delta=dest/f'delta-{i}.pprof'
                pprof(root/'statescale',['-proto','-base',str(case/'profile/alloc-before.pprof')],[case/'profile/alloc-after.pprof'],delta,env);inputs.append(delta)
        merged=dest/'merged.pprof';pprof(root/'statescale',['-proto'],inputs,merged,env)
        raw=dest/'raw.txt';pprof(root/'statescale',['-raw'],[merged],raw,env)
        period=re.search(r'^Period: (\d+)$',raw.read_text(),re.M)
        if profiler=='cpu':require(period is not None and int(period[1])==10000000,'CPU sample period changed')
        metric='cpu' if profiler=='cpu' else 'alloc_space';profiles={}
        for scope,focus in SCOPES.items():
            flags=['-top','-nodecount=0','-nodefraction=0','-edgefraction=0','-sample_index='+metric,'-unit='+('ns' if profiler=='cpu' else 'bytes')]
            if focus:flags+=['-focus='+focus,'-relative_percentages']
            path=dest/(scope+'.txt');pprof(root/'statescale',flags,[merged],path,env)
            parsed=parse_top(path.read_text())
            if not p['smoke'] and scope in ('wholeOpen','snapshotRead','snapshotDecode'):require(parsed['total']>0 and parsed['functions'],'no usable read profile samples')
            profiles[scope]={'total':parsed['total'],'perOperation':parsed['total']/len(cases),
                             'topFlat':sorted(parsed['functions'],key=lambda v:(-v['flat'],v['function']))[:15],
                             'topCumulative':sorted(parsed['functions'],key=lambda v:(-v['cumulative'],v['function']))[:20]}
            if profiler=='alloc':
                pprof(root/'statescale',['-top','-nodecount=0','-nodefraction=0','-sample_index=alloc_objects',*(['-focus='+focus,'-relative_percentages'] if focus else [])],[merged],dest/(scope+'-objects.txt'),env)
        if p['smoke'] and profiles['snapshotRead']['total']==0 and profiles['confirm']['total']==0:
            (dest/'callers.txt').write_text('No read/confirm samples in this functional smoke; no cost inference.\n')
        else:
            pprof(root/'statescale',['-sample_index='+metric,'-peek=readWithTimings|parseValue|decodePayloadWithTimings|confirmWithTimings'],[merged],dest/'callers.txt',env)
        rows.append({'kind':kind,'profiler':profiler,'repeats':len(cases),'counts':fixtures[kind]['Counts'],
                     'cpuSamples':round(profiles['wholeOpen']['total']/int(period[1])) if profiler=='cpu' else None,
                     'apiAllocMiB':values_summary([(o['After']['TotalAlloc']-o['Before']['TotalAlloc'])/2**20 for _,o in cases]),
                     'stagesMs':{name:values_summary([stage(o['Recovery']['Stages'],name) for _,o in cases]) for name in STAGES},
                     'profiles':profiles,'artifacts':dest.name})
    report={'version':1,'commands':p['commands'],'workersVerified':len(p['order']),'sourceBundleManifestSHA256':p['sourceBundleManifestSHA256'],
            'binarySHA256':b['binarySHA256'],'allocationSampleRate':65536,'rows':rows,'powerLossTested':False,'performanceImprovementClaimed':False,
            'scope':'tail=0 public Open; CPU samples and baseline allocation deltas from separate fresh workers; overlapping stack focus; warm cache; not baseline wall-time/HTTP performance'}
    save(output/'report.json',report);(output/'report.md').write_text(markdown(report));return report


def markdown(report):
    lines=['# 최신 스냅샷 읽기 프로파일','',
           '| 상태 | 계측 | 반복 | Open 전체 | 읽기 | decode | restore | confirm | WAL |','| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |']
    for row in report['rows']:
        scale=1e6 if row['profiler']=='cpu' else 2**20
        lines.append('| '+' | '.join([row['kind'],row['profiler'],str(row['repeats']),*[f"{row['profiles'][name]['perOperation']/scale:.3f}" for name in SCOPES]])+' |')
    lines+=['','CPU 단위는 ms/호출, allocation은 MiB/호출 추정치다. CPU는 I/O 대기 wall 시간이 아니다.',
            '각 호출 경로 focus는 중첩되므로 더하지 않는다. allocation은 64KiB 표본의 전후 차이이며 유지 heap/RSS가 아니다.',
            '조회/최종 bytes 검증은 profile 밖이다. warm-cache·비격리 호스트이며 실제 전원 장애 검증은 미완료·보류다.','']
    return '\n'.join(lines)


def input_build(bundle, smoke):
    """Verify the sealed input producer; never relabel an older decoder as current."""
    p=read(bundle/'protocol.json')
    require(p['smoke']==smoke,'source smoke/full mismatch')
    kind=p.get('experiment','snapshot-tail')
    if kind=='snapshot-tail':
        analyze_tail(bundle);path=bundle/'build.json'
    elif kind=='canonical-buffer':
        from canonical_buffer import analyze as analyze_canonical
        analyze_canonical(bundle);path=bundle/'builds/candidate/build.json'
    elif kind=='recovery-rss':
        from recovery_rss import analyze as analyze_rss
        analyze_rss(bundle);path=bundle/'builds/candidate/build.json'
    else:
        raise ValueError('unsupported profile input experiment')
    return kind,read(path)


def run(bundle, output, smoke=False):
    require(platform.system()=='Darwin' and platform.machine()=='arm64','requires macOS arm64/APFS/cgo')
    bundle=bundle.resolve();original=verify(bundle);source_kind,source_build=input_build(bundle,smoke)
    n=24 if smoke else 10000
    require(read(bundle/'protocol.json')['smoke']==smoke,'source smoke/full mismatch')
    output=prepare_output(output,[bundle]);env=environment();env['PPROF_TMPDIR']=str(output)
    protocol={'version':1,'commands':n,'smoke':smoke,'order':schedule(smoke),'allocationSampleRate':65536,'createdUTC':datetime.now(timezone.utc).isoformat(),
              'sourceBundle':str(bundle),'sourceKind':source_kind,'sourceBuildBinarySHA256':source_build['binarySHA256'],'sourceBundleManifestSHA256':digest(bundle/'manifest.json'),'environment':{k:env[k] for k in ('GOMAXPROCS','GOGC','GOMEMLIMIT')},'cache':'warm copies/hashes; no flush','powerLossTested':False}
    save(output/'protocol.json',protocol)
    try:
        require_production_head();sources=source_hashes();old=source_build['sourceSHA256']
        production=lambda values:{k:v for k,v in values.items() if k.startswith(('internal/','cmd/')) or k in ('go.mod','.go-version')}
        require(production(sources)==production(old),'production differs from fixed final state')
        with tarfile.open(output/'source.tar.gz','x:gz') as archive:
            for name in sources:archive.add(REPO/name,arcname=name,recursive=False)
        binary=output/'statescale';subprocess.run([str(REPO/'scripts/go'),'build','-mod=readonly','-o',str(binary),'./tools/statescale'],cwd=REPO,env=env,check=True,timeout=180)
        go=json.loads(subprocess.check_output([str(REPO/'scripts/go'),'env','-json','GOVERSION','CGO_ENABLED','GOOS','GOARCH'],cwd=REPO,env=env))
        require(go['CGO_ENABLED']=='1' and source_hashes()==sources,'read profile build changed')
        build={'revision':subprocess.check_output(['git','rev-parse','HEAD'],cwd=REPO,text=True).strip(),'workingTreeModified':bool(subprocess.check_output(['git','status','--porcelain'],cwd=REPO)),
               'go':go,'sourceSHA256':sources,'sourceArchiveSHA256':digest(output/'source.tar.gz'),'binarySHA256':digest(binary),'productionSourceIdentical':True,
               'platform':platform.platform(),'hardware':{k:subprocess.check_output(['sysctl','-n',k],text=True).strip() for k in ('machdep.cpu.brand_string','hw.memsize','hw.logicalcpu')}}
        save(output/'build.json',build);(output/'fixtures').mkdir();(output/'runs').mkdir();fixtures={}
        for kind in ('active','history'):
            f=read(bundle/'fixtures'/(kind+'.json'));source=bundle/'fixtures'/kind;target=output/'fixtures'/kind
            budget(output,sum((source/name).stat().st_size for name in selected_files(n)));target.mkdir()
            for name in selected_files(n):shutil.copyfile(child(source,name),target/name);require(digest(target/name)==f['Files'][name],'read fixture copy differs')
            save(output/'fixtures'/(kind+'.json'),f);fixtures[kind]=f
        predicted=sum(sum((output/'fixtures'/row['kind']/name).stat().st_size for name in selected_files(n)) for row in protocol['order'])
        budget(output,predicted+128*2**20)
        for i,row in enumerate(protocol['order']):
            f=fixtures[row['kind']];case=output/'runs'/f'{i:03d}';budget(output,sum((output/'fixtures'/row['kind']/name).stat().st_size for name in selected_files(n))+2**20)
            case.mkdir();(case/'run').mkdir()
            for name in selected_files(n):
                source=output/'fixtures'/row['kind']/name;require(digest(source)==f['Files'][name],'read fixture changed before copy')
                shutil.copyfile(source,case/'run'/name);require(digest(case/'run'/name)==f['Files'][name],'read worker copy differs')
            save(case/'case.json',row);save(case/'run/.statescale-owned',row)
            invoke(binary,['profile','--dir',case/'run','--output',case/'profile','--phase','snapshot-open','--profiler',row['profiler'],'--count',n,
                           '--snapshot-sha',f['Files'][selected_files(n)[-1]],'--wal-sha',f['Files']['commands.wal']],case/'probe',env)
            require(source_hashes()==sources and digest(binary)==build['binarySHA256'],'read profile source/binary changed')
            print(json.dumps({'completed':i+1,'total':len(protocol['order']),**row}),flush=True)
        (output/'analysis').mkdir();result=summarize(output,output/'analysis',env)
        require(verify(bundle)==original and digest(bundle/'manifest.json')==protocol['sourceBundleManifestSHA256'],'source evidence changed')
        seal(output,True);return result
    except BaseException as error:save(output/'failed.json',{'error':type(error).__name__,'message':str(error)});raise


def main():
    p=argparse.ArgumentParser(description=__doc__);sub=p.add_subparsers(dest='command',required=True)
    r=sub.add_parser('run');r.add_argument('--bundle',type=Path,required=True);r.add_argument('--output',type=Path,required=True);r.add_argument('--smoke',action='store_true')
    a=sub.add_parser('analyze');a.add_argument('--bundle',type=Path,required=True);a.add_argument('--output',type=Path,required=True)
    args=p.parse_args()
    if args.command=='run':result=run(args.bundle,args.output.absolute(),args.smoke)
    else:
        root=args.bundle.resolve();verify(root);collect(root);out=prepare_output(args.output,[root]);env=environment();env['PPROF_TMPDIR']=str(out)
        result=summarize(root,out,env);seal(out,True)
    print(json.dumps({'status':'PASSED','workersVerified':result['workersVerified']}))


if __name__=='__main__':
    try:main()
    except (Exception,KeyboardInterrupt) as error:print(f'snapshot-read-profile: {error}',file=sys.stderr);sys.exit(1)
