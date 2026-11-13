"""Profile fixed state Open/Snapshot using fresh workers, preserving source evidence."""
import argparse
from datetime import datetime, timezone
import json
import os
from pathlib import Path
import platform
import re
import shutil
import subprocess
import sys
import tarfile

from common import REPO, child, digest, read, require, save, seal, verify
from evidence import fresh_output
from runner import source_hashes
from state_scale import MIN_FREE, analyze as analyze_scale, budget, invoke

PHASES = ('wal-open', 'snapshot-open', 'create')
FOCUS = 'github.com/gaon000/exchange/internal/'


def prepare_output(path, inputs):
    """Reject insufficient preparation space before creating or building output."""
    output = fresh_output(path, inputs)
    require(shutil.disk_usage(output.parent).free >= MIN_FREE+128*2**20,
            'insufficient preparation space (8GiB reserve plus 128MiB)')
    output.mkdir(mode=0o700)
    return output


def schedule(smoke=False):
    result = []
    for profiler in ('cpu', 'alloc'):
        for repeat in range(1, (1 if smoke else (40 if profiler == 'cpu' else 5))+1):
            phases = PHASES if repeat % 2 else PHASES[::-1]
            for phase in phases:
                if not smoke and profiler == 'cpu' and phase != 'create' and repeat > 20:
                    continue
                for kind in (('active', 'history') if repeat % 2 else ('history', 'active')):
                    result.append(dict(kind=kind, phase=phase, profiler=profiler, repeat=repeat))
    return result


def pprof(binary, args, inputs, output, env):
    with output.open('x') as target, Path(str(output)+'.stderr').open('x') as error:
        subprocess.run([str(REPO/'scripts/go'), 'tool', 'pprof', *args, str(binary), *map(str, inputs)],
                       stdout=target, stderr=error, env=env, cwd=REPO, check=True, timeout=180)


def parse_top(text):
    """Read fixed-unit pprof text; retain flat and cumulative weights separately."""
    total = re.search(r' of ([\d.]+)(?:ns|B) total', text)
    # When all nodes account for the profile, pprof omits the second total.
    if total is None:
        total = re.search(r'Showing nodes accounting for ([\d.]+)(?:ns|B),', text)
    rows = []
    for line in text.splitlines():
        m = re.match(r'^\s*(-?[\d.]+)(?:ns|B)?\s+(-?[\d.]+)%\s+(-?[\d.]+)%\s+(-?[\d.]+)(?:ns|B)?\s+(-?[\d.]+)%\s+(.+)$', line)
        if m:
            rows.append({'function': m[6], 'flat': float(m[1]), 'flatPercent': float(m[2]),
                         'cumulative': float(m[4]), 'cumulativePercent': float(m[5])})
    return {'total': float(total[1]) if total else 0, 'functions': rows}


