"""Sequential P6 reference measurements with owned host observers and fixed gates."""
import json
import os
from pathlib import Path
import platform
import shutil
import signal
import subprocess
import time
from analysis import host_lifetime
from build import go
from common import REPO, child, digest, lines, read, require, save, seal
from recovery import now
from runner import source_hashes
from samplers import Samplers

BINARIES = ('exchange', 'loadgen', 'experiment', 'benchreport', 'storageprobe')


def freeze(output):
    require(not subprocess.check_output(['git', 'status', '--porcelain'], cwd=REPO, text=True),
            'commit source and documentation before freezing a producer build')
    revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=REPO, text=True).strip()
    require(not output.is_symlink(), 'existing output symlink')
    output = output.resolve()
    output.parent.mkdir(parents=True, exist_ok=True)
    output.mkdir(mode=0o700)
    try:
        (output/'bin').mkdir()
        args = ['build', '-o', output/'bin', './cmd/exchange', './cmd/loadgen', './cmd/experiment',
                './tools/benchreport', './tools/storageprobe']
        go(*args)
        require(not subprocess.check_output(['git', 'status', '--porcelain'], cwd=REPO, text=True), 'source changed while building')
        metadata = {name: subprocess.check_output([str(REPO/'scripts/go'), 'version', '-m', str(output/'bin'/name)], text=True)
                    for name in BINARIES}
        require(all('vcs.modified=false' in value and 'vcs.revision='+revision in value for value in metadata.values()),
                'clean build metadata required')
        save(output/'build.json', {'revision': revision, 'argv': list(map(str, [REPO/'scripts/go', *args])),
                                  'sha256': {name: digest(output/'bin'/name) for name in BINARIES}, 'goBuild': metadata})
    except BaseException as error:
        save(output/'failed.json', {'error': repr(error)})
        raise
    return output


def validate_build(root):
    record = read(root/'build.json')
    for name in BINARIES:
        path = child(root, 'bin/'+name)
        require(digest(path) == record['sha256'][name], 'frozen binary changed: '+name)
        actual = subprocess.check_output([str(REPO/'scripts/go'), 'version', '-m', str(path)], text=True)
        require(actual.splitlines()[1:] == record['goBuild'][name].splitlines()[1:]
                and 'vcs.modified=false' in actual and 'vcs.revision='+record['revision'] in actual,
                'clean frozen build metadata mismatch: '+name)
    return record


def order(preset, normal_rate=None, snapshot_rate=None):
    require(preset in ('smoke', 'reference'), 'invalid preset')
    if preset == 'smoke':
        require(normal_rate is None and snapshot_rate is None, 'rate overrides require reference preset')
        return [('normal-1', 'normal', 20), ('snapshot-1', 'snapshot', 20)]
    normal = 29000 if normal_rate is None else normal_rate
    snapshot = 3000 if snapshot_rate is None else snapshot_rate
    require(all(type(rate) is int and 1000 <= rate <= 80000 and rate % 1000 == 0 for rate in (normal, snapshot)),
            'reference rates must be 1000..80000 in steps of 1000 (at most 2000000 scheduled attempts)')
    return [('normal-1', 'normal', normal), ('snapshot-1', 'snapshot', snapshot),
            ('snapshot-2', 'snapshot', snapshot), ('normal-2', 'normal', normal),
            ('normal-3', 'normal', normal), ('snapshot-3', 'snapshot', snapshot)]


def quiet_stats(path, minimum):
    observed = host_lifetime(path)
    values = []
    for record in lines(path):
        try:
            row = list(map(float, record['line'].split()))
        except ValueError:
            continue
        if row:
            values.append(100-row[-1])
    maximum = max(values[1:], default=100)
    return {**observed, 'cpuBusyMax': maximum,
            'accepted': observed['records'] >= minimum and observed['cpuBusyMedian'] <= 15 and maximum <= 30}


def owned_processes(raw, parent):
    rows = []
    for line in raw.splitlines():
        fields = line.split(None, 3)
        if len(fields) == 4:
            rows.append({'pid': int(fields[0]), 'ppid': int(fields[1]),
                         'rssBytes': int(fields[2])*1024, 'name': Path(fields[3]).name})
    identifiers = {parent}
    while True:
        found = identifiers | {r['pid'] for r in rows if r['ppid'] in identifiers}
        if found == identifiers:
            break
        identifiers = found
    return [r for r in rows if r['pid'] in identifiers]


