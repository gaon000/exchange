"""Bounded fixed-state size/cost experiment, isolated probe processes and reports."""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import shutil
import statistics
import subprocess
import sys
import tarfile

from common import REPO, child, digest, read, require, save, seal, verify
from evidence import fresh_output
from runner import source_hashes

MIN_FREE = 8*2**30
MAX_BYTES = 4*2**30


def schedule(sizes, repeats):
    conditions = [(kind, n) for n in sizes for kind in ('active', 'history')]
    rows = []
    for repeat in range(1, repeats+1):
        offset = (repeat-1) % len(conditions)
        ordered = conditions[offset:]+conditions[:offset]
        if repeat % 2 == 0:
            ordered.reverse()
        for kind, n in ordered:
            for mode in (('wal', 'snapshot') if repeat % 2 else ('snapshot', 'wal')):
                rows.append({'kind': kind, 'commands': n, 'repeat': repeat, 'mode': mode})
    return rows


def budget(root, extra=0):
    used = sum(p.stat().st_size for p in root.rglob('*') if p.is_file())
    require(used+extra <= MAX_BYTES, 'experiment exceeds 4GiB budget')
    require(shutil.disk_usage(root).free >= MIN_FREE+extra, 'experiment requires 8GiB free reserve')


def invoke(binary, args, log, env):
    with Path(str(log)+'.stdout').open('x') as stdout, Path(str(log)+'.stderr').open('x') as stderr:
        result = subprocess.run([str(binary), *map(str, args)], cwd=REPO, env=env,
                                stdout=stdout, stderr=stderr, timeout=180)
    require(result.returncode == 0, f'probe failed: {log}.stderr (exit {result.returncode})')


def probe_args(root, row, fixture):
    name = next(n for n in fixture['files'] if n.startswith('snapshot-'))
    return ['measure', '--dir', root/'run', '--count', row['commands'], '--mode', row['mode'],
            '--snapshot-sha', fixture['files'][name], '--wal-sha', fixture['files']['commands.wal'],
            '--output', root/'result.json']


