"""Freeze two probes and compare WAL decoder recovery on identical fixed states."""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import shutil
import subprocess
import sys
import tarfile

from common import REPO, child, digest, read, require, save, seal, verify
from evidence import fresh_output
from runner import source_hashes
from state_scale import MIN_FREE, analyze as analyze_scale, budget, invoke, metrics, validate_observation, values_summary


def environment():
    env = os.environ.copy()
    for name in ('GODEBUG', 'EXCHANGE_TRACE', 'GOFLAGS', 'GOOS', 'GOARCH'):env.pop(name, None)
    env.update(GOMAXPROCS=str(os.cpu_count()), GOGC='100', GOMEMLIMIT='2GiB', CGO_ENABLED='1')
    return env


def new_output(path, inputs=()):
    output = fresh_output(path, inputs)
    require(shutil.disk_usage(output.parent).free >= MIN_FREE+128*2**20, 'insufficient preparation space')
    output.mkdir(mode=0o700)
    return output


def freeze(output, label):
    require(platform.system() == 'Darwin' and platform.machine() == 'arm64', 'requires native macOS arm64/APFS')
    if label == 'baseline':
        subprocess.run(['git', 'diff', '--exit-code', 'HEAD', '--', 'internal', 'cmd', 'go.mod', '.go-version'], cwd=REPO, check=True, capture_output=True)
    output = new_output(output)
    env = environment()
    sources = source_hashes()
    try:
        with tarfile.open(output/'source.tar.gz', 'x:gz') as archive:
            for name in sources:archive.add(REPO/name, arcname=name, recursive=False)
        binary = output/'statescale'
        subprocess.run([str(REPO/'scripts/go'), 'build', '-mod=readonly', '-o', str(binary), './tools/statescale'], cwd=REPO, env=env, check=True, timeout=180)
        go = json.loads(subprocess.check_output([str(REPO/'scripts/go'), 'env', '-json', 'GOVERSION', 'CGO_ENABLED', 'GOOS', 'GOARCH'], cwd=REPO, env=env))
        require(go['CGO_ENABLED'] == '1' and source_hashes() == sources, 'source/toolchain changed while freezing')
        save(output/'build.json', {'version': 1, 'label': label, 'createdUTC': datetime.now(timezone.utc).isoformat(),
             'revision': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=REPO, text=True).strip(),
             'workingTreeModified': bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=REPO)),
             'binarySHA256': digest(binary), 'sourceSHA256': sources, 'sourceArchiveSHA256': digest(output/'source.tar.gz'), 'go': go})
        seal(output, True)
    except BaseException as error:
        save(output/'failed.json', {'error': type(error).__name__, 'message': str(error)})
        raise
    return read(output/'build.json')


def schedule(smoke=False):
    rows = []
    for repeat in range(1, (1 if smoke else 5)+1):
        conditions = [('active','wal'), ('history','wal'), ('active','snapshot'), ('history','snapshot')]
        if repeat % 2 == 0:conditions.reverse()
        for kind, mode in conditions:
            for version in (('baseline','candidate') if repeat % 2 else ('candidate','baseline')):
                rows.append({'kind': kind, 'mode': mode, 'commands': 12 if smoke else 10000, 'repeat': repeat, 'version': version})
    return rows


def build_pair(root, allowed_changes=('internal/wal/codec.go',)):
    builds = {}
    for side in ('baseline','candidate'):
        path = root/'builds'/side
        verify(path)
        b = read(path/'build.json')
        require(b['label'] == side and digest(path/'statescale') == b['binarySHA256'] and digest(path/'source.tar.gz') == b['sourceArchiveSHA256'], 'build hash/label mismatch')
        builds[side] = b
    a,b = builds['baseline'],builds['candidate']
    require(a['go'] == b['go'], 'toolchains differ')
    production = lambda src: {n:sha for n,sha in src.items() if (n.startswith(('internal/','cmd/')) and n.endswith('.go') and not n.endswith('_test.go')) or n in ('go.mod','.go-version')}
    old,new = production(a['sourceSHA256']),production(b['sourceSHA256'])
    require(set(old) == set(new) and sorted(n for n in old if old[n] != new[n]) == sorted(allowed_changes), 'unexpected production changes in comparison')
    probe = lambda src: {n:sha for n,sha in src.items() if n.startswith('tools/statescale/')}
    require(probe(a['sourceSHA256']) == probe(b['sourceSHA256']), 'measurement probe changed')
    return builds