def within_budget(free, used, processes):
    return (free >= 8*2**30 and used <= 4*2**30
            and all(r['rssBytes'] <= 2*2**30 for r in processes)
            and sum(r['rssBytes'] for r in processes) <= 3*2**30)


def bundle_bytes(root):
    total = 0
    for path in root.rglob('*'):
        try:
            if path.is_file():
                total += path.stat().st_size
        except FileNotFoundError:
            # Snapshot publication can rename a temporary file during a scan.
            continue
    return total


def stop(target):
    if target is not None and target.poll() is None:
        target.terminate()
        try:
            target.wait(timeout=30)
        except subprocess.TimeoutExpired:
            target.kill()
            target.wait()
            raise RuntimeError('owned experiment required forced termination')


def power_and_thermal(root, label):
    power = subprocess.check_output(['/usr/bin/pmset', '-g', 'batt'], text=True)
    thermal = subprocess.check_output(['/usr/bin/pmset', '-g', 'therm'], text=True)
    save(root/(label+'-power.json'), {'utc': now(), 'power': power, 'thermal': thermal})
    require('AC Power' in power, 'AC power required')
    # Explicit CPU throttling indicators, when the OS reports them.
    for line in thermal.splitlines():
        if any(key in line for key in ('CPU_Speed_Limit', 'CPU_Scheduler_Limit')) and '=' in line:
            require(int(line.rsplit('=', 1)[1].strip().rstrip(';')) == 100, 'OS reports CPU throttling')


