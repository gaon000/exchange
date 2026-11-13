"""Profile current canonical validation, then compare frozen same-state recovery."""
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
from evidence import fresh_output
from read_buffer import expected_names, retire_copy
from runner import source_hashes
import snapshot_array_compare as paired
from snapshot_profile import parse_top, pprof
from snapshot_tail import expected_counts, require_production_head, validate
from state_scale import budget, invoke, values_summary
from wal_decoder_compare import environment, new_output

SCOPES={'wholeOpen':None,'snapshotRead':r'github.com/gaon000/exchange/internal/snapshot\.readWithTimings','snapshotDecode':r'github.com/gaon000/exchange/internal/snapshot\.decodePayloadWithTimings'}


def production(sources):
    return {n:s for n,s in sources.items() if (n.startswith(('internal/','cmd/')) and n.endswith('.go') and not n.endswith('_test.go')) or n in ('go.mod','.go-version')}


def workspace(root):
    require(not root.is_symlink(),'symlink workspace refused')
    p=read(child(root,'protocol.json'));require(p['experiment']=='canonical-buffer' and p['version']==1 and p['order']==paired.schedule(p['smoke']),'invalid canonical workspace')
    return p


def freeze(root,label):
    p=workspace(root);dest=root/'builds'/label
    require(not dest.exists() and label in ('baseline','candidate'),'existing/invalid frozen build')
    if label=='baseline':require_production_head()
    else:
        require(read(root/'profile-summary.json')['workersVerified']==(2 if p['smoke'] else 10),'latest profile required before candidate')
        verify(root/'builds/baseline')
    budget(root,8*2**20);dest.mkdir();env=environment();sources=source_hashes()
    with tarfile.open(dest/'source.tar.gz','x:gz') as archive:
        for name in sources:archive.add(REPO/name,arcname=name,recursive=False)
    binary=dest/'statescale'
    subprocess.run([str(REPO/'scripts/go'),'build','-mod=readonly','-o',str(binary),'./tools/statescale'],cwd=REPO,env=env,check=True,timeout=180)
    go=json.loads(subprocess.check_output([str(REPO/'scripts/go'),'env','-json','GOVERSION','CGO_ENABLED','GOOS','GOARCH'],cwd=REPO,env=env))
    require(go['CGO_ENABLED']=='1' and go['GOOS']=='darwin' and go['GOARCH']=='arm64' and source_hashes()==sources,'build/source/toolchain changed')
    b={'version':1,'label':label,'createdUTC':datetime.now(timezone.utc).isoformat(),'revision':subprocess.check_output(['git','rev-parse','HEAD'],cwd=REPO,text=True).strip(),
       'workingTreeModified':bool(subprocess.check_output(['git','status','--porcelain'],cwd=REPO)),'binarySHA256':digest(binary),'sourceSHA256':sources,'sourceArchiveSHA256':digest(dest/'source.tar.gz'),'go':go}
    save(dest/'build.json',b);seal(dest,True)
    if label=='candidate':paired.checked_builds(root)
    return b


def profile_order(smoke):
    return [dict(kind=k,commands=24 if smoke else 10000,tail=0,repeat=r,version='baseline',profiler='alloc')
            for r in range(1,(1 if smoke else 5)+1) for k in (('active','history') if r%2 else ('history','active'))]


def validate_profile(o,row,f,build,env):
    n=row['commands'];name=f'snapshot-{n:020d}.bin';r=o['Recovery']
    require(o['Version']==1 and o['Verified'] is True and o['Phase']=='snapshot-open' and o['Profiler']=='alloc' and o['Commands']==n,'profile identity mismatch')
    require(o['GoVersion']==build['go']['GOVERSION'] and o['GOMAXPROCS']==int(env['GOMAXPROCS']) and o['MemProfileRate']==65536,'profile runtime changed')
    require(int(r['SnapshotSeq'])==n and int(r['ReplayedCommands'])==0 and r['SnapshotFile']==name and not r['Skipped'],'profile recovery changed')
    require(o['SnapshotSHA256']==f['Files'][name] and o['WALSHA256']==f['Files']['commands.wal'],'profile full state changed')
    require(o['After']['TotalAlloc']>=o['Before']['TotalAlloc'],'invalid profile allocation counters')


