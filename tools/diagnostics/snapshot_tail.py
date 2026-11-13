"""Fixed total-command recovery cost with old snapshots and explicit tail witnesses."""
import argparse
from datetime import datetime, timezone
import json
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile

from common import REPO, child, digest, read, require, save, seal, verify
from runner import source_hashes
from state_scale import budget, invoke, stage, values_summary
from wal_decoder_compare import environment, new_output


def settings(smoke):
    return (24,[0,8,12],1) if smoke else (10000,[0,1000,5000],5)


def schedule(smoke=False):
    n,tails,repeats=settings(smoke)
    conditions=[(kind,tail) for tail in tails for kind in ('active','history')]
    result=[]
    for repeat in range(1,repeats+1):
        offset=(repeat-1)%len(conditions)
        order=conditions[offset:]+conditions[:offset]
        if repeat%2==0:order.reverse()
        result += [{'kind':kind,'commands':n,'tail':tail,'repeat':repeat} for kind,tail in order]
    return result


def expected_counts(kind,n):
    if kind=='active':return dict(Orders=n-5,Active=n-8,Filled=1,Cancelled=2,Trades=1,Results=n)
    cycles=(n-8)//4
    return dict(Orders=2*cycles+3,Active=0,Filled=cycles+1,Cancelled=cycles+2,Trades=cycles+1,Results=n)


def validate(o,row,f,build,env):
    n,tail=row['commands'],row['tail'];cut=n-tail
    require(o['Version']==1 and o['Kind']==row['kind'] and o['Commands']==n and o['Tail']==tail and o['Verified'] is True,'tail identity/verification mismatch')
    require(o['FirstResponsesVerified']==8 and o['RetriesVerified']==2 and o['OrdersBalancesTradeVerified'] is True,'tail witnesses missing')
    require(o['GoVersion']==build['go']['GOVERSION'] and o['GOMAXPROCS']==int(env['GOMAXPROCS']),'tail runtime changed')
    r=o['Recovery'];name=f'snapshot-{cut:020d}.bin'
    require(int(r['SnapshotSeq'])==cut and int(r['ReplayedCommands'])==tail and r['SnapshotFile']==name and not r['Skipped'],'wrong snapshot/tail recovery')
    require(o['FinalSnapshotSHA256']==f['Files'][f'snapshot-{n:020d}.bin'] and o['WALSHA256']==f['Files']['commands.wal'],'tail state hash mismatch')
    p=o['Open']
    require(type(p['WallNanos']) is int and p['WallNanos']>0 and 0<p['PeakRSSBytes']<=2*2**30,'invalid tail timing/RSS')
    require(p['After']['TotalAlloc']>=p['Before']['TotalAlloc'] and p['After']['Mallocs']>=p['Before']['Mallocs'] and p['AfterGC']['NumGC']>p['After']['NumGC'],'invalid tail memory/GC counters')


def metrics(o):
    p=o['Open'];stages=o['Recovery']['Stages']
    result={'openMs':p['WallNanos']/1e6,'openAllocMiB':(p['After']['TotalAlloc']-p['Before']['TotalAlloc'])/2**20,
            'heapAfterGC_MiB':p['AfterGC']['HeapAlloc']/2**20,'heapDeltaMiB':(p['AfterGC']['HeapAlloc']-p['Before']['HeapAlloc'])/2**20,
            'peakRSSMiB':p['PeakRSSBytes']/2**20}
    for label,name in [('loadMs','recovery.load'),('restoreMs','recovery.restore'),('replayMs','recovery.replay'),('confirmMs','recovery.confirmSnapshot')]:result[label]=stage(stages,name)
    return result