def run(output, build_dir, preset, normal_rate=None, snapshot_rate=None):
    selected_order = order(preset, normal_rate, snapshot_rate)
    require(platform.system() == 'Darwin' and platform.machine() == 'arm64', 'reference measurement requires macOS arm64/APFS')
    require(preset in ('smoke', 'reference'), 'invalid preset')
    require(not output.is_symlink(), 'existing output symlink')
    output, build_dir = output.resolve(), build_dir.resolve()
    require(not output.is_relative_to(build_dir), 'output must be outside frozen build')
    build = validate_build(build_dir)
    output.parent.mkdir(parents=True, exist_ok=True)
    output.mkdir(mode=0o700)
    target, results = None, []
    previous_handlers = {}
    def interrupted(*_):
        raise KeyboardInterrupt()
    for sig in (signal.SIGINT, signal.SIGTERM):
        previous_handlers[sig] = signal.signal(sig, interrupted)
    env = os.environ.copy()
    env.update(GOMAXPROCS=str(os.cpu_count()), GOGC='100', GOMEMLIMIT='off')
    env.pop('GODEBUG', None)
    env.pop('EXCHANGE_TRACE', None)
    try:
        (output/'bin').mkdir()
        for name in BINARIES:
            shutil.copy2(build_dir/'bin'/name, output/'bin'/name)
        save(output/'producer-build.json', build)
        protocol = {'version': 1, 'preset': preset, 'createdUTC': now(), 'producerRevision': build['revision'],
                    'binarySHA256': build['sha256'], 'sourceSHA256': source_hashes(), 'order': selected_order,
                    'runtimeEnvironment': {key: env[key] for key in ('GOMAXPROCS', 'GOGC', 'GOMEMLIMIT')},
                    'quietGate': {'medianBusyMax': 15, 'peakBusyMax': 30, 'minimumRecords': 2 if preset == 'smoke' else 10,
                                  'enforced': preset == 'reference'},
                    'cooldownSeconds': 0 if preset == 'smoke' else 30, 'trace': False, 'generatorDiagnosticsNanos': 0,
                    'metricsIntervalNanos': 500000000, 'snapshotIntervalNanos': 5000000000,
                    'minimumFreeBytes': 8*2**30, 'maximumBundleBytes': 4*2**30,
                    'maximumProcessRSSBytes': 2*2**30, 'maximumOwnedRSSBytes': 3*2**30,
                    'hostFullyIsolated': False, 'powerLossTested': False}
        save(output/'protocol.json', protocol)
        probe = subprocess.run([str(output/'bin/storageprobe'), '-dir', str(output)], capture_output=True, text=True, timeout=30)
        require(probe.returncode == 0, 'storage probe failed: '+probe.stderr)
        save(output/'storage-probe.json', json.loads(probe.stdout))
        for index, (name, scenario, rate) in enumerate(selected_order):
            power_and_thermal(output, name)
            for attempt in range(3):
                label = name+'-quiet-'+str(attempt+1)
                with Samplers(output, label):
                    time.sleep(3 if preset == 'smoke' else 11)
                quiet = quiet_stats(output/(label+'-host.jsonl'), protocol['quietGate']['minimumRecords'])
                save(output/(label+'.json'), quiet)
                if quiet['accepted']:
                    break
                print(json.dumps({'stage': 'quiet-rejected', 'case': name, 'attempt': attempt+1, 'observation': quiet}), flush=True)
                if preset == 'smoke':
                    # A six-second path check cannot qualify under P6 criteria.
                    # Keep accepted=false; never reuse this bypass for reference.
                    break
                time.sleep(5)
            require(quiet['accepted'] or not protocol['quietGate']['enforced'], 'host did not meet predeclared quiet gate')
            require(shutil.disk_usage(output).free >= 8*2**30, 'insufficient free disk space')
            workload = read(REPO/'config/benchmarks/workload-v1.json')
            workload['workload']['requestsPerSecond'] = rate
            if preset == 'smoke':
                workload['workload'].update(warmupNanos=0, durationNanos=6_000_000_000)
            work = output/(name+'-workload.json')
            save(work, workload)
            for binary in BINARIES:
                require(digest(output/'bin'/binary) == build['sha256'][binary], 'producer binary changed')
            argv = [str(output/'bin/experiment'), '--exchange', str(output/'bin/exchange'),
                    '--loadgen', str(output/'bin/loadgen'), '--config', str(REPO/'config/benchmarks/config-v1.json'),
                    '--workload', str(work), '--output', str(output/name), '--scenario', scenario,
                    '--metrics-interval', '500ms', '--snapshot-interval', '5s', '--diagnostics-interval', '0']
            save(output/(name+'-invocation.json'), {'argv': argv, 'runtimeEnvironment': protocol['runtimeEnvironment']})
            print(json.dumps({'stage': 'starting', 'case': name, 'rate': rate, 'utc': now()}), flush=True)
            with Samplers(output, name), (output/(name+'.stdout')).open('x') as stdout, (output/(name+'.stderr')).open('x') as stderr, (output/(name+'-guard.jsonl')).open('x') as guard:
                target = subprocess.Popen(argv, stdout=stdout, stderr=stderr, env=env)
                deadline = time.monotonic()+240
                while target.poll() is None:
                    raw = subprocess.check_output(['/bin/ps', '-axo', 'pid=,ppid=,rss=,comm='], text=True)
                    processes = owned_processes(raw, target.pid)
                    free = shutil.disk_usage(output).free
                    used = bundle_bytes(output)
                    guard.write(json.dumps({'utc': now(), 'freeBytes': free, 'bundleBytes': used, 'owned': processes})+'\n')
                    guard.flush()
                    require(within_budget(free, used, processes), 'resource budget exceeded')
                    require(time.monotonic() <= deadline, 'experiment deadline exceeded')
                    time.sleep(.5 if preset == 'smoke' else 2)
                code = target.returncode
                target = None
            require(code in (0, 1) and (output/name/'loadgen/summary.json').is_file(), 'experiment failed before summary')
            summary = read(output/name/'loadgen/summary.json')
            result = {'case': name, 'exit': code, 'measurementValid': summary['measurementValid']}
            results.append(result)
            print(json.dumps({'stage': 'finished', **result}), flush=True)
            if index+1 < len(selected_order):
                time.sleep(protocol['cooldownSeconds'])
        judged = subprocess.run([str(output/'bin/benchreport'), '-output', str(output/'qualification.json'),
                                 *[str(output/name) for name, _, _ in selected_order]], capture_output=True, text=True, env=env, timeout=300)
        require(judged.returncode == 0, 'benchreport failed: '+judged.stderr)
        save(output/'suite.json', {'status': 'COLLECTION_COMPLETED', 'results': results,
                                  'meaning': 'inspect qualification.json; collection completion is not performance qualification'})
        seal(output, True)
        return output
    except BaseException as error:
        cleanup_errors = []
        try:
            stop(target)
        except Exception as cleanup:
            cleanup_errors.append(repr(cleanup))
        save(output/'failed.json', {'error': repr(error), 'cleanupErrors': cleanup_errors, 'completedCases': results})
        seal(output, False)
        raise
    finally:
        for sig, handler in previous_handlers.items():
            signal.signal(sig, handler)