def retire_profile(case,row,f):
    run=case/'run'
    require(not run.is_symlink() and read(child(run,'.statescale-owned'))==row,'profile scratch ownership changed; preserve it')
    require(set(x.name for x in run.iterdir())==expected_names(row)|{'.statescale-owned'},'unexpected profile scratch files; preserve it')
    (run/'.statescale-owned').unlink();save(run/'.tail-owned',row);retire_copy(case,row,f)


def profile_summary(root,output):
    p=workspace(root);build=read(root/'builds/baseline/build.json');verify(root/'builds/baseline')
    order=profile_order(p['smoke']);require(sorted(x.name for x in (root/'profiles').iterdir())==[f'{i:03d}' for i in range(len(order))],'profile workers missing/extra')
    output.mkdir();env=environment();env['PPROF_TMPDIR']=str(output);binary=root/'builds/baseline/statescale';groups={}
    for i,row in enumerate(order):
        case=root/'profiles'/f'{i:03d}';f=read(root/'fixtures'/(row['kind']+'.json'))
        require(read(case/'case.json')==row,'profile case changed')
        require(set(x.name for x in case.iterdir())=={'case.json','profile','probe.stdout','probe.stderr','verified-files.json'},'profile case/retirement files differ')
        require(set(x.name for x in (case/'profile').iterdir())=={'result.json','alloc-before.pprof','alloc-after.pprof'},'profile files differ')
        receipt=read(case/'verified-files.json');require(receipt['version']==1 and receipt['owner']==row and set(receipt['files'])==expected_names(row) and receipt['policy']=='successful scratch copy removed; immutable common fixtures retained','profile scratch receipt changed')
        for name,meta in receipt['files'].items():require(meta=={'sha256':f['Files'][name],'bytes':child(root/'fixtures'/row['kind'],name).stat().st_size},'profile scratch differs')
        o=read(case/'profile/result.json');validate_profile(o,row,f,build,p['environment']);groups.setdefault(row['kind'],[]).append((case,o))
    rows=[]
    for kind,cases in sorted(groups.items()):
        dest=output/kind;dest.mkdir();inputs=[]
        for i,(case,_) in enumerate(cases):
            delta=dest/f'delta-{i}.pprof';pprof(binary,['-proto','-base',str(case/'profile/alloc-before.pprof')],[case/'profile/alloc-after.pprof'],delta,env);inputs.append(delta)
        merged=dest/'merged.pprof';pprof(binary,['-proto'],inputs,merged,env);scopes={}
        for name,focus in SCOPES.items():
            flags=['-top','-nodecount=0','-nodefraction=0','-edgefraction=0','-sample_index=alloc_space','-unit=bytes']
            if focus:flags+=['-focus='+focus,'-relative_percentages']
            path=dest/(name+'.txt');pprof(binary,flags,[merged],path,env);data=parse_top(path.read_text())
            if not p['smoke']:require(data['total']>0 and data['functions'],'no usable allocation samples')
            data['perOperation']=data['total']/len(cases);scopes[name]=data
        rows.append({'kind':kind,'repeats':len(cases),'apiAllocMiB':values_summary([(o['After']['TotalAlloc']-o['Before']['TotalAlloc'])/2**20 for _,o in cases]),'scopes':scopes})
    return {'version':1,'workersVerified':len(order),'sampleRate':65536,'binarySHA256':build['binarySHA256'],'rows':rows,'scope':'baseline tail=0 public Open allocation-only separate workers; estimated sampled allocations, overlapping scopes; not wall-time/live heap/RSS'}


