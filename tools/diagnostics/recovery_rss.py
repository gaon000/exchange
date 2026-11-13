"""Diagnose recovery RSS across idle, extra GC and explicit OS release stages."""
import argparse
from datetime import datetime, timezone
import hashlib
import io
import json
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile

import canonical_buffer
from common import REPO, child, digest, read, require, save, seal, verify
from read_buffer import expected_names, retire_copy
from runner import source_hashes
import snapshot_array_compare as paired
from snapshot_tail import validate
from state_scale import budget, invoke, values_summary
from wal_decoder_compare import environment, new_output

SOURCE='internal/snapshot/decode.go'


def schedule(smoke=False):
    result=[]
    for repeat in range(1,(1 if smoke else 5)+1):
        for i,kind in enumerate(('active','history') if repeat%2 else ('history','active')):
            sides=('baseline','candidate') if (repeat+(kind=='history'))%2 else ('candidate','baseline')
            result.extend(dict(kind=kind,commands=24 if smoke else 10000,tail=0,repeat=repeat,version=side) for side in sides)
    return result


def sample_order(smoke):
    idle=[0,1,5,10,20] if smoke else [0,100,1000,5000,15000]
    control=[0,1,5] if smoke else [0,100,1000]
    return [('before-open',0),('after-open',0)]+[('idle',t) for t in idle]+[(s,t) for s in ('gc-only','force-release') for t in control]


def production(src):
    return {n:s for n,s in src.items() if (n.startswith(('internal/','cmd/')) and not n.endswith('_test.go')) or n in ('go.mod','.go-version')}


def freeze(root,bundle):
    current=source_hashes();env=environment();(root/'builds').mkdir()
    for side in ('baseline','candidate'):
        original=read(bundle/'builds'/side/'build.json')
        with tarfile.open(bundle/'builds'/side/'source.tar.gz') as archive:decoder=archive.extractfile(SOURCE).read()
        sha=hashlib.sha256(decoder).hexdigest();require(sha==original['sourceSHA256'][SOURCE],'original decoder differs')
        sources={**current,SOURCE:sha};dest=root/'builds'/side;budget(root,12*2**20);dest.mkdir();(dest/'decode.go').write_bytes(decoder)
        mapping={'Replace':{str(REPO/SOURCE):str(dest/'decode.go')}};save(dest/'overlay.json',mapping)
        with tarfile.open(dest/'source.tar.gz','x:gz') as archive:
            for name in sources:
                if name==SOURCE:
                    info=tarfile.TarInfo(name);info.size=len(decoder);info.mode=0o600;archive.addfile(info,io.BytesIO(decoder))
                else:archive.add(REPO/name,arcname=name,recursive=False)
        binary=dest/'statescale'
        subprocess.run([str(REPO/'scripts/go'),'build','-mod=readonly','-overlay',str(dest/'overlay.json'),'-o',str(binary),'./tools/statescale'],cwd=REPO,env=env,check=True,timeout=180)
        go=json.loads(subprocess.check_output([str(REPO/'scripts/go'),'env','-json','GOVERSION','CGO_ENABLED','GOOS','GOARCH'],cwd=REPO,env=env))
        require(source_hashes()==current and go['CGO_ENABLED']=='1' and go['GOOS']=='darwin' and go['GOARCH']=='arm64','source/toolchain changed while freezing')
        save(dest/'build.json',{'version':1,'label':side,'revision':subprocess.check_output(['git','rev-parse','HEAD'],cwd=REPO,text=True).strip(),
             'workingTreeModified':bool(subprocess.check_output(['git','status','--porcelain'],cwd=REPO)),'sourceRoot':str(REPO),'buildRoot':str(dest),
             'sourceSHA256':sources,'binarySHA256':digest(binary),'sourceArchiveSHA256':digest(dest/'source.tar.gz'),'decoderSHA256':sha,'overlaySHA256':digest(dest/'overlay.json'),'go':go})
        seal(dest,True)
    return check_builds(root)


def check_builds(root):
    builds=paired.checked_builds(root)
    for side,b in builds.items():
        dest=root/'builds'/side
        require(digest(dest/'decode.go')==b['decoderSHA256']==b['sourceSHA256'][SOURCE] and digest(dest/'overlay.json')==b['overlaySHA256'],'decoder overlay differs')
        require(read(dest/'overlay.json')=={'Replace':{str(Path(b['sourceRoot'])/SOURCE):str(Path(b['buildRoot'])/'decode.go')}},'unexpected overlay mapping')
    require(production(builds['candidate']['sourceSHA256'])==production(read(root/'prior.json')['builds']['candidate']['sourceSHA256']),'current production differs from prior candidate')
    for side,b in builds.items():require(production(b['sourceSHA256'])==production(read(root/'prior.json')['builds'][side]['sourceSHA256']),'diagnostic production differs from prior version')
    return builds


