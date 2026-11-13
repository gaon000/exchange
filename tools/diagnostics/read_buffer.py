"""Compare decoder-only build overlays and untimed ReaderAt/Pread diagnostics."""
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
from snapshot_tail import analyze as analyze_tail, expected_counts, metrics, require_production_head, validate
from state_scale import budget, invoke, values_summary
from wal_decoder_compare import environment, new_output

BUFFERS=(32,64,128,256)
SOURCE='internal/snapshot/decode.go'
TARGET='bufio.NewReaderSize(r, bufferBytes)'
TEST_SOURCE='internal/snapshot/codec_test.go'
TEST_TARGET='if len(b) > bufferBytes {\n\t\treturn 0, fmt.Errorf("unbounded read: %d", len(b))'


def variant(source,kib):
    require(kib in BUFFERS and source.count(TARGET)==1,'decoder overlay target changed')
    return source.replace(TARGET,f'bufio.NewReaderSize(r, {kib*1024})')


def test_variant(source,kib):
    require(kib in BUFFERS and source.count(TEST_TARGET)==1,'bounded test overlay target changed')
    return source.replace(TEST_TARGET,TEST_TARGET.replace('> bufferBytes','> '+str(kib*1024)))


def schedule(smoke=False):
    n,tails,repeats=(24,[0,8,12],1) if smoke else (10000,[0,1000,5000],5)
    conditions=[(kind,tail) for tail in tails for kind in ('active','history')];rows=[]
    for repeat in range(1,repeats+1):
        offset=(repeat-1)%6;order=conditions[offset:]+conditions[:offset]
        if repeat%2==0:order.reverse()
        for i,(kind,tail) in enumerate(order):
            offset=(repeat+i-1)%4;buffers=BUFFERS[offset:]+BUFFERS[:offset]
            if repeat%2==0:buffers=buffers[::-1]
            rows.extend(dict(kind=kind,tail=tail,commands=n,repeat=repeat,bufferKiB=k) for k in buffers)
    return rows


def freeze(output):
    require(platform.system()=='Darwin' and platform.machine()=='arm64','requires macOS arm64/APFS/cgo')
    require_production_head();output=new_output(output);sources=source_hashes();source=(REPO/SOURCE).read_text();test_source=(REPO/TEST_SOURCE).read_text();env=environment()
    with tarfile.open(output/'source.tar.gz','x:gz') as archive:
        for name in sources:archive.add(REPO/name,arcname=name,recursive=False)
    go=json.loads(subprocess.check_output([str(REPO/'scripts/go'),'env','-json','GOVERSION','CGO_ENABLED','GOOS','GOARCH','GOEXPERIMENT'],cwd=REPO,env=env))
    require(go['CGO_ENABLED']=='1','native cgo required');builds={}
    for kib in BUFFERS:
        budget(output,32*2**20);path=output/str(kib);path.mkdir();(path/'decode.go').write_text(variant(source,kib))
        (path/'codec_test.go').write_text(test_variant(test_source,kib))
        save(path/'overlay.json',{'Replace':{str(REPO/SOURCE):str(path/'decode.go'),str(REPO/TEST_SOURCE):str(path/'codec_test.go')}})
        binary=path/'statescale'
        subprocess.run([str(REPO/'scripts/go'),'build','-mod=readonly','-overlay',str(path/'overlay.json'),'-ldflags',f'-X main.readBufferLabel={kib*1024}','-o',str(binary),'./tools/statescale'],cwd=REPO,env=env,check=True,timeout=180)
        require(source_hashes()==sources,'source changed while freezing buffers')
        builds[str(kib)]={'binarySHA256':digest(binary),'decoderSHA256':digest(path/'decode.go'),'overlaySHA256':digest(path/'overlay.json'),'testDecoderSHA256':digest(path/'codec_test.go')}
    b={'version':1,'revision':subprocess.check_output(['git','rev-parse','HEAD'],cwd=REPO,text=True).strip(),
       'workingTreeModified':bool(subprocess.check_output(['git','status','--porcelain'],cwd=REPO)),'sourceRoot':str(REPO),'buildRoot':str(output),
       'sourceSHA256':sources,'sourceArchiveSHA256':digest(output/'source.tar.gz'),'go':go,'buffers':builds,'onlyOverlaySource':SOURCE,'testOverlaySource':TEST_SOURCE,
       'hardware':{k:subprocess.check_output(['sysctl','-n',k],text=True).strip() for k in ('machdep.cpu.brand_string','hw.memsize','hw.logicalcpu')}}
    save(output/'build.json',b);seal(output,True);return b