def run(output, smoke=False):
    sizes, repeats = ([12], 1) if smoke else ([1000, 5000, 10000], 5)
    require(platform.system() == 'Darwin' and platform.machine() == 'arm64', 'requires macOS arm64/APFS/cgo')
    clean = os.environ.copy()
    for key in ('GOFLAGS', 'GOOS', 'GOARCH'):
        clean.pop(key, None)
    go_env = json.loads(subprocess.check_output([str(REPO/'scripts/go'), 'env', '-json', 'GOVERSION', 'GOARCH', 'GOOS', 'CGO_ENABLED'], cwd=REPO, env=clean))
    require(go_env['CGO_ENABLED'] == '1', 'native cgo build required')
    output = fresh_output(output)
    require(shutil.disk_usage(output.parent).free >= MIN_FREE+128*2**20, 'insufficient preparation space')
    output.mkdir(mode=0o700)
    env = os.environ.copy()
    for key in ('GODEBUG', 'EXCHANGE_TRACE', 'GOFLAGS', 'GOOS', 'GOARCH'):
        env.pop(key, None)
    env.update(GOMAXPROCS=str(os.cpu_count()), GOGC='100', GOMEMLIMIT='2GiB', CGO_ENABLED='1')
    protocol = {'version': 1, 'sizes': sizes, 'repeats': repeats, 'order': schedule(sizes, repeats),
                'createdUTC': datetime.now(timezone.utc).isoformat(), 'smoke': smoke,
                'cache': 'warm; copies/hashes before timing; no cache flush', 'powerLossTested': False,
                'runtimeEnvironment': {k: env[k] for k in ('GOMAXPROCS', 'GOGC', 'GOMEMLIMIT')},
                'memory': 'post-GC live heap delta; phase total allocation; Darwin cumulative ru_maxrss before validation'}
    save(output/'protocol.json', protocol)
    try:
        initial_sources = source_hashes()
        sources = output/'source.tar.gz'
        with tarfile.open(sources, 'x:gz') as archive:
            for name in initial_sources:
                archive.add(REPO/name, arcname=name, recursive=False)
        binary = output/'statescale'
        subprocess.run([str(REPO/'scripts/go'), 'build', '-mod=readonly', '-o', str(binary), './tools/statescale'],
                       cwd=REPO, env=env, check=True, timeout=180)
        metadata = {'go': go_env, 'platform': platform.platform(), 'revision': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=REPO, text=True).strip(),
                    'workingTreeModified': bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=REPO)),
                    'binarySHA256': digest(binary), 'sourceSHA256': initial_sources, 'sourceArchiveSHA256': digest(sources),
                    'hardware': {k: subprocess.check_output(['sysctl', '-n', k], text=True).strip()
                                 for k in ('machdep.cpu.brand_string', 'hw.memsize', 'hw.logicalcpu')}}
        save(output/'build.json', metadata)
        (output/'fixtures').mkdir()
        (output/'runs').mkdir()
        fixtures = {}
        for n in sizes:
            for kind in ('active', 'history'):
                name = f'{kind}-{n}'
                budget(output, n*4096)
                invoke(binary, ['prepare', '--kind', kind, '--count', n, '--dir', output/'fixtures'/name,
                                '--output', output/'fixtures'/(name+'.json')], output/'fixtures'/(name+'-prepare'), env)
                fixtures[name] = read(output/'fixtures'/(name+'.json'))
        projected = sum(sum((output/'fixtures'/name/f).stat().st_size for f in value['files'])
                        for name, value in fixtures.items())*2*repeats
        budget(output, projected+32*2**20)
        for i, row in enumerate(protocol['order']):
            name = f"{row['kind']}-{row['commands']}"
            value = fixtures[name]
            path = output/'runs'/f'{i:03d}'
            path.mkdir()
            target = path/'run'
            target.mkdir()
            budget(output, sum((output/'fixtures'/name/f).stat().st_size for f in value['files']))
            for filename, sha in value['files'].items():
                source = output/'fixtures'/name/filename
                require(digest(source) == sha, 'fixture changed before copy')
                if filename.startswith('snapshot-') and row['mode'] == 'wal':
                    continue
                shutil.copyfile(source, target/filename)
                require(digest(target/filename) == sha, 'copy hash mismatch')
            save(target/'.statescale-owned', row)
            save(path/'case.json', row)
            invoke(binary, probe_args(path, row, value), path/'probe', env)
            # Genesis, lock and WAL bytes must survive every measured Open.
            for filename in ('.lock', 'genesis.bin', 'commands.wal'):
                require(digest(target/filename) == value['files'][filename], 'worker changed immutable input')
            require(digest(binary) == metadata['binarySHA256'] and source_hashes() == initial_sources, 'source or binary changed during experiment')
            print(json.dumps({'completed': i+1, 'total': len(protocol['order']), **row}), flush=True)
        report = analyze(output)
        save(output/'report.json', report)
        (output/'report.md').write_text(markdown(report))
        seal(output, True)
        return report
    except BaseException as error:
        save(output/'failed.json', {'error': type(error).__name__, 'message': str(error)})
        raise


def values_summary(values):
    require(values and all(type(v) in (int, float) and v == v and abs(v) != float('inf') for v in values), 'invalid numeric observations')
    return {'count': len(values), 'median': statistics.median(values), 'min': min(values), 'max': max(values)}


def stage(stages, name):
    value = stages[name]
    require(int(value['count']) == 1 and not value['sumOverflow'] and int(value['negativeSamples']) == 0, 'invalid stage observation')
    require(int(value['sumNanos']) >= 0, 'negative stage total')
    return int(value['sumNanos'])/1e6


def validate_observation(o, row, fixture, environment):
    require(o['Version'] == 1 and o['Verified'] is True and o['Mode'] == row['mode'] and o['Commands'] == row['commands'], 'probe identity or verification mismatch')
    require(o['Publication'] == ('new' if row['mode'] == 'wal' else 'reconfirm'), 'publication type mismatch')
    require(o['GOMAXPROCS'] == int(environment['GOMAXPROCS']), 'runtime setting mismatch')
    seq, replay = (0, row['commands']) if row['mode'] == 'wal' else (row['commands'], 0)
    r = o['Recovery']
    require(int(r['SnapshotSeq']) == seq and int(r['ReplayedCommands']) == replay and not r['Skipped']
            and bool(r['SnapshotFile']) == (row['mode'] == 'snapshot'), 'recovery path mismatch')
    snapshot_name = next(n for n in fixture['files'] if n.startswith('snapshot-'))
    require(o['SnapshotSHA256'] == fixture['files'][snapshot_name] and o['WALSHA256'] == fixture['files']['commands.wal'], 'state/input hash mismatch')
    for name in ('Open', 'Snapshot'):
        p = o[name]
        require(type(p['WallNanos']) is int and p['WallNanos'] > 0 and 0 < p['PeakRSSBytes'] <= 2*2**30, 'invalid phase timing/memory')
        require(p['After']['TotalAlloc'] >= p['Before']['TotalAlloc'] and p['After']['Mallocs'] >= p['Before']['Mallocs'], 'memory counters decreased')
        require(p['AfterGC']['NumGC'] > p['After']['NumGC'], 'post-phase GC observation missing')