def analyze(root):
    p,b=read(root/'protocol.json'),read(root/'build.json')
    n,tails,repeats=settings(p['smoke'])
    require(p['version']==1 and p['commands']==n and p['tails']==tails and p['repeats']==repeats and p['order']==schedule(p['smoke']),'tail protocol changed')
    require(digest(root/'statescale')==b['binarySHA256'] and digest(root/'source.tar.gz')==b['sourceArchiveSHA256'],'tail frozen build changed')
    require(sorted(path.name for path in (root/'runs').iterdir())==[f'{i:03d}' for i in range(len(p['order']))],'missing/extra tail worker')
    fixtures={}
    for kind in ('active','history'):
        f=read(root/'fixtures'/(kind+'.json'))
        require(f['Version']==1 and f['Kind']==kind and f['Commands']==n and f['Counts']==expected_counts(kind,n) and f['Cuts']==sorted(n-t for t in tails),'tail fixture identity/composition mismatch')
        names={'.lock','genesis.bin','commands.wal'}|{f'snapshot-{n-t:020d}.bin' for t in tails}
        require(set(f['Files'])==names and len(f['Witnesses'])==8,'tail fixture files/witness count mismatch')
        codes=['ACCEPTED','ACCEPTED','ACCEPTED','CANCELLED','CANCELLED','ALREADY_FILLED','ALREADY_CANCELLED','ORDER_NOT_FOUND']
        seqs=[1,2,*range(n-5,n+1)]
        for w,code,seq in zip(f['Witnesses'],codes,seqs):
            require(w['First']['code']==code and int(w['First']['commandSeq'])==seq,'tail first response fixture mismatch')
        require(all(w['First']['order']['status']=='OPEN' for w in f['Witnesses'][:2]),'original response is not OPEN')
        for name,sha in f['Files'].items():require(digest(child(root/'fixtures'/kind,name))==sha,'tail original fixture changed')
        fixtures[kind]=f
    groups={}
    for i,row in enumerate(p['order']):
        path=root/'runs'/f'{i:03d}';f=fixtures[row['kind']]
        require(read(path/'case.json')==row,'tail case ordering changed')
        o=read(path/'result.json');validate(o,row,f,b,p['runtimeEnvironment'])
        selected=f"snapshot-{n-row['tail']:020d}.bin";final=f'snapshot-{n:020d}.bin'
        names={'.lock','genesis.bin','commands.wal',selected,final}
        require(set(file.name for file in (path/'run').iterdir())==names|{'.tail-owned'},'tail worker file set changed')
        require(read(path/'run/.tail-owned')==row,'tail ownership receipt changed')
        for name in names:require(digest(child(path/'run',name))==f['Files'][name],'tail worker state/input changed')
        groups.setdefault((row['kind'],row['tail']),[]).append(metrics(o))
    rows=[]
    for kind in ('active','history'):
        for tail in tails:
            values=groups[kind,tail];require(len(values)==repeats,'insufficient tail repeats')
            snapshot=f'snapshot-{n-tail:020d}.bin'
            rows.append({'kind':kind,'commands':n,'tail':tail,'snapshotSeq':n-tail,'counts':fixtures[kind]['Counts'],
                         'snapshotBytes':(root/'fixtures'/kind/snapshot).stat().st_size,
                         'metrics':{name:values_summary([v[name] for v in values]) for name in values[0]}})
    return {'version':1,'workersVerified':len(p['order']),'repeats':repeats,'smoke':p['smoke'],'commands':n,'tails':tails,
            'binarySHA256':b['binarySHA256'],'sourceArchiveSHA256':b['sourceArchiveSHA256'],'runtimeEnvironment':p['runtimeEnvironment'],
            'rows':rows,'firstResponsesVerified':len(p['order'])*8,'retriesVerified':len(p['order'])*2,
            'powerLossTested':False,'scope':'fixed final state/WAL; warm-cache public Open; post-snapshot matching/cancel/original response verified outside timing'}


def markdown(report):
    cell=lambda x:f"{x['median']:.3f} [{x['min']:.3f}~{x['max']:.3f}]"
    text=['# 스냅샷 이후 명령 복구 비용','',f"총 {report['commands']}명령, 조건별 {report['repeats']}회 중앙값 [최소~최대], {report['workersVerified']} worker 상태 일치.",'',
          '| 상태 | 이후 명령 | snapshot 순번 | snapshot MiB | Open ms | 할당 MiB | GC 뒤 heap 증가 MiB | 누적 peak RSS MiB |',
          '| --- | ---: | ---: | ---: | --- | --- | --- | --- |']
    for row in report['rows']:
        m=row['metrics'];text.append(f"| {row['kind']} | {row['tail']} | {row['snapshotSeq']} | {row['snapshotBytes']/2**20:.3f} | {cell(m['openMs'])} | {cell(m['openAllocMiB'])} | {cell(m['heapDeltaMiB'])} | {cell(m['peakRSSMiB'])} |")
    text+=['','| 상태 | 이후 명령 | load ms | restore ms | replay/prefix ms | confirm ms |','| --- | ---: | --- | --- | --- | --- |']
    for row in report['rows']:
        m=row['metrics'];text.append('| '+' | '.join([row['kind'],str(row['tail']),*[cell(m[n]) for n in ('loadMs','restoreMs','replayMs','confirmMs')]])+' |')
    text+=['','같은 상태 유형의 전체 WAL/초기 자금/최종 상태를 고정하고 snapshot cut만 변경했다. 전체 WAL prefix 검사·strict sync는 유지한다.',
           '조회/최초 응답/중복 재시도/최종 snapshot bytes 검증은 Open 시간 밖이다. 준비/사본/해시/강제 GC도 제외하고 자연 GC·I/O 대기는 포함한다.',
           '새 프로세스·warm-cache·비격리 호스트이며 느린 표본도 포함한다. 실제 전원 장애 검증은 미완료·보류다.','']
    return '\n'.join(text)


def require_production_head(root=REPO):
    paths=['internal','cmd','go.mod','.go-version']
    # Without exclude-standard, ignored files are also checked: Go builds them too.
    unknown=subprocess.check_output(['git','ls-files','--others','--',*paths],cwd=root)
    require(not unknown,'untracked production files differ from HEAD')
    subprocess.run(['git','diff','--exit-code','HEAD','--',*paths],cwd=root,check=True,capture_output=True)