def validate_rss(o,row,f,build,protocol):
    smoke=protocol['smoke']
    require(o['Version']==1 and o['Smoke']==smoke and o['Verified'] is True and not o.get('Error'),'RSS worker failed/incomplete')
    validate(o['Tail'],row,f,build,protocol['environment']);paired.validate_resident(o['Tail'])
    samples=o['Samples'];expected=sample_order(smoke)
    require([(s['Stage'],s['TargetMillis']) for s in samples]==expected,'RSS sample order differs')
    require([a['Kind'] for a in o['Actions']]==['gc-only','force-release'] and all(type(a['DurationNanos']) is int and a['DurationNanos']>0 for a in o['Actions']),'RSS diagnostic actions missing')
    previous={}
    for s in samples:
        m=s['Memory'];vm=s['VM'];stage=s['Stage']
        require(type(s['ElapsedNanos']) is int and s['ElapsedNanos']>=s['TargetMillis']*10**6 and s['ElapsedNanos']>=previous.get(stage,0) and s['CaptureNanos']>0,'invalid RSS timing window')
        previous[stage]=s['ElapsedNanos']
        require(all(type(x) is int and x>=0 for x in [*m.values(),*vm.values()]),'invalid memory counter')
        require(0<s['RSSBytes']<=2*2**30 and 0<s['PeakRSSBytes']<=2*2**30 and 0<vm['ResidentBytes']<=2*2**30 and 0<vm['PhysicalFootprintBytes']<=2*2**30 and 0<m['HeapAlloc']<=2*2**30,'RSS memory budget/availability violated')
        require(m['HeapReleased']<=m['HeapIdle'] and m['HeapIdle']+m['HeapInuse']==m['HeapSys'] and m['HeapAlloc']<=m['HeapInuse'] and m['StackInuse']<=m['StackSys'] and m['Sys']>=m['HeapReleased'],'Go memory relation differs')
    require(samples[2]['Memory']['NumGC']>samples[1]['Memory']['NumGC'] and samples[7]['Memory']['NumGC']>samples[6]['Memory']['NumGC'] and samples[10]['Memory']['NumGC']>samples[9]['Memory']['NumGC'],'expected diagnostic GC absent')


def metrics(s):
    m=s['Memory'];vm=s['VM'];result={'rssMiB':s['RSSBytes']/2**20,'peakRSSMiB':s['PeakRSSBytes']/2**20,'captureMs':s['CaptureNanos']/1e6,'latenessMs':s['ElapsedNanos']/1e6-s['TargetMillis'],'goroutines':s['Goroutines']}
    for name in ('HeapAlloc','HeapInuse','HeapSys','HeapIdle','HeapReleased','StackInuse','StackSys','GCSys','OtherSys','Sys'):
        result[name+'MiB']=m[name]/2**20
    result.update(heapObjects=m['HeapObjects'],numGC=m['NumGC'],retainedIdleMiB=(m['HeapIdle']-m['HeapReleased'])/2**20,unusedInuseMiB=(m['HeapInuse']-m['HeapAlloc'])/2**20,goSysLessReleasedMiB=(m['Sys']-m['HeapReleased'])/2**20)
    for name in ('ResidentBytes','PhysicalFootprintBytes','ReusableBytes','CompressedBytes','InternalBytes','ExternalBytes'):
        result[name.replace('Bytes','MiB')]=vm[name]/2**20
    return result