def check_builds(root):
    verify(root);b=read(root/'build.json')
    require(b['version']==1 and b['onlyOverlaySource']==SOURCE and b.get('testOverlaySource')==TEST_SOURCE and set(b['buffers'])==set(map(str,BUFFERS)),'buffer builds mismatch')
    require(digest(root/'source.tar.gz')==b['sourceArchiveSHA256'],'buffer source archive changed')
    with tarfile.open(root/'source.tar.gz') as archive:
        source=archive.extractfile(SOURCE).read().decode();test_source=archive.extractfile(TEST_SOURCE).read().decode()
    for kib in BUFFERS:
        p=root/str(kib);entry=b['buffers'][str(kib)]
        require((p/'decode.go').read_text()==variant(source,kib) and digest(p/'decode.go')==entry['decoderSHA256'],'unexpected decoder overlay')
        require((p/'codec_test.go').read_text()==test_variant(test_source,kib) and digest(p/'codec_test.go')==entry['testDecoderSHA256'],'unexpected test overlay')
        require(read(p/'overlay.json')=={'Replace':{str(Path(b['sourceRoot'])/SOURCE):str(Path(b['buildRoot'])/str(kib)/'decode.go'),str(Path(b['sourceRoot'])/TEST_SOURCE):str(Path(b['buildRoot'])/str(kib)/'codec_test.go')}} and digest(p/'overlay.json')==entry['overlaySHA256'],'overlay mapping changed')
        require(digest(p/'statescale')==entry['binarySHA256'],'frozen buffer binary changed')
    return b


