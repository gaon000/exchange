"""Compare snapshot array allocation with identical fixed tail recovery states."""
import argparse
from datetime import datetime, timezone
import json
import hashlib
import platform
import subprocess
import tarfile
from pathlib import Path
import shutil
import sys

from common import REPO, child, digest, read, require, save, seal, verify
from read_buffer import expected_names, retire_copy
from runner import source_hashes
from snapshot_tail import analyze as analyze_tail, expected_counts, metrics, schedule as tail_schedule, validate
from state_scale import budget, invoke, values_summary
from wal_decoder_compare import build_pair, environment, freeze, new_output

CHANGES=('internal/snapshot/decode.go',)
ENV_KEYS=('GOMAXPROCS','GOGC','GOMEMLIMIT')


def schedule(smoke=False):
    rows=[]
    for i,row in enumerate(tail_schedule(smoke)):
        sides=('baseline','candidate') if (row['repeat']+i)%2 else ('candidate','baseline')
        rows.extend({**row,'version':side} for side in sides)
    return rows


def validate_receipt(case,row,f,fixtures):
    require(set(p.name for p in case.iterdir())=={'case.json','result.json','verified-files.json','probe.stdout','probe.stderr'},'incomplete case/retirement')
    receipt=read(case/'verified-files.json')
    require(receipt['version']==1 and receipt['owner']==row and set(receipt['files'])==expected_names(row) and receipt['policy']=='successful scratch copy removed; immutable common fixtures retained','scratch receipt mismatch')
    for name,meta in receipt['files'].items():
        require(meta=={'sha256':f['Files'][name],'bytes':child(fixtures,name).stat().st_size},'scratch receipt state differs')


def checked_builds(root,changes=CHANGES):
    builds=build_pair(root,changes)
    for b in builds.values():
        require(b['go']['CGO_ENABLED']=='1' and b['go']['GOOS']=='darwin' and b['go']['GOARCH']=='arm64','requires native macOS arm64/cgo')
    for side,b in builds.items():
        with tarfile.open(root/'builds'/side/'source.tar.gz') as archive:
            members=archive.getmembers()
            require(len(members)==len(b['sourceSHA256']) and {m.name for m in members}==set(b['sourceSHA256']),'source archive members differ')
            for m in members:
                require(m.isfile() and hashlib.sha256(archive.extractfile(m).read()).hexdigest()==b['sourceSHA256'][m.name],'source archive content differs')
    return builds


def validate_resident(o):
    rss=o.get('RSSAfterGCBytes')
    require(type(rss) is int and 0<rss<=2*2**30,'missing/invalid post-GC current resident RSS')
    require(0<o['Open']['AfterGC']['HeapAlloc']<=2*2**30,'invalid post-GC live heap')


def analyze(root,changes=CHANGES):
    p=read(root/'protocol.json');order=schedule(p['smoke']);builds=checked_builds(root,changes)
    require(p['version']==1 and p['order']==order and set(p['environment'])==set(ENV_KEYS),'array protocol changed')
    require(p['environment']=={'GOMAXPROCS':p['environment']['GOMAXPROCS'],'GOGC':'100','GOMEMLIMIT':'2GiB'} and int(p['environment']['GOMAXPROCS'])>0,'array runtime budget changed')
    require(sorted(x.name for x in (root/'runs').iterdir())==[f'{i:03d}' for i in range(len(order))],'missing/extra array workers')
    fixtures={};groups={};pairs={}
    for kind in ('active','history'):
        f=read(root/'fixtures'/(kind+'.json'))
        require(f['Version']==1 and f['Kind']==kind and f['Commands']==order[0]['commands'] and f['Counts']==expected_counts(kind,f['Commands']),'array fixture identity changed')
        for name,sha in f['Files'].items():require(digest(child(root/'fixtures'/kind,name))==sha,'array fixture changed')
        fixtures[kind]=f
    for i,row in enumerate(order):
        case=root/'runs'/f'{i:03d}';f=fixtures[row['kind']]
        require(read(case/'case.json')==row,'array case order changed')
        validate_receipt(case,row,f,root/'fixtures'/row['kind'])
        o=read(case/'result.json');validate(o,row,f,builds[row['version']],p['environment']);validate_resident(o);m=metrics(o);m['rssAfterGC_MiB']=o['RSSAfterGCBytes']/2**20
        groups.setdefault((row['kind'],row['tail'],row['version']),[]).append(m)
        pairs.setdefault((row['kind'],row['tail'],row['repeat']),{})[row['version']]=m
    rows=[]
    for kind in ('active','history'):
        for tail in ([0,8,12] if p['smoke'] else [0,1000,5000]):
            versions={}
            for side in ('baseline','candidate'):
                values=groups[kind,tail,side];require(len(values)==(1 if p['smoke'] else 5),'array repeats missing')
                versions[side]={name:values_summary([v[name] for v in values]) for name in values[0]}
            a,b=versions['baseline'],versions['candidate']
            rows.append({'kind':kind,'tail':tail,'versions':versions,
                         'medianChangePercent':{name:(b[name]['median']/a[name]['median']-1)*100 for name in a if a[name]['median']>0},
                         'pairs':[{'repeat':rep,'changesPercent':{name:(v['candidate'][name]/v['baseline'][name]-1)*100 for name in ('openMs','openAllocMiB','heapAfterGC_MiB','rssAfterGC_MiB','peakRSSMiB')}} for (k,t,rep),v in pairs.items() if (k,t)==(kind,tail)]})
    return {'version':1,'workersVerified':len(order),'repeats':1 if p['smoke'] else 5,'commands':order[0]['commands'],'smoke':p['smoke'],
            'rows':rows,'firstResponsesVerified':len(order)*8,'retriesVerified':len(order)*2,'sourceBundleManifestSHA256':p['sourceBundleManifestSHA256'],
            'binarySHA256':{s:b['binarySHA256'] for s,b in builds.items()},'environment':p['environment'],'productionChanges':list(changes),'platform':p['platform'],'hardware':p['hardware'],
            'scope':'fresh worker public Open without observers; warm cache; successful owned scratch retired with receipts; post-GC live engine heap/current Mach resident_size and worker-start-through-Open peak RSS',
            'powerLossTested':False}