def metrics(o):
    p = o['Open']
    result = {'openMs': p['WallNanos']/1e6, 'heapAfterGC_MiB': p['AfterGC']['HeapAlloc']/2**20,
              'heapDeltaMiB': (p['AfterGC']['HeapAlloc']-p['Before']['HeapAlloc'])/2**20,
              'openAllocMiB': (p['After']['TotalAlloc']-p['Before']['TotalAlloc'])/2**20,
              'openPeakRSSMiB': p['PeakRSSBytes']/2**20,
              'replayMs': stage(o['Recovery']['Stages'], 'recovery.replay')}
    if o['Mode'] == 'snapshot':
        for label, name in [('loadMs', 'recovery.load'), ('restoreMs', 'recovery.restore'), ('confirmMs', 'recovery.confirmSnapshot')]:
            result[label] = stage(o['Recovery']['Stages'], name)
    else:
        p = o['Snapshot']
        result.update(snapshotMs=p['WallNanos']/1e6,
                      snapshotAllocMiB=(p['After']['TotalAlloc']-p['Before']['TotalAlloc'])/2**20,
                      beforeValidationPeakRSSMiB=p['PeakRSSBytes']/2**20)
        for label, name in [('copyMs', 'capture.copy'), ('sortMs', 'capture.sort'), ('encodeWriteMs', 'publish.encodeWrite'),
                            ('fileSyncMs', 'publish.fileSync'), ('directorySyncMs', 'publish.directorySync')]:
            result[label] = stage(o['SnapshotStages'], name)
    return result