def analyze(root):
    p=read(root/'protocol.json');require(p['version']==1 and p['experiment']=='recovery-rss' and p['order']==schedule(p['smoke']),'RSS protocol differs');builds=check_builds(root)
    require(p['environment']['GOGC']=='100' and p['environment']['GOMEMLIMIT']=='2GiB' and int(p['environment']['GOMAXPROCS'])>0,'RSS environment/budget differs')
    require(sorted(x.name for x in (root/'runs').iterdir())==[f'{i:03d}' for i in range(len(p['order']))],'RSS workers missing/extra')
    fixtures={}
    for kind in ('active','history'):
        f=read(root/'fixtures'/(kind+'.json'));row={'commands':24 if p['smoke'] else 10000,'tail':0}
        require(set(x.name for x in (root/'fixtures'/kind).iterdir())==expected_names(row),'RSS common fixture file set differs')
        for name in expected_names(row):require(digest(child(root/'fixtures'/kind,name))==f['Files'][name],'RSS common fixture differs')
        fixtures[kind]=f
    prior=read(root/'prior.json');prior_order=[r for r in paired.schedule(p['smoke']) if r['tail']==0]
    require([x['case'] for x in prior['observations']]==prior_order,'prior RSS cases differ')
    previous={}
    for x in prior['observations']:
        row=x['case'];o=x['result'];validate(o,row,fixtures[row['kind']],prior['builds'][row['version']],prior['environment']);paired.validate_resident(o)
        m=o['Open']['AfterGC'];previous.setdefault((row['kind'],row['version']),[]).append({'rssMiB':o['RSSAfterGCBytes']/2**20,'heapMiB':m['HeapAlloc']/2**20,'heapInuseMiB':m['HeapInuse']/2**20,'heapSysMiB':m['HeapSys']/2**20})
    groups={};by_case={}
    for i,row in enumerate(p['order']):
        case=root/'runs'/f'{i:03d}';require(read(case/'case.json')==row,'RSS case changed');f=fixtures[row['kind']]
        paired.validate_receipt(case,row,f,root/'fixtures'/row['kind']);o=read(case/'result.json');validate_rss(o,row,f,builds[row['version']],p)
        for s in o['Samples']:
            groups.setdefault((row['kind'],row['version'],s['Stage'],s['TargetMillis']),[]).append(metrics(s))
            by_case.setdefault((row['kind'],row['repeat'],s['Stage'],s['TargetMillis']),{})[row['version']]=metrics(s)
    rows=[]
    for (kind,side,stage,t),values in sorted(groups.items()):
        require(len(values)==(1 if p['smoke'] else 5),'RSS repeats missing')
        rows.append({'kind':kind,'version':side,'stage':stage,'targetMillis':t,'metrics':{k:values_summary([v[k] for v in values]) for k in values[0]}})
    pairs=[]
    for (kind,stage,t) in sorted({(k,s,t) for k,_,s,t in by_case}):
        values=[v for (k,_,s,target),v in sorted(by_case.items()) if (k,s,target)==(kind,stage,t)]
        deltas=[v['candidate']['rssMiB']-v['baseline']['rssMiB'] for v in values]
        pairs.append({'kind':kind,'stage':stage,'targetMillis':t,'rssCandidateMinusBaselineMiB':deltas,'positivePairs':sum(x>0 for x in deltas)})
    return {'version':1,'workersVerified':len(p['order']),'repeats':1 if p['smoke'] else 5,'smoke':p['smoke'],'rows':rows,'pairs':pairs,
            'prior':[{**dict(zip(('kind','version'),key)),'metrics':{k:values_summary([v[k] for v in values]) for k in values[0]}} for key,values in sorted(previous.items())],
            'binarySHA256':{s:b['binarySHA256'] for s,b in builds.items()},'firstResponsesVerified':len(p['order'])*8,'retriesVerified':len(p['order'])*2,'sourceBundleManifestSHA256':p['sourceBundleManifestSHA256'],
            'scope':'instrumented fresh-worker recovery RSS diagnosis, tail=0; idle/GC-only/FreeOSMemory precede all queries and validation snapshot; not uninstrumented performance or leak absence proof', 'powerLossTested':False}


def markdown(report):
    lines=['# 복구 뒤 RSS 진단','',f"버전/상태별 {report['repeats']}회, {report['workersVerified']} worker. 값은 중앙값 MiB.",'',
           '| 상태 | 버전 | 단계 | 목표 ms | RSS | footprint | live heap | HeapReleased | retained idle | stack | reusable |',
           '| --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |']
    for r in report['rows']:
        m=r['metrics'];names=('rssMiB','PhysicalFootprintMiB','HeapAllocMiB','HeapReleasedMiB','retainedIdleMiB','StackInuseMiB','ReusableMiB')
        lines.append('| '+' | '.join([r['kind'],r['version'],r['stage'],str(r['targetMillis']),*[f"{m[n]['median']:.3f}" for n in names]])+' |')
    return '\n'.join(lines+['','RSS/Go heap/footprint/reusable은 다른 계수이며 더해서 총량을 만들지 않는다. 강제 반환은 진단 자식 프로세스에만 적용한다.',
                              '자연 유휴와 추가 GC/FreeOSMemory 단계를 분리하고 모든 상태/최초 응답/재시도를 뒤에서 검증했다. 비격리 호스트·warm-cache이며 실제 전원 장애는 미완료·보류다.',''])