def analyze(root, allowed_changes=('internal/wal/codec.go',)):
    protocol = read(root/'protocol.json')
    require(protocol['version'] == 1 and protocol['order'] == schedule(protocol['smoke']), 'comparison schedule changed')
    expected = protocol['order']
    builds = build_pair(root, allowed_changes)
    require(sorted(p.name for p in (root/'runs').iterdir()) == [f'{i:03d}' for i in range(len(expected))], 'missing/extra worker')
    groups, paired = {}, {}
    for i,row in enumerate(expected):
        path = root/'runs'/f'{i:03d}'
        require(read(path/'case.json') == row, 'comparison case changed')
        f = read(root/'fixtures'/(row['kind']+'.json'))
        require(f['version'] == 1 and f['kind'] == row['kind'] and f['commands'] == row['commands'], 'fixture identity changed')
        o = read(path/'result.json')
        validate_observation(o, row, f, protocol['runtimeEnvironment'])
        require(o['GoVersion'] == builds[row['version']]['go']['GOVERSION'], 'worker Go changed')
        for name,sha in f['files'].items():
            require(digest(child(root/'fixtures'/row['kind'], name)) == sha and digest(child(path/'run', name)) == sha, 'fixture/worker input or full state changed')
        data = metrics(o)
        key = row['kind'],row['mode'],row['version']
        groups.setdefault(key, []).append(data)
        paired.setdefault((row['kind'],row['mode'],row['repeat']), {})[row['version']] = data
    rows = []
    for kind,mode in [('active','wal'),('history','wal'),('active','snapshot'),('history','snapshot')]:
        versions = {}
        for side in ('baseline','candidate'):
            values = groups[kind,mode,side]
            require(len(values) == (1 if protocol['smoke'] else 5), 'repeat count mismatch')
            versions[side] = {name: values_summary([r[name] for r in values]) for name in values[0]}
        a,b = versions['baseline'],versions['candidate']
        deltas = {name: (b[name]['median']/a[name]['median']-1)*100 for name in ('openMs','openAllocMiB','replayMs') if a[name]['median'] > 0}
        rows.append({'kind':kind,'mode':mode,'versions':versions,'medianChangePercent':deltas,
                     'pairs':[{'repeat':rep,'openMsChangePercent':(sides['candidate']['openMs']/sides['baseline']['openMs']-1)*100} for (k,m,rep),sides in paired.items() if (k,m)==(kind,mode)]})
    return {'version':1,'workersVerified':len(expected),'repeats':1 if protocol['smoke'] else 5,'smoke':protocol['smoke'],
            'binarySHA256':{side:b['binarySHA256'] for side,b in builds.items()},'sourceBundleManifestSHA256':protocol['sourceBundleManifestSHA256'],
            'runtimeEnvironment':protocol['runtimeEnvironment'],'rows':rows,'powerLossTested':False,
            'scope':'warm-cache fixed-state non-profiled public Open/Snapshot; no HTTP throughput or power-loss claim'}


def markdown(report):
    cell = lambda x: f"{x['median']:.3f} [{x['min']:.3f}~{x['max']:.3f}]"
    text = ['# WAL 디코더 동일 상태 비교','',f"각 버전 {report['repeats']}회 중앙값 [최소~최대], {report['workersVerified']} worker 상태 일치.",'',
            '| 유형 | 복구 | 이전 Open ms | 이후 Open ms | 변화 % | 이전 할당 MiB | 이후 할당 MiB | 변화 % |','| --- | --- | --- | --- | ---: | --- | --- | ---: |']
    for row in report['rows']:
        a,b = row['versions']['baseline'],row['versions']['candidate'];d=row['medianChangePercent']
        text.append(f"| {row['kind']} | {row['mode']} | {cell(a['openMs'])} | {cell(b['openMs'])} | {d['openMs']:.2f} | {cell(a['openAllocMiB'])} | {cell(b['openAllocMiB'])} | {d['openAllocMiB']:.2f} |")
    text += ['','| 유형 | 복구 | 이전 replay ms | 이후 replay ms |','| --- | --- | --- | --- |']
    for row in report['rows']:
        a,b=row['versions']['baseline'],row['versions']['candidate']
        text.append(f"| {row['kind']} | {row['mode']} | {cell(a['replayMs'])} | {cell(b['replayMs'])} |")
    text += ['','| 유형 | 이전 새 snapshot ms | 이후 새 snapshot ms |','| --- | --- | --- |']
    for row in report['rows']:
        if row['mode']!='wal':continue
        a,b=row['versions']['baseline'],row['versions']['candidate']
        text.append(f"| {row['kind']} | {cell(a['snapshotMs'])} | {cell(b['snapshotMs'])} |")
    text += ['','전체 WAL prefix 검사·canonical 비교·strict sync 유지. 준비/복사/강제 GC/검증은 공개 호출 시간 밖이다.',
             '새 worker·같은 입력·warm-cache·비격리 호스트이며 느린 표본도 포함한다. HTTP p99/최대 처리량 개선으로 확대하지 않는다.',
             '실제 OS·전원 장애 검증은 미완료·보류다.','']
    return '\n'.join(text)