def prepare(bundle,root,smoke=False):
    require(platform.system()=='Darwin' and platform.machine()=='arm64','requires macOS arm64/APFS/cgo')
    bundle=bundle.resolve();verify(bundle);source=paired.analyze(bundle);require(source['smoke']==smoke,'input smoke/full mismatch');require_production_head()
    require(production(source_hashes())==production(read(bundle/'builds/candidate/build.json')['sourceSHA256']),'input baseline production differs')
    root=new_output(root,[bundle]);budget(root,128*2**20);env=environment()
    save(root/'protocol.json',{'version':1,'experiment':'canonical-buffer','smoke':smoke,'order':paired.schedule(smoke),'sourceBundleManifestSHA256':digest(bundle/'manifest.json'),
         'createdUTC':datetime.now(timezone.utc).isoformat(),'platform':platform.platform(),'hardware':{k:subprocess.check_output(['sysctl','-n',k],text=True).strip() for k in ('machdep.cpu.brand_string','hw.memsize','hw.logicalcpu')},'environment':{k:env[k] for k in paired.ENV_KEYS}})
    (root/'builds').mkdir();(root/'fixtures').mkdir();(root/'profiles').mkdir();(root/'runs').mkdir()
    try:
        for kind in ('active','history'):
            f=read(bundle/'fixtures'/(kind+'.json'));target=root/'fixtures'/kind;target.mkdir()
            budget(root,sum((bundle/'fixtures'/kind/name).stat().st_size for name in f['Files']))
            for name,sha in f['Files'].items():shutil.copyfile(child(bundle/'fixtures'/kind,name),target/name);require(digest(target/name)==sha,'common fixture copy differs')
            save(root/'fixtures'/(kind+'.json'),f)
        build=freeze(root,'baseline');binary=root/'builds/baseline/statescale'
        for i,row in enumerate(profile_order(smoke)):
            f=read(root/'fixtures'/(row['kind']+'.json'));case=root/'profiles'/f'{i:03d}';budget(root,sum((root/'fixtures'/row['kind']/n).stat().st_size for n in expected_names(row))+2**20)
            case.mkdir();(case/'run').mkdir()
            for name in expected_names(row):shutil.copyfile(child(root/'fixtures'/row['kind'],name),case/'run'/name)
            save(case/'case.json',row);save(case/'run/.statescale-owned',row)
            invoke(binary,['profile','--dir',case/'run','--output',case/'profile','--phase','snapshot-open','--profiler','alloc','--count',row['commands'],
                           '--snapshot-sha',f['Files'][f"snapshot-{row['commands']:020d}.bin"],'--wal-sha',f['Files']['commands.wal']],case/'probe',env)
            validate_profile(read(case/'profile/result.json'),row,f,build,{k:env[k] for k in paired.ENV_KEYS})
            require(source_hashes()==build['sourceSHA256'] and digest(binary)==build['binarySHA256'],'baseline source/binary changed during profile')
            retire_profile(case,row,f)
            print(json.dumps({'profileCompleted':i+1,**row}),flush=True)
        summary=profile_summary(root,root/'profile-analysis');save(root/'profile-summary.json',summary)
        # Seal this completed phase separately before production changes; the
        # final bundle later also includes both builds and all comparison rows.
        save(root/'prepared.json',{'version':1,'files':{str(p.relative_to(root)):digest(p) for p in root.rglob('*') if p.is_file()}})
        verify(bundle);require(digest(bundle/'manifest.json')==workspace(root)['sourceBundleManifestSHA256'],'source input changed');return summary
    except BaseException as error:save(root/'failed-prepare.json',{'error':type(error).__name__,'message':str(error)});raise


def verify_prepared(root):
    receipt=read(root/'prepared.json');require(receipt['version']==1,'prepared phase version changed')
    for name,sha in receipt['files'].items():require(digest(child(root,name))==sha,'prepared profile/baseline/common fixture changed')