def summarize(root, output, env):
    """Verify retained inputs, merge each profile group and generate reports."""
    protocol, build = read(root/'protocol.json'), read(root/'build.json')
    require(protocol['version'] == 1 and protocol['order'] == schedule(protocol['smoke']), 'profile schedule mismatch')
    require(protocol['commands'] == (12 if protocol['smoke'] else 10000) and protocol['allocationSampleRate'] == 65536, 'profile configuration mismatch')
    require(digest(root/'statescale') == build['binarySHA256'] and digest(root/'source.tar.gz') == build['sourceArchiveSHA256'], 'profile build changed')
    binary = root/'statescale'
    require(sorted(p.name for p in (root/'runs').iterdir()) == [f'{i:03d}' for i in range(len(protocol['order']))], 'profile workers missing/extra')
    groups = {}
    for i, row in enumerate(protocol['order']):
        case = root/'runs'/f'{i:03d}'
        require(read(case/'case.json') == row, 'profile case mismatch')
        f = read(root/'fixtures'/(row['kind']+'.json'))
        o = read(case/'profile/result.json')
        require(o['Version'] == 1 and o['Verified'] is True and o['Phase'] == row['phase'] and o['Profiler'] == row['profiler'] and o['Commands'] == protocol['commands'], 'profile result mismatch')
        require(o['GoVersion'] == build['go']['GOVERSION'] and o['GOMAXPROCS'] == int(protocol['environment']['GOMAXPROCS']), 'profile environment mismatch')
        require(o['MemProfileRate'] == (65536 if row['profiler'] == 'alloc' else 524288), 'profile sampling mismatch')
        seq = protocol['commands'] if row['phase'] == 'snapshot-open' else 0
        r = o['Recovery']
        require(int(r['SnapshotSeq']) == seq and int(r['ReplayedCommands']) == protocol['commands']-seq and not r['Skipped'] and bool(r['SnapshotFile']) == bool(seq), 'profile recovery path mismatch')
        snapshot = f"snapshot-{protocol['commands']:020d}.bin"
        require(o['SnapshotSHA256'] == f['files'][snapshot] and o['WALSHA256'] == f['files']['commands.wal'], 'profile state mismatch')
        for filename, sha in f['files'].items():
            require(digest(child(root/'fixtures'/row['kind'], filename)) == sha and digest(child(case/'run', filename)) == sha, 'profile fixture/copy changed')
        key = (row['kind'], row['phase'], row['profiler'])
        groups.setdefault(key, []).append(case)
    rows = []
    for (kind, phase, profiler), cases in groups.items():
        dest = output/f'{kind}-{phase}-{profiler}'
        dest.mkdir()
        inputs = []
        if profiler == 'cpu':
            inputs = [p/'profile/cpu.pprof' for p in cases]
        else:
            for i, p in enumerate(cases):
                delta = dest/f'delta-{i}.pprof'
                pprof(binary, ['-proto', '-base', str(p/'profile/alloc-before.pprof')], [p/'profile/alloc-after.pprof'], delta, env)
                inputs.append(delta)
        merged = dest/'merged.pprof'
        pprof(binary, ['-proto'], inputs, merged, env)
        raw = dest/'raw.txt'
        pprof(binary, ['-raw'], [merged], raw, env)
        period = re.search(r'^Period: (\d+)$', raw.read_text(), re.M)
        metrics = ('cpu',) if profiler == 'cpu' else ('alloc_space', 'alloc_objects')
        data = {}
        for metric in metrics:
            unit = 'ns' if metric == 'cpu' else 'bytes'
            # Object counts use unit=count and are retained as raw text only.
            for focused in (False, True):
                path = dest/(metric+('-internal' if focused else '-all')+'.txt')
                flags = ['-top', '-nodecount=0', '-nodefraction=0', '-edgefraction=0', '-sample_index='+metric]
                if metric != 'alloc_objects':flags += ['-unit='+unit]
                if focused:flags += ['-focus='+FOCUS, '-relative_percentages']
                pprof(binary, flags, [merged], path, env)
                if metric != 'alloc_objects':
                    parsed = parse_top(path.read_text())
                    if not protocol['smoke']:
                        require(parsed['total'] > 0 and parsed['functions'], 'no usable profile samples')
                    parsed['perOperation'] = parsed['total']/len(cases)
                    data[metric+('-internal' if focused else '-all')] = parsed
        exact = [read(p/'profile/result.json') for p in cases]
        rows.append({'kind': kind, 'phase': phase, 'profiler': profiler, 'repeats': len(cases),
                     'cpuSamplePeriodNanos': int(period[1]) if profiler == 'cpu' and period else None,
                     'cpuSamples': round(data['cpu-all']['total']/int(period[1])) if profiler == 'cpu' and period else None,
                     'apiTotalAllocBytes': [o['After']['TotalAlloc']-o['Before']['TotalAlloc'] for o in exact],
                     'profiles': data, 'artifacts': str(dest.relative_to(output))})
    report = {'version': 1, 'workersVerified': len(protocol['order']), 'commands': protocol['commands'],
              'sourceBundleManifestSHA256': protocol['sourceBundleManifestSHA256'],
              'binarySHA256': build['binarySHA256'], 'allocationSampleRate': 65536,
              'scope': 'CPU samples and estimated allocation deltas; profiling runs are not baseline wall-time or throughput measurements',
              'powerLossTested': False, 'performanceImprovementClaimed': False, 'rows': rows}
    save(output/'report.json', report)
    (output/'report.md').write_text(markdown(report))
    return report