def run(bundle, baseline, candidate, output, smoke=False,
        allowed_changes=('internal/wal/codec.go',), render=markdown):
    bundle,baseline,candidate = (p.resolve() for p in (bundle,baseline,candidate))
    verify(bundle);analyze_scale(bundle)
    require(read(bundle/'protocol.json')['smoke'] == smoke, 'source fixture scope mismatch')
    verify(baseline);verify(candidate)
    output = new_output(output, [bundle,baseline,candidate])
    env = environment()
    initial_sources = source_hashes()
    protocol = {'version':1,'smoke':smoke,'order':schedule(smoke),'createdUTC':datetime.now(timezone.utc).isoformat(),
                'sourceBundle':str(bundle),'sourceBundleManifestSHA256':digest(bundle/'manifest.json'),
                'runtimeEnvironment':{k:env[k] for k in ('GOMAXPROCS','GOGC','GOMEMLIMIT')},'cache':'warm copies/hashes; no flush',
                'hardware':{k:subprocess.check_output(['sysctl','-n',k],text=True).strip() for k in ('machdep.cpu.brand_string','hw.memsize','hw.logicalcpu')},
                'platform':platform.platform(),'powerLossTested':False}
    save(output/'protocol.json',protocol)
    try:
        (output/'builds').mkdir()
        for side,source in [('baseline',baseline),('candidate',candidate)]:
            budget(output,sum(p.stat().st_size for p in source.rglob('*') if p.is_file()))
            shutil.copytree(source,output/'builds'/side)
        builds = build_pair(output, allowed_changes)
        (output/'fixtures').mkdir();(output/'runs').mkdir()
        fixtures={}
        for kind in ('active','history'):
            n=12 if smoke else 10000
            f=read(bundle/'fixtures'/f'{kind}-{n}.json');fixtures[kind]=f
            source=bundle/'fixtures'/f'{kind}-{n}'
            budget(output,sum(child(source,name).stat().st_size for name in f['files']))
            target=output/'fixtures'/kind;target.mkdir()
            for name,sha in f['files'].items():
                shutil.copyfile(source/name,target/name)
                require(digest(target/name)==sha,'copied fixture changed')
            save(output/'fixtures'/(kind+'.json'),f)
        budget(output,sum(sum((output/'fixtures'/r['kind']/name).stat().st_size for name in fixtures[r['kind']]['files']) for r in protocol['order'])+32*2**20)
        for i,row in enumerate(protocol['order']):
            f=fixtures[row['kind']];case=output/'runs'/f'{i:03d}'
            budget(output,sum((output/'fixtures'/row['kind']/name).stat().st_size for name in f['files'])+2**20)
            case.mkdir();(case/'run').mkdir()
            for name,sha in f['files'].items():
                if row['mode']=='wal' and name.startswith('snapshot-'):continue
                shutil.copyfile(output/'fixtures'/row['kind']/name,case/'run'/name)
                require(digest(case/'run'/name)==sha,'worker copy changed')
            save(case/'case.json',row);save(case/'run/.statescale-owned',row)
            binary=output/'builds'/row['version']/'statescale'
            args=['measure','--dir',case/'run','--count',row['commands'],'--mode',row['mode'],'--snapshot-sha',f['files'][f"snapshot-{row['commands']:020d}.bin"],'--wal-sha',f['files']['commands.wal'],'--output',case/'result.json']
            invoke(binary,args,case/'probe',env)
            require(digest(binary)==builds[row['version']]['binarySHA256'] and source_hashes()==initial_sources,'binary/current source changed during comparison')
            print(json.dumps({'completed':i+1,'total':len(protocol['order']),**row}),flush=True)
        report=analyze(output, allowed_changes);save(output/'report.json',report);(output/'report.md').write_text(render(report))
        verify(bundle);require(digest(bundle/'manifest.json')==protocol['sourceBundleManifestSHA256'],'source evidence changed')
        seal(output,True)
        return report
    except BaseException as error:
        save(output/'failed.json',{'error':type(error).__name__,'message':str(error)});raise


def main():
    parser=argparse.ArgumentParser(description=__doc__);sub=parser.add_subparsers(dest='command',required=True)
    f=sub.add_parser('freeze');f.add_argument('--output',type=Path,required=True);f.add_argument('--label',choices=('baseline','candidate'),required=True)
    r=sub.add_parser('run')
    for name in ('bundle','baseline','candidate','output'):r.add_argument('--'+name,type=Path,required=True)
    r.add_argument('--smoke',action='store_true')
    a=sub.add_parser('analyze');a.add_argument('--bundle',type=Path,required=True);a.add_argument('--output',type=Path,required=True)
    args=parser.parse_args()
    if args.command=='freeze':freeze(args.output,args.label);print('frozen');return
    if args.command=='run':report=run(args.bundle,args.baseline,args.candidate,args.output,args.smoke)
    else:
        root=args.bundle.resolve();verify(root);report=analyze(root);out=new_output(args.output,[root]);save(out/'report.json',report);(out/'report.md').write_text(markdown(report));seal(out,True)
    print(json.dumps({'status':'PASSED','workersVerified':report['workersVerified']}))


if __name__=='__main__':
    try:main()
    except (Exception,KeyboardInterrupt) as error:
        print(f'wal-decoder-compare: {error}',file=sys.stderr);sys.exit(1)