def compare(root):
    p=workspace(root);verify_prepared(root);builds=paired.checked_builds(root);current=source_hashes();require(current==builds['candidate']['sourceSHA256'],'current source differs from candidate');env=environment()
    require({k:env[k] for k in paired.ENV_KEYS}==p['environment'],'comparison environment differs')
    require(not any((root/'runs').iterdir()),'comparison rows already exist')
    try:
        for i,row in enumerate(p['order']):
            f=read(root/'fixtures'/(row['kind']+'.json'));case=root/'runs'/f'{i:03d}';names=expected_names(row)
            budget(root,sum((root/'fixtures'/row['kind']/n).stat().st_size for n in names)+2**20);case.mkdir();(case/'run').mkdir()
            for name in {'.lock','genesis.bin','commands.wal',f"snapshot-{row['commands']-row['tail']:020d}.bin"}:
                source=child(root/'fixtures'/row['kind'],name);require(digest(source)==f['Files'][name],'fixture changed before copy');shutil.copyfile(source,case/'run'/name)
            save(case/'case.json',row);save(case/'run/.tail-owned',row);binary=root/'builds'/row['version']/'statescale'
            invoke(binary,['tail-measure','--resident','--kind',row['kind'],'--count',row['commands'],'--tail',row['tail'],'--dir',case/'run','--receipt',root/'fixtures'/(row['kind']+'.json'),'--output',case/'result.json'],case/'probe',env)
            o=read(case/'result.json');validate(o,row,f,builds[row['version']],p['environment']);paired.validate_resident(o)
            require(source_hashes()==current and digest(binary)==builds[row['version']]['binarySHA256'],'source/binary changed during comparison');retire_copy(case,row,f)
            print(json.dumps({'completed':i+1,'total':len(p['order']),**row}),flush=True)
        report=analyze(root);save(root/'report.json',report);(root/'report.md').write_text(markdown(report));seal(root,True);return report
    except BaseException as error:save(root/'failed-compare.json',{'error':type(error).__name__,'message':str(error)});raise


def analyze(root):
    workspace(root);verify_prepared(root);report=paired.analyze(root)
    report['experiment']='canonical-buffer';report['baselineProfile']=read(root/'profile-summary.json');return report


def markdown(report):
    return paired.markdown(report).replace('# 스냅샷 배열 할당 비교','# 스냅샷 정규형 비교 버퍼 재사용')


def main():
    p=argparse.ArgumentParser(description=__doc__);sub=p.add_subparsers(dest='command',required=True)
    a=sub.add_parser('prepare');a.add_argument('--bundle',type=Path,required=True);a.add_argument('--output',type=Path,required=True);a.add_argument('--smoke',action='store_true')
    a=sub.add_parser('freeze');a.add_argument('--workspace',type=Path,required=True);a.add_argument('--label',choices=('candidate',),required=True)
    a=sub.add_parser('compare');a.add_argument('--workspace',type=Path,required=True)
    a=sub.add_parser('analyze');a.add_argument('--bundle',type=Path,required=True);a.add_argument('--output',type=Path,required=True)
    args=p.parse_args()
    if args.command=='prepare':result=prepare(args.bundle,args.output,args.smoke)
    elif args.command=='freeze':freeze(args.workspace.resolve(),args.label);print('frozen');return
    elif args.command=='compare':result=compare(args.workspace.resolve())
    else:
        root=args.bundle.resolve();verify(root);result=analyze(root);out=fresh_output(args.output,[root]);budget(root,8*2**20);out.mkdir(mode=0o700)
        require(profile_summary(root,out/'profile-analysis')==result['baselineProfile'],'profile summary regeneration differs')
        save(out/'report.json',result);(out/'report.md').write_text(markdown(result));seal(out,True)
    print(json.dumps({'status':'PASSED','workersVerified':result['workersVerified']}))


if __name__=='__main__':
    try:main()
    except (Exception,KeyboardInterrupt) as error:print(f'canonical-buffer: {error}',file=sys.stderr);sys.exit(1)