def markdown(report,title='스냅샷 배열 할당 비교'):
    cell=lambda v:f"{v['median']:.3f} [{v['min']:.3f}~{v['max']:.3f}]"
    lines=['# '+title,'',f"각 버전 {report['repeats']}회, {report['workersVerified']} worker. 중앙값 [최소~최대].",'',
           '| 상태 | 이후 명령 | 이전 Open ms | 이후 Open ms | 변화 % | 이전 TotalAlloc MiB | 이후 TotalAlloc MiB | 변화 % |',
           '| --- | ---: | --- | --- | ---: | --- | --- | ---: |']
    for r in report['rows']:
        a,b=r['versions']['baseline'],r['versions']['candidate'];d=r['medianChangePercent']
        lines.append(f"| {r['kind']} | {r['tail']} | {cell(a['openMs'])} | {cell(b['openMs'])} | {d['openMs']:.2f} | {cell(a['openAllocMiB'])} | {cell(b['openAllocMiB'])} | {d['openAllocMiB']:.2f} |")
    lines+=['','| 상태 | 이후 명령 | 이전 GC 뒤 heap MiB | 이후 GC 뒤 heap MiB | 이전 GC 뒤 RSS MiB | 이후 GC 뒤 RSS MiB | 이전 peak RSS MiB | 이후 peak RSS MiB |',
            '| --- | ---: | --- | --- | --- | --- | --- | --- |']
    for r in report['rows']:
        a,b=r['versions']['baseline'],r['versions']['candidate']
        lines.append(f"| {r['kind']} | {r['tail']} | {cell(a['heapAfterGC_MiB'])} | {cell(b['heapAfterGC_MiB'])} | {cell(a['rssAfterGC_MiB'])} | {cell(b['rssAfterGC_MiB'])} | {cell(a['peakRSSMiB'])} | {cell(b['peakRSSMiB'])} |")
    lines+=['','준비/해시/강제 GC/후속 검증은 Open wall 밖이다. 자연 GC·내부 I/O 대기는 포함한다.',
            'heap은 엔진을 살려 둔 강제 GC 뒤 HeapAlloc, GC 뒤 RSS는 Mach resident_size, peak RSS는 worker 시작부터 Open 직후까지 high-water다. 2GiB budget 유지.',
            '동일 입력·32KiB decoder·canonical/이중 checksum·WAL prefix/최초 응답/전체 상태·strict sync 유지. 느린 표본도 포함.',
            'warm-cache·비격리 호스트이며 HTTP p99/최대 처리량 개선을 증명하지 않는다. 실제 OS·전원 장애 검증은 미완료·보류다.','']
    return '\n'.join(lines)


def input_producer(bundle,smoke,kind):
    """Validate a fixed-state producer before selecting its source metadata."""
    require(read(bundle/'protocol.json')['smoke']==smoke,'input smoke/full mismatch')
    if kind=='snapshot-tail':
        analyze_tail(bundle);return read(bundle/'build.json')
    if kind=='canonical-buffer':
        from canonical_buffer import analyze as analyze_canonical
        analyze_canonical(bundle);return read(bundle/'builds/candidate/build.json')
    raise ValueError('unsupported comparison input kind')