def markdown(report):
    text = ['# 스냅샷 CPU·할당 프로파일', '',
            '| 유형 | 경로 | 계측 | 반복 | internal CPU ms 또는 추정 할당 MiB / 호출 |',
            '| --- | --- | --- | ---: | ---: |']
    for row in report['rows']:
        metric = 'cpu-internal' if row['profiler'] == 'cpu' else 'alloc_space-internal'
        scale = 1e6 if row['profiler'] == 'cpu' else 2**20
        text.append(f"| {row['kind']} | {row['phase']} | {row['profiler']} | {row['repeats']} | {row['profiles'][metric]['perOperation']/scale:.3f} |")
    text += ['', 'CPU는 표본의 합이며 wall 시간이 아니다. allocation은 64KiB 샘플링의 baseline 차이이며 추정값이다.',
             'internal focus는 해당 호출 경로를 포함한 표본이다. 상위 cumulative 값은 중첩되어 합산하지 않는다.',
             '원본 비계측 성능 결과와 혼합하지 않는다. 실제 전원 장애 검증은 미완료·보류다.', '']
    return '\n'.join(text)


def run(bundle, output, smoke=False):
    require(platform.system() == 'Darwin' and platform.machine() == 'arm64', 'requires native macOS arm64/APFS')
    bundle = bundle.resolve()
    original = verify(bundle)
    analyze_scale(bundle)
    commands = 12 if smoke else 10000
    require(read(bundle/'protocol.json')['smoke'] == smoke, 'source bundle smoke/full mismatch')
    output = prepare_output(output, [bundle])
    env = os.environ.copy()
    for name in ('GODEBUG', 'EXCHANGE_TRACE', 'GOFLAGS', 'GOOS', 'GOARCH'):env.pop(name, None)
    env.update(GOMAXPROCS=str(os.cpu_count()), GOGC='100', GOMEMLIMIT='2GiB', CGO_ENABLED='1')
    env['PPROF_TMPDIR'] = str(output)
    protocol = {'version': 1, 'commands': commands, 'smoke': smoke, 'order': schedule(smoke),
                'createdUTC': datetime.now(timezone.utc).isoformat(),
                'sourceBundle': str(bundle), 'sourceBundleManifestSHA256': digest(bundle/'manifest.json'),
                'environment': {k: env[k] for k in ('GOMAXPROCS', 'GOGC', 'GOMEMLIMIT')},
                'allocationSampleRate': 65536, 'cache': 'warm copies/hashes, no cache flush', 'powerLossTested': False}
    save(output/'protocol.json', protocol)
    try:
        sources = source_hashes()
        old = read(bundle/'build.json')['sourceSHA256']
        production = {n: sha for n, sha in old.items() if n.startswith(('internal/', 'cmd/')) or n in ('go.mod', '.go-version')}
        require(all(sources[n] == sha for n, sha in production.items()), 'production source differs from fixed-state baseline')
        with tarfile.open(output/'source.tar.gz', 'x:gz') as archive:
            for name in sources:archive.add(REPO/name, arcname=name, recursive=False)
        binary = output/'statescale'
        subprocess.run([str(REPO/'scripts/go'), 'build', '-mod=readonly', '-o', str(binary), './tools/statescale'], cwd=REPO, env=env, check=True, timeout=180)
        build = {'revision': subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=REPO, text=True).strip(),
                 'workingTreeModified': bool(subprocess.check_output(['git', 'status', '--porcelain'], cwd=REPO)),
                 'binarySHA256': digest(binary), 'sourceSHA256': sources, 'sourceArchiveSHA256': digest(output/'source.tar.gz'),
                 'go': json.loads(subprocess.check_output([str(REPO/'scripts/go'), 'env', '-json', 'GOVERSION', 'CGO_ENABLED', 'GOOS', 'GOARCH'], cwd=REPO, env=env)),
                 'platform': platform.platform(), 'cpuCount': os.cpu_count(), 'sourceBundleVerification': original,
                 'productionSourceIdentical': True, 'productionSourceSHA256': production,
                 'hardware': {k: subprocess.check_output(['sysctl', '-n', k], text=True).strip() for k in ('machdep.cpu.brand_string', 'hw.memsize', 'hw.logicalcpu')}}
        save(output/'build.json', build)
        require(build['go']['CGO_ENABLED'] == '1', 'native cgo required')
        (output/'fixtures').mkdir()
        (output/'runs').mkdir()
        fixtures = {}
        for kind in ('active', 'history'):
            f = read(bundle/'fixtures'/f'{kind}-{commands}.json')
            source = bundle/'fixtures'/f'{kind}-{commands}'
            budget(output, sum(child(source, name).stat().st_size for name in f['files']))
            target = output/'fixtures'/kind
            target.mkdir()
            for name, sha in f['files'].items():
                shutil.copyfile(child(bundle/'fixtures'/f'{kind}-{commands}', name), target/name)
                require(digest(target/name) == sha, 'fixture copy differs')
            save(output/'fixtures'/(kind+'.json'), f)
            fixtures[kind] = f
        predicted = sum(sum((output/'fixtures'/row['kind']/n).stat().st_size for n in fixtures[row['kind']]['files']) for row in protocol['order'])
        budget(output, predicted+128*2**20)
        for i, row in enumerate(protocol['order']):
            f = fixtures[row['kind']]
            case = output/'runs'/f'{i:03d}'
            case.mkdir(); (case/'run').mkdir()
            budget(output, sum((output/'fixtures'/row['kind']/n).stat().st_size for n in f['files'])+2**20)
            for name, sha in f['files'].items():
                if name.startswith('snapshot-') and row['phase'] != 'snapshot-open':continue
                shutil.copyfile(output/'fixtures'/row['kind']/name, case/'run'/name)
                require(digest(case/'run'/name) == sha, 'worker copy differs')
            save(case/'run/.statescale-owned', row)
            save(case/'case.json', row)
            args = ['profile', '--dir', case/'run', '--output', case/'profile', '--phase', row['phase'], '--profiler', row['profiler'],
                    '--count', commands, '--snapshot-sha', f['files'][f'snapshot-{commands:020d}.bin'], '--wal-sha', f['files']['commands.wal']]
            invoke(binary, args, case/'probe', env)
            require(source_hashes() == sources and digest(binary) == build['binarySHA256'], 'source/binary changed during profiles')
            print(json.dumps({'completed': i+1, 'total': len(protocol['order']), **row}), flush=True)
        (output/'analysis').mkdir()
        result = summarize(output, output/'analysis', env)
        require(verify(bundle) == original and digest(bundle/'manifest.json') == protocol['sourceBundleManifestSHA256'], 'source evidence changed')
        seal(output, True)
        return result
    except BaseException as error:
        save(output/'failed.json', {'error': type(error).__name__, 'message': str(error)})
        raise


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    start = sub.add_parser('run')
    start.add_argument('--bundle', type=Path, required=True)
    start.add_argument('--output', type=Path, required=True)
    start.add_argument('--smoke', action='store_true')
    again = sub.add_parser('analyze')
    again.add_argument('--bundle', type=Path, required=True)
    again.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.command == 'run':result = run(args.bundle, args.output.absolute(), args.smoke)
    else:
        root = args.bundle.resolve();verify(root)
        out = fresh_output(args.output, [root]);out.mkdir()
        env = os.environ.copy();env['PPROF_TMPDIR'] = str(out)
        result = summarize(root, out, env)
        seal(out, True)
    print(json.dumps({'status': 'PASSED', 'workersVerified': result['workersVerified']}))


if __name__ == '__main__':
    try:main()
    except (Exception, KeyboardInterrupt) as error:
        print(f'snapshot-profile: {error}', file=sys.stderr)
        sys.exit(1)