def validate_io(o,row,f,payload):
    size=row['bufferKiB']*1024;cut=row['commands']-row['tail'];name=f'snapshot-{cut:020d}.bin'
    require(o['Version']==1 and o['Verified'] is True and o['BufferBytes']==size and int(o['SnapshotSeq'])==cut and o['SnapshotFile']==name and o['SnapshotSHA256']==f['Files'][name] and o['PayloadBytes']==payload and payload>0,'buffer IO identity changed')
    require(o['FileReadAt']==o['DirectPread'],'File.ReadAt/Pread diagnostic requests differ')
    expected={'header':(20,20),'checksum':(payload+20,32768),'digest':(32,32),'decode':(payload,size)}
    require(set(o['FileReadAt'])==set(expected),'IO phases missing')
    for phase,(n,quantum) in expected.items():
        v=o['FileReadAt'][phase];hist={}
        if n//quantum:hist[str(quantum)]=n//quantum
        if n%quantum:hist[str(n%quantum)]=hist.get(str(n%quantum),0)+1
        require(v['Calls']==(n+quantum-1)//quantum and v['RequestedBytes']==n and v['ReadBytes']==n and v['MaxRequest']==min(n,quantum) and v['Sizes']==hist,'IO phase request/count/bytes mismatch')
    v=o['PreadCalls'];calls=sum(v['Calls'] for v in o['DirectPread'].values())
    require(type(o['Interrupted']) is int and type(o['ShortReads']) is int and o['Interrupted']>=0 and o['ShortReads']>=0,'invalid Pread retries')
    require(v['Calls']==calls+o['Interrupted']+o['ShortReads'] and v['ReadBytes']==2*payload+72 and v['RequestedBytes']>=v['ReadBytes'],'Pread counts/bytes mismatch')
    require(sum(v['Sizes'].values())==v['Calls'] and sum(int(k)*n for k,n in v['Sizes'].items())==v['RequestedBytes'] and max(map(int,v['Sizes']))==v['MaxRequest'] and 0<v['MaxRequest']<=max(size,32768),'Pread histogram mismatch')


def expected_names(row):
    n=row['commands'];return {'.lock','genesis.bin','commands.wal',f"snapshot-{n-row['tail']:020d}.bin",f'snapshot-{n:020d}.bin'}


def retire_copy(case,row,f):
    """Remove only this successful owned scratch copy after recording all hashes."""
    run=case/'run';names=expected_names(row)
    require(not run.is_symlink() and read(child(run,'.tail-owned'))==row,'scratch ownership mismatch')
    require(set(p.name for p in run.iterdir())==names|{'.tail-owned'},'unexpected scratch file; preserve it')
    files={}
    for name in names:
        p=child(run,name);sha=digest(p);require(sha==f['Files'][name],'scratch changed; preserve it')
        files[name]={'sha256':sha,'bytes':p.stat().st_size}
    save(case/'verified-files.json',{'version':1,'owner':row,'files':files,'policy':'successful scratch copy removed; immutable common fixtures retained'})
    for name in names|{'.tail-owned'}:child(run,name).unlink()
    run.rmdir()


def analyze(root):
    p=read(root/'protocol.json');b=check_builds(root/'builds');order=schedule(p['smoke'])
    require(p['version']==1 and p['order']==order and p['buffersKiB']==list(BUFFERS),'buffer experiment schedule mismatch')
    require(sorted(x.name for x in (root/'runs').iterdir())==[f'{i:03d}' for i in range(len(order))],'missing/extra buffer workers')
    fixtures={};groups={};pairs={}
    for kind in ('active','history'):
        f=read(root/'fixtures'/(kind+'.json'));require(f['Version']==1 and f['Kind']==kind and f['Commands']==order[0]['commands'] and f['Counts']==expected_counts(kind,f['Commands']),'buffer fixture identity changed')
        for name,sha in f['Files'].items():require(digest(child(root/'fixtures'/kind,name))==sha,'buffer common fixture changed')
        fixtures[kind]=f
    for i,row in enumerate(order):
        case=root/'runs'/f'{i:03d}';f=fixtures[row['kind']];require(read(case/'case.json')==row,'buffer case order changed')
        require(set(x.name for x in case.iterdir())=={'case.json','result.json','io.json','verified-files.json','probe.stdout','probe.stderr'},'incomplete buffer case/retirement')
        o=read(case/'result.json');validate(o,row,f,b,p['environment'])
        io=read(case/'io.json');snapshot=f"snapshot-{row['commands']-row['tail']:020d}.bin";payload=(root/'fixtures'/row['kind']/snapshot).stat().st_size-52;validate_io(io,row,f,payload)
        receipt=read(case/'verified-files.json');require(receipt['version']==1 and receipt['owner']==row and set(receipt['files'])==expected_names(row),'scratch receipt mismatch')
        for name,meta in receipt['files'].items():require(meta=={'sha256':f['Files'][name],'bytes':(root/'fixtures'/row['kind']/name).stat().st_size},'scratch receipt state differs')
        m=metrics(o);m.update(checksumCalls=io['FileReadAt']['checksum']['Calls'],decodeCalls=io['FileReadAt']['decode']['Calls'],readAtCalls=sum(x['Calls'] for x in io['FileReadAt'].values()),preadCalls=io['PreadCalls']['Calls'],preadInterrupted=io['Interrupted'],preadShortReads=io['ShortReads'])
        groups.setdefault((row['kind'],row['tail'],row['bufferKiB']),[]).append(m)
        pairs.setdefault((row['kind'],row['tail'],row['repeat']),{})[row['bufferKiB']]=m['openMs']
    rows=[]
    for (kind,tail,kib),values in sorted(groups.items()):
        require(len(values)==(1 if p['smoke'] else 5),'insufficient buffer repeats')
        paired=[v for (k,t,_),v in pairs.items() if k==kind and t==tail]
        rows.append({'kind':kind,'tail':tail,'bufferKiB':kib,'metrics':{name:values_summary([v[name] for v in values]) for name in values[0]},
                     'pairedOpenChangesPercent':[(v[kib]/v[32]-1)*100 for v in paired]})
    report={'version':1,'workersVerified':len(order),'commands':order[0]['commands'],'smoke':p['smoke'],'rows':rows,'sourceBundleManifestSHA256':p['sourceBundleManifestSHA256'],
            'buildRevision':b['revision'],'binarySHA256':{k:v['binarySHA256'] for k,v in b['buffers'].items()},'firstResponsesVerified':len(order)*8,'retriesVerified':len(order)*2,
            'scope':'timed public Open without IO observers; same prefix snapshot reread outside timing via File.ReadAt and direct syscall.Pread; warm cache; successful owned scratch copies retired with receipts',
            'productionDefaultKiB':32,'powerLossTested':False}
    return report


def markdown(report):
    lines=['# 디코더 읽기 버퍼 비교','',
           '| 상태 | 이후 명령 | KiB | Open ms 중앙값 [최소~최대] | 할당 MiB | decode ReadAt 횟수 | checksum 횟수 | Pread 횟수 |',
           '| --- | ---: | ---: | --- | ---: | ---: | ---: | ---: |']
    for row in report['rows']:
        m=row['metrics'];o=m['openMs'];lines.append(f"| {row['kind']} | {row['tail']} | {row['bufferKiB']} | {o['median']:.3f} [{o['min']:.3f}~{o['max']:.3f}] | {m['openAllocMiB']['median']:.3f} | {m['decodeCalls']['median']:g} | {m['checksumCalls']['median']:g} | {m['preadCalls']['median']:g} |")
    lines+=['','IO 횟수는 동일한 선택 snapshot의 별도 진단 Read에서 센 값이며 Open 자체의 커널 trace가 아니다. 시간/메모리에는 관찰자가 없다.',
            'checksum/쓰기/confirm 버퍼와 두 checksum·canonical/복구 검증·strict sync는 유지했다. 기본 생산 버퍼는 32KiB다.',
            'warm-cache·비격리 호스트이며 실패/느린 표본을 대체하지 않는다. 실제 전원 장애 검증은 미완료·보류다.','']
    return '\n'.join(lines)


def run(bundle,builds,output,smoke=False):
    bundle=bundle.resolve();builds=builds.resolve();verify(bundle);analyze_tail(bundle);b=check_builds(builds)
    require(read(bundle/'protocol.json')['smoke']==smoke,'input smoke/full mismatch');require_production_head()
    current=source_hashes();require(current==b['sourceSHA256'],'source differs from frozen buffer builds')
    old=read(bundle/'build.json')['sourceSHA256'];production=lambda s:{k:v for k,v in s.items() if k.startswith(('internal/','cmd/')) or k in ('go.mod','.go-version')}
    require(production(old)==production(current),'unexpected production changes')
    output=new_output(output,[bundle,builds]);env=environment();order=schedule(smoke)
    save(output/'protocol.json',{'version':1,'smoke':smoke,'order':order,'buffersKiB':list(BUFFERS),'sourceBundleManifestSHA256':digest(bundle/'manifest.json'),
                                'createdUTC':datetime.now(timezone.utc).isoformat(),'environment':{k:env[k] for k in ('GOMAXPROCS','GOGC','GOMEMLIMIT')}})
    try:
        budget(output,sum(p.stat().st_size for p in builds.rglob('*') if p.is_file())+64*2**20);shutil.copytree(builds,output/'builds')
        (output/'fixtures').mkdir();(output/'runs').mkdir();fixtures={}
        for kind in ('active','history'):
            f=read(bundle/'fixtures'/(kind+'.json'));target=output/'fixtures'/kind;target.mkdir()
            budget(output,sum((bundle/'fixtures'/kind/name).stat().st_size for name in f['Files'])+2**20)
            for name,sha in f['Files'].items():shutil.copyfile(child(bundle/'fixtures'/kind,name),target/name);require(digest(target/name)==sha,'buffer fixture copy differs')
            save(output/'fixtures'/(kind+'.json'),f);fixtures[kind]=f
        for i,row in enumerate(order):
            f=fixtures[row['kind']];case=output/'runs'/f'{i:03d}';names=expected_names(row)
            budget(output,sum((output/'fixtures'/row['kind']/name).stat().st_size for name in names)+2**20)
            case.mkdir();(case/'run').mkdir()
            selected=f"snapshot-{row['commands']-row['tail']:020d}.bin"
            for name in {'.lock','genesis.bin','commands.wal',selected}:
                source=output/'fixtures'/row['kind']/name;require(digest(source)==f['Files'][name],'buffer fixture changed before copy');shutil.copyfile(source,case/'run'/name)
            save(case/'case.json',row);save(case/'run/.tail-owned',row)
            binary=output/'builds'/str(row['bufferKiB'])/'statescale'
            invoke(binary,['buffer-measure','--kind',row['kind'],'--count',row['commands'],'--tail',row['tail'],'--dir',case/'run','--receipt',output/'fixtures'/(row['kind']+'.json'),'--output',case/'result.json','--io-output',case/'io.json'],case/'probe',env)
            validate(read(case/'result.json'),row,f,b,{k:env[k] for k in ('GOMAXPROCS','GOGC','GOMEMLIMIT')})
            payload=(output/'fixtures'/row['kind']/selected).stat().st_size-52;validate_io(read(case/'io.json'),row,f,payload)
            require(source_hashes()==current and digest(binary)==b['buffers'][str(row['bufferKiB'])]['binarySHA256'],'source/buffer binary changed')
            retire_copy(case,row,f);print(json.dumps({'completed':i+1,'total':len(order),**row}),flush=True)
        report=analyze(output);save(output/'report.json',report);(output/'report.md').write_text(markdown(report));verify(bundle);seal(output,True);return report
    except BaseException as error:save(output/'failed.json',{'error':type(error).__name__,'message':str(error)});raise


def main():
    p=argparse.ArgumentParser(description=__doc__);sub=p.add_subparsers(dest='command',required=True)
    f=sub.add_parser('freeze');f.add_argument('--output',type=Path,required=True)
    r=sub.add_parser('run');r.add_argument('--bundle',type=Path,required=True);r.add_argument('--builds',type=Path,required=True);r.add_argument('--output',type=Path,required=True);r.add_argument('--smoke',action='store_true')
    a=sub.add_parser('analyze');a.add_argument('--bundle',type=Path,required=True);a.add_argument('--output',type=Path,required=True)
    args=p.parse_args()
    if args.command=='freeze':freeze(args.output);print(json.dumps({'status':'FROZEN','buffersKiB':BUFFERS}));return
    if args.command=='run':report=run(args.bundle,args.builds,args.output,args.smoke)
    else:
        root=args.bundle.resolve();verify(root);report=analyze(root);out=new_output(args.output,[root]);save(out/'report.json',report);(out/'report.md').write_text(markdown(report));seal(out,True)
    print(json.dumps({'status':'PASSED','workersVerified':report['workersVerified']}))


if __name__=='__main__':
    try:main()
    except (Exception,KeyboardInterrupt) as error:print(f'read-buffer: {error}',file=sys.stderr);sys.exit(1)