def run(bundle,baseline,candidate,output,smoke=False,*,changes=CHANGES,input_kind='snapshot-tail',title='스냅샷 배열 할당 비교'):
    bundle=bundle.resolve();baseline=baseline.resolve();candidate=candidate.resolve()
    verify(bundle);producer=input_producer(bundle,smoke,input_kind)
    for path in (baseline,candidate):verify(path)
    output=new_output(output,[bundle,baseline,candidate]);env=environment();current=source_hashes();order=schedule(smoke)
    save(output/'protocol.json',{'version':1,'smoke':smoke,'inputKind':input_kind,'order':order,'sourceBundleManifestSHA256':digest(bundle/'manifest.json'),
                                'createdUTC':datetime.now(timezone.utc).isoformat(),'platform':platform.platform(),'hardware':{k:subprocess.check_output(['sysctl','-n',k],text=True).strip() for k in ('machdep.cpu.brand_string','hw.memsize','hw.logicalcpu')},'environment':{k:env[k] for k in ENV_KEYS}})
    try:
        budget(output,sum(p.stat().st_size for path in (baseline,candidate) for p in path.rglob('*') if p.is_file())+64*2**20)
        (output/'builds').mkdir()
        for side,path in [('baseline',baseline),('candidate',candidate)]:shutil.copytree(path,output/'builds'/side)
        builds=checked_builds(output,changes);require(current==builds['candidate']['sourceSHA256'],'source differs from frozen candidate')
        production=lambda src:{n:s for n,s in src.items() if (n.startswith(('internal/','cmd/')) and n.endswith('.go') and not n.endswith('_test.go')) or n in ('go.mod','.go-version')}
        require(production(producer['sourceSHA256'])==production(builds['baseline']['sourceSHA256']),'baseline differs from fixture production')
        (output/'fixtures').mkdir();(output/'runs').mkdir();fixtures={}
        for kind in ('active','history'):
            f=read(bundle/'fixtures'/(kind+'.json'));target=output/'fixtures'/kind;target.mkdir()
            budget(output,sum((bundle/'fixtures'/kind/name).stat().st_size for name in f['Files'])+2**20)
            for name,sha in f['Files'].items():shutil.copyfile(child(bundle/'fixtures'/kind,name),target/name);require(digest(target/name)==sha,'array fixture copy differs')
            save(output/'fixtures'/(kind+'.json'),f);fixtures[kind]=f
        for i,row in enumerate(order):
            f=fixtures[row['kind']];case=output/'runs'/f'{i:03d}';names=expected_names(row)
            budget(output,sum((output/'fixtures'/row['kind']/name).stat().st_size for name in names)+2**20)
            case.mkdir();(case/'run').mkdir();selected=f"snapshot-{row['commands']-row['tail']:020d}.bin"
            for name in {'.lock','genesis.bin','commands.wal',selected}:
                source=child(output/'fixtures'/row['kind'],name);require(digest(source)==f['Files'][name],'fixture changed before copy');shutil.copyfile(source,case/'run'/name)
            save(case/'case.json',row);save(case/'run/.tail-owned',row);binary=output/'builds'/row['version']/'statescale'
            invoke(binary,['tail-measure','--resident','--kind',row['kind'],'--count',row['commands'],'--tail',row['tail'],'--dir',case/'run','--receipt',output/'fixtures'/(row['kind']+'.json'),'--output',case/'result.json'],case/'probe',env)
            validate(read(case/'result.json'),row,f,builds[row['version']],{k:env[k] for k in ENV_KEYS});validate_resident(read(case/'result.json'))
            require(source_hashes()==current and digest(binary)==builds[row['version']]['binarySHA256'],'source/binary changed during comparison')
            retire_copy(case,row,f);print(json.dumps({'completed':i+1,'total':len(order),**row}),flush=True)
        report=analyze(output,changes);save(output/'report.json',report);(output/'report.md').write_text(markdown(report,title))
        verify(bundle);require(digest(bundle/'manifest.json')==read(output/'protocol.json')['sourceBundleManifestSHA256'],'source evidence changed');seal(output,True);return report
    except BaseException as error:save(output/'failed.json',{'error':type(error).__name__,'message':str(error)});raise


def main():
    p=argparse.ArgumentParser(description=__doc__);sub=p.add_subparsers(dest='command',required=True)
    f=sub.add_parser('freeze');f.add_argument('--label',choices=('baseline','candidate'),required=True);f.add_argument('--output',type=Path,required=True)
    r=sub.add_parser('run')
    for name in ('bundle','baseline','candidate','output'):r.add_argument('--'+name,type=Path,required=True)
    r.add_argument('--smoke',action='store_true')
    a=sub.add_parser('analyze');a.add_argument('--bundle',type=Path,required=True);a.add_argument('--output',type=Path,required=True)
    args=p.parse_args()
    if args.command=='freeze':freeze(args.output,args.label);print('frozen');return
    if args.command=='run':report=run(args.bundle,args.baseline,args.candidate,args.output,args.smoke)
    else:
        root=args.bundle.resolve();verify(root);report=analyze(root);out=new_output(args.output,[root]);save(out/'report.json',report);(out/'report.md').write_text(markdown(report));seal(out,True)
    print(json.dumps({'status':'PASSED','workersVerified':report['workersVerified']}))


if __name__=='__main__':
    try:main()
    except (Exception,KeyboardInterrupt) as error:print(f'snapshot-array-compare: {error}',file=sys.stderr);sys.exit(1)