def analyze(root):
    protocol = read(root/'protocol.json')
    build = read(root/'build.json')
    require(digest(root/'statescale') == build['binarySHA256'] and digest(root/'source.tar.gz') == build['sourceArchiveSHA256'], 'frozen build changed')
    require(protocol['version'] == 1 and protocol['repeats'] in (1, 5), 'unsupported experiment protocol')
    require(protocol['sizes'] == ([12] if protocol['smoke'] else [1000, 5000, 10000]), 'unexpected sizes')
    expected = schedule(protocol['sizes'], protocol['repeats'])
    require(protocol['order'] == expected and protocol['repeats'] == (1 if protocol['smoke'] else 5), 'schedule changed')
    require(sorted(p.name for p in (root/'runs').iterdir()) == [f'{i:03d}' for i in range(len(expected))], 'missing or extra repeated observation')
    grouped, fixtures = {}, {}
    for i, row in enumerate(expected):
        name = f"{row['kind']}-{row['commands']}"
        if name not in fixtures:
            f = read(root/'fixtures'/(name+'.json'))
            require(f['version'] == 1 and f['kind'] == row['kind'] and f['commands'] == row['commands'], 'fixture identity mismatch')
            n = row['commands']
            counts = {'Orders': n, 'Active': n, 'Filled': 0, 'Cancelled': 0, 'Trades': 0, 'Results': n}
            if row['kind'] == 'history':
                counts = {'Orders': n//2, 'Active': 0, 'Filled': n//4, 'Cancelled': n//4, 'Trades': n//4, 'Results': n}
            require(f['counts'] == counts, 'fixture composition mismatch')
            require(set(f['files']) == {'.lock', 'genesis.bin', 'commands.wal', f'snapshot-{n:020d}.bin'}, 'fixture file set mismatch')
            for filename, sha in f['files'].items():
                require(digest(child(root/'fixtures'/name, filename)) == sha, 'original fixture changed')
            fixtures[name] = f
        f = fixtures[name]
        path = root/'runs'/f'{i:03d}'
        require(read(path/'case.json') == row, 'case ordering mismatch')
        o = read(path/'result.json')
        validate_observation(o, row, f, protocol['runtimeEnvironment'])
        require(o['GoVersion'] == build['go']['GOVERSION'], 'probe toolchain changed')
        for filename, sha in f['files'].items():
            require(digest(child(path/'run', filename)) == sha, 'retained worker state/input differs')
        grouped.setdefault((name, row['mode']), []).append(metrics(o))
    rows = []
    for n in protocol['sizes']:
        for kind in ('active', 'history'):
            name = f'{kind}-{n}'
            f = fixtures[name]
            result = {'kind': kind, 'commands': n, 'counts': f['counts'], 'inputBytes': {p: (root/'fixtures'/name/p).stat().st_size for p in f['files']}, 'modes': {}}
            for mode in ('wal', 'snapshot'):
                data = grouped[(name, mode)]
                require(len(data) == protocol['repeats'], 'insufficient repeats')
                result['modes'][mode] = {metric: values_summary([x[metric] for x in data]) for metric in data[0]}
            rows.append(result)
    return {'version': 1, 'repeats': protocol['repeats'], 'workersVerified': len(expected), 'smoke': protocol['smoke'],
            'binarySHA256': build['binarySHA256'], 'sourceArchiveSHA256': build['sourceArchiveSHA256'], 'runtimeEnvironment': protocol['runtimeEnvironment'],
            'rows': rows, 'powerLossTested': False, 'performanceImprovementClaimed': False,
            'scope': 'isolated warm-cache fixed-state Open/Snapshot; not HTTP latency/throughput or power-loss validation'}


def markdown(report):
    cell = lambda x: f"{x['median']:.3f} [{x['min']:.3f}~{x['max']:.3f}]"
    text = ['# 상태 크기별 비용', '', f"각 값: {report['repeats']}회 중앙값 [최소~최대]. 검증 worker {report['workersVerified']}개.", '',
            '| 유형 | 명령 | 주문/활성/체결 | snapshot MiB |', '| --- | ---: | --- | ---: |']
    for row in report['rows']:
        size = sum(v for k, v in row['inputBytes'].items() if k.startswith('snapshot-'))/2**20
        c = row['counts']
        text.append(f"| {row['kind']} | {row['commands']} | {c['Orders']}/{c['Active']}/{c['Trades']} | {size:.3f} |")
    text += ['', '| 유형 | 명령 | WAL heap 증가 MiB | snapshot heap 증가 MiB | WAL Open ms | snapshot Open ms | 새 snapshot ms |', '| --- | ---: | --- | --- | --- | --- | --- |']
    for row in report['rows']:
        w, s = row['modes']['wal'], row['modes']['snapshot']
        text.append(f"| {row['kind']} | {row['commands']} | {cell(w['heapDeltaMiB'])} | {cell(s['heapDeltaMiB'])} | {cell(w['openMs'])} | {cell(s['openMs'])} | {cell(w['snapshotMs'])} |")
    text += ['', '| 유형 | 명령 | 복사 ms | 정렬 ms | encode/write ms | file sync ms | directory sync ms | 생성 할당 MiB | 검증 전 누적 peak RSS MiB |', '| --- | ---: | --- | --- | --- | --- | --- | --- | --- |']
    for row in report['rows']:
        w = row['modes']['wal']
        values = [cell(w[n]) for n in ('copyMs', 'sortMs', 'encodeWriteMs', 'fileSyncMs', 'directorySyncMs', 'snapshotAllocMiB', 'beforeValidationPeakRSSMiB')]
        text.append('| '+ ' | '.join([row['kind'], str(row['commands']), *values])+' |')
    text += ['', '입력 준비/사본/해시/강제 GC/상태 검증은 wall 시간 밖이다. heap 증가는 Open 전후 강제 GC 뒤 HeapAlloc 차이이며 engine만의 크기가 아니다.',
             'RSS는 새 worker 시작 이후 검증 전까지의 누적 high-water이며 snapshot phase만의 peak가 아니다. snapshot 복구의 기존 파일 재확인 시간은 새 생성 통계에 섞지 않는다.',
             'snapshot tail=0이어도 전체 보존 WAL prefix를 검사한다. warm-cache, 비격리 호스트, 각 5회 관찰이며 HTTP p99/최대 처리량·개선 효과로 확대하지 않는다.',
             '실제 OS·전원 장애 검증은 미완료·보류다.', '']
    return '\n'.join(text)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    start = sub.add_parser('run')
    start.add_argument('--output', type=Path, required=True)
    start.add_argument('--smoke', action='store_true')
    report = sub.add_parser('analyze')
    report.add_argument('--bundle', type=Path, required=True)
    report.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.command == 'run':
        result = run(args.output.absolute(), args.smoke)
    else:
        verify(args.bundle)
        result = analyze(args.bundle)
        output = fresh_output(args.output)
        output.mkdir()
        save(output/'report.json', result)
        (output/'report.md').write_text(markdown(result))
        seal(output, True)
    print(json.dumps({'status': 'PASSED', 'workersVerified': result['workersVerified']}))


if __name__ == '__main__':
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        print(f'state-scale: {error}', file=sys.stderr)
        sys.exit(1)