def run(bundle,output,smoke=False):
    require(platform.system()=='Darwin' and platform.machine()=='arm64','requires macOS arm64/APFS/cgo')
    bundle=bundle.resolve();verify(bundle);source=canonical_buffer.analyze(bundle);require(source['smoke']==smoke,'input smoke/full differs')
    require(production(source_hashes())==production(read(bundle/'builds/candidate/build.json')['sourceSHA256']),'current production differs from input candidate')
    output=new_output(output,[bundle]);env=environment();current=source_hashes()
    protocol={'version':1,'experiment':'recovery-rss','smoke':smoke,'order':schedule(smoke),'sourceBundleManifestSHA256':digest(bundle/'manifest.json'),'createdUTC':datetime.now(timezone.utc).isoformat(),
              'platform':platform.platform(),'hardware':{k:subprocess.check_output(['sysctl','-n',k],text=True).strip() for k in ('machdep.cpu.brand_string','hw.memsize','hw.logicalcpu')},'environment':{k:env[k] for k in paired.ENV_KEYS}}
    save(output/'protocol.json',protocol)
    try:
        previous=[]
        for i,row in enumerate(read(bundle/'protocol.json')['order']):
            if row['tail']==0:previous.append({'case':row,'result':read(bundle/'runs'/f'{i:03d}'/'result.json')})
        save(output/'prior.json',{'environment':read(bundle/'protocol.json')['environment'],'builds':{s:read(bundle/'builds'/s/'build.json') for s in ('baseline','candidate')},'observations':previous})
        (output/'fixtures').mkdir();(output/'runs').mkdir()
        for kind in ('active','history'):
            f=read(bundle/'fixtures'/(kind+'.json'));target=output/'fixtures'/kind;target.mkdir();names=expected_names({'commands':24 if smoke else 10000,'tail':0})
            budget(output,sum((bundle/'fixtures'/kind/n).stat().st_size for n in names)+2**20)
            for name in names:shutil.copyfile(child(bundle/'fixtures'/kind,name),target/name);require(digest(target/name)==f['Files'][name],'RSS fixture copy differs')
            save(output/'fixtures'/(kind+'.json'),f)
        builds=freeze(output,bundle)
        for i,row in enumerate(protocol['order']):
            case=output/'runs'/f'{i:03d}';f=read(output/'fixtures'/(row['kind']+'.json'));names=expected_names(row)
            budget(output,sum((output/'fixtures'/row['kind']/n).stat().st_size for n in names)+2**20);case.mkdir();(case/'run').mkdir()
            for name in names:
                source=child(output/'fixtures'/row['kind'],name);require(digest(source)==f['Files'][name],'RSS fixture changed before copy');shutil.copyfile(source,case/'run'/name)
            save(case/'case.json',row);save(case/'run/.tail-owned',row);binary=output/'builds'/row['version']/'statescale'
            invoke(binary,['rss-measure','--dir',case/'run','--receipt',output/'fixtures'/(row['kind']+'.json'),'--output',case/'result.json',*(['--smoke'] if smoke else [])],case/'probe',env)
            validate_rss(read(case/'result.json'),row,f,builds[row['version']],protocol)
            require(source_hashes()==current and digest(binary)==builds[row['version']]['binarySHA256'],'source/binary changed during RSS diagnosis')
            retire_copy(case,row,f);print(json.dumps({'completed':i+1,'total':len(protocol['order']),**row}),flush=True)
        result=analyze(output);save(output/'report.json',result);(output/'report.md').write_text(markdown(result));verify(bundle);seal(output,True);return result
    except BaseException as error:save(output/'failed.json',{'error':type(error).__name__,'message':str(error)});raise


def main():
    p=argparse.ArgumentParser(description=__doc__);sub=p.add_subparsers(dest='command',required=True)
    a=sub.add_parser('run');a.add_argument('--bundle',type=Path,required=True);a.add_argument('--output',type=Path,required=True);a.add_argument('--smoke',action='store_true')
    a=sub.add_parser('analyze');a.add_argument('--bundle',type=Path,required=True);a.add_argument('--output',type=Path,required=True)
    args=p.parse_args()
    if args.command=='run':r=run(args.bundle,args.output,args.smoke)
    else:
        root=args.bundle.resolve();verify(root);r=analyze(root);out=new_output(args.output,[root]);save(out/'report.json',r);(out/'report.md').write_text(markdown(r));seal(out,True)
    print(json.dumps({'status':'PASSED','workersVerified':r['workersVerified']}))


if __name__=='__main__':
    try:main()
    except (Exception,KeyboardInterrupt) as error:print(f'recovery-rss: {error}',file=sys.stderr);sys.exit(1)