def run(output,smoke=False):
    require(platform.system()=='Darwin' and platform.machine()=='arm64','requires macOS arm64/APFS/cgo')
    output=new_output(output);env=environment();sources=source_hashes();n,tails,repeats=settings(smoke)
    protocol={'version':1,'smoke':smoke,'commands':n,'tails':tails,'repeats':repeats,'order':schedule(smoke),
              'createdUTC':datetime.now(timezone.utc).isoformat(),'runtimeEnvironment':{k:env[k] for k in ('GOMAXPROCS','GOGC','GOMEMLIMIT')},
              'cache':'warm copies/hashes; no flush','powerLossTested':False}
    save(output/'protocol.json',protocol)
    try:
        require_production_head()
        with tarfile.open(output/'source.tar.gz','x:gz') as archive:
            for name in sources:archive.add(REPO/name,arcname=name,recursive=False)
        binary=output/'statescale'
        subprocess.run([str(REPO/'scripts/go'),'build','-mod=readonly','-o',str(binary),'./tools/statescale'],cwd=REPO,env=env,check=True,timeout=180)
        go=json.loads(subprocess.check_output([str(REPO/'scripts/go'),'env','-json','GOVERSION','CGO_ENABLED','GOOS','GOARCH'],cwd=REPO,env=env))
        require(go['CGO_ENABLED']=='1' and source_hashes()==sources,'tail build source/runtime changed')
        b={'revision':subprocess.check_output(['git','rev-parse','HEAD'],cwd=REPO,text=True).strip(),'workingTreeModified':bool(subprocess.check_output(['git','status','--porcelain'],cwd=REPO)),
           'go':go,'platform':platform.platform(),'binarySHA256':digest(binary),'sourceSHA256':sources,'sourceArchiveSHA256':digest(output/'source.tar.gz'),'productionMatchesHEAD':True,
           'hardware':{k:subprocess.check_output(['sysctl','-n',k],text=True).strip() for k in ('machdep.cpu.brand_string','hw.memsize','hw.logicalcpu')}}
        save(output/'build.json',b);(output/'fixtures').mkdir();(output/'runs').mkdir();fixtures={}
        for kind in ('active','history'):
            budget(output,n*8192)
            invoke(binary,['tail-prepare','--kind',kind,'--count',n,'--dir',output/'fixtures'/kind,'--output',output/'fixtures'/(kind+'.json')],output/'fixtures'/(kind+'-prepare'),env)
            fixtures[kind]=read(output/'fixtures'/(kind+'.json'))
        extra=0
        for row in protocol['order']:
            f=fixtures[row['kind']];names={'.lock','genesis.bin','commands.wal',f"snapshot-{n-row['tail']:020d}.bin",f'snapshot-{n:020d}.bin'}
            extra+=sum((output/'fixtures'/row['kind']/name).stat().st_size for name in names)
        budget(output,extra+32*2**20)
        for i,row in enumerate(protocol['order']):
            f=fixtures[row['kind']];selected=f"snapshot-{n-row['tail']:020d}.bin";names=('.lock','genesis.bin','commands.wal',selected)
            budget(output,sum((output/'fixtures'/row['kind']/name).stat().st_size for name in set(names)|{f'snapshot-{n:020d}.bin'})+2**20)
            path=output/'runs'/f'{i:03d}';path.mkdir();(path/'run').mkdir()
            for name in names:
                source=output/'fixtures'/row['kind']/name;require(digest(source)==f['Files'][name],'tail fixture changed before copy')
                shutil.copyfile(source,path/'run'/name);require(digest(path/'run'/name)==f['Files'][name],'tail copy mismatch')
            save(path/'case.json',row);save(path/'run/.tail-owned',row)
            invoke(binary,['tail-measure','--kind',row['kind'],'--count',n,'--tail',row['tail'],'--dir',path/'run','--output',path/'result.json','--receipt',output/'fixtures'/(row['kind']+'.json')],path/'probe',env)
            require(digest(binary)==b['binarySHA256'] and source_hashes()==sources,'tail source/binary changed during measurement')
            print(json.dumps({'completed':i+1,'total':len(protocol['order']),**row}),flush=True)
        report=analyze(output);save(output/'report.json',report);(output/'report.md').write_text(markdown(report));seal(output,True)
        return report
    except BaseException as error:
        save(output/'failed.json',{'error':type(error).__name__,'message':str(error)});raise


def main():
    parser=argparse.ArgumentParser(description=__doc__);sub=parser.add_subparsers(dest='command',required=True)
    r=sub.add_parser('run');r.add_argument('--output',type=Path,required=True);r.add_argument('--smoke',action='store_true')
    a=sub.add_parser('analyze');a.add_argument('--bundle',type=Path,required=True);a.add_argument('--output',type=Path,required=True)
    args=parser.parse_args()
    if args.command=='run':report=run(args.output,args.smoke)
    else:
        root=args.bundle.resolve();verify(root);report=analyze(root);out=new_output(args.output,[root]);save(out/'report.json',report);(out/'report.md').write_text(markdown(report));seal(out,True)
    print(json.dumps({'status':'PASSED','workersVerified':report['workersVerified']}))


if __name__=='__main__':
    try:main()
    except (Exception,KeyboardInterrupt) as error:
        print(f'snapshot-tail: {error}',file=sys.stderr);sys.exit(1)
