"""Sequential diagnostic suites, always in new output directories."""
import contextlib
import json
import os
import platform
import shutil
import signal
import subprocess
import time
from pathlib import Path
from archive import archive
from build import build
from common import REPO, digest, read, require, save, seal
from recovery import guard, now, run_case
from samplers import Samplers


def cases(suite, repeats):
    result = []
    for repeat in range(repeats):
        if suite == 'timer':
            for label, workers, tracing in [('only-off', 0, False), ('only-on', 0, True),
                                             ('handoff-on', 2048, True), ('handoff-off', 2048, False)]:
                result.append({'name': f'{label}-{repeat+1}', 'kind': 'timer',
                               'workers': workers, 'trace': tracing})
        else:
            order = ['on'] if suite == 'io' else (['off', 'on'] if repeat % 2 == 0 else ['on', 'off'])
            for label in order:
                result.append({'name': f'{label}-{repeat+1}', 'kind': 'trade',
                               'scenario': 'snapshot' if label == 'on' else 'normal',
                               'trace': suite in ('trace', 'io'), 'io': suite == 'io'})
    return result


def source_hashes():
    roots = ['cmd', 'internal', 'tools', 'scripts', 'config']
    paths = [REPO/'go.mod', REPO/'.go-version']
    for root in roots:
        paths += [p for p in (REPO/root).rglob('*') if p.is_file() and '__pycache__' not in p.parts
                  and p.suffix in ('.go', '.py', '.sh', '.json', '.tmpl', '.c')]
    paths.append(REPO/'scripts/go')
    return {str(p.relative_to(REPO)): digest(p) for p in sorted(set(paths))}


def environment():
    return {'capturedUTC': now(), 'platform': platform.platform(), 'machine': platform.machine(),
            'python': platform.python_version(), 'cpuCount': os.cpu_count(),
            'go': json.loads(subprocess.check_output([str(REPO/'scripts/go'), 'env', '-json',
                              'GOVERSION', 'GOOS', 'GOARCH', 'CGO_ENABLED'], cwd=REPO)),
            'runtimeEnvironment': {k: os.environ[k] for k in ('GOMAXPROCS', 'GOGC', 'GOMEMLIMIT', 'GODEBUG') if k in os.environ}}


def timer_case(root, item, seconds):
    case = root/item['name']
    args = [str(root/'bin/timerprobe'), '--duration', f'{seconds}s', '--workers', str(item['workers']),
            '--trace='+str(item['trace']).lower(), '--output', str(case)]
    with (root/(item['name']+'.stdout')).open('x') as out, (root/(item['name']+'.stderr')).open('x') as err:
        target = subprocess.Popen(args, stdout=out, stderr=err)
        started = time.monotonic()
        try:
            while target.poll() is None:
                if time.monotonic()-started > seconds+60:
                    raise TimeoutError('timer suite deadline')
                try:
                    observation = guard(root, target)
                except subprocess.CalledProcessError:
                    if target.poll() is not None:
                        break
                    raise
                with (root/(item['name']+'-guard.jsonl')).open('a') as output:
                    output.write(json.dumps(observation)+'\n')
                time.sleep(.1 if seconds <= 6 else 5)
            require(target.returncode == 0, f'timer exited {target.returncode}')
        finally:
            if target.poll() is None:
                target.terminate()
                try:
                    target.wait(timeout=15)
                except subprocess.TimeoutExpired:
                    target.kill()
                    target.wait()
                    raise RuntimeError('owned timer required forced termination')
    meta = read(case/'run.json')
    require(meta['count'] == seconds*100 and not meta['aborted'], 'incomplete timer case')
    require(meta['traceEnabled'] == item['trace'], 'timer trace option mismatch')
    require((case/'timer.trace').exists() == item['trace'], 'timer trace file mismatch')
    return {'case': item['name'], 'kind': 'timer', 'argv': args, 'trace': item['trace']}


def run_suite(options):
    require(not options.prune_archived or options.archive, '--prune-archived requires --archive')
    require(options.suite != 'timer' or not options.archive, 'timer suite has no state archive')
    if options.suite != 'timer':
        require(platform.system() == 'Darwin' and platform.machine() == 'arm64',
                'trade diagnostics require macOS arm64/APFS/cgo')
    root = options.output.absolute()
    root.parent.mkdir(parents=True, exist_ok=True)
    root.mkdir(mode=0o700)  # Even an empty existing directory is never reused.
    root = root.resolve()
    results = []
    previous_handler = {}
    original_trace = os.environ.get("EXCHANGE_TRACE")
    for sig in (signal.SIGINT, signal.SIGTERM):
        previous_handler[sig] = signal.signal(sig, lambda *_: (_ for _ in ()).throw(KeyboardInterrupt()))
    try:
        require(shutil.disk_usage(root).free >= 8*2**30, 'minimum free space is 8GiB')
        smoke = options.preset == 'smoke'
        repeats = options.repeats if options.repeats is not None else (3 if options.suite == 'soak' and not smoke else 1)
        cooldown = options.cooldown if options.cooldown is not None else (0 if smoke else 30)
        order = cases(options.suite, repeats)
        build(root, options.suite)
        if options.suite != 'timer':
            shutil.copyfile(REPO/'config/benchmarks/config-v1.json', root/'config.json')
            if smoke:
                workload = read(REPO/'config/benchmarks/soak-10m-v1.json')
                workload['workload'].update(requestsPerSecond=20, warmupNanos=0, durationNanos=6_000_000_000)
                save(root/'workload.json', workload)
            else:
                shutil.copyfile(REPO/'config/benchmarks/soak-10m-v1.json', root/'workload.json')
        save(root/'environment.json', environment())
        revision = subprocess.check_output(['git', 'rev-parse', 'HEAD'], cwd=REPO, text=True).strip()
        protocol = {'version': 1, 'suite': options.suite, 'preset': options.preset,
                    'createdUTC': now(), 'baseRevision': revision,
                    'workingTreeModified': bool(subprocess.check_output(['git','status','--porcelain'],cwd=REPO,text=True)), 'cases': order,
                    'cooldownSeconds': cooldown, 'timerSeconds': 1 if smoke else 600,
                    'diagnosticsIntervalNanos': 500000000, 'snapshotIntervalSeconds': 5,
                    'archive': options.archive, 'pruneArchived': options.prune_archived,
                    'kernelEventsAvailable': False, 'powerLossTested': False,
                    'sourceSHA256': source_hashes(), 'binarySHA256':
                    {str(p.relative_to(root)): digest(p) for p in (root/'bin').iterdir()}}
        save(root/'protocol.json', protocol)
        for index, item in enumerate(order):
            for path, expected in protocol['binarySHA256'].items():
                require(digest(root/path) == expected, f'binary changed: {path}')
            print(json.dumps({'stage': 'starting', 'case': item['name'], 'utc': now()}), flush=True)
            if item['kind'] == 'timer':
                result = timer_case(root, item, protocol['timerSeconds'])
            else:
                manager = Samplers(root, item['name'], item['io']) if item['trace'] else contextlib.nullcontext()
                with manager as samplers:
                    if item['trace']:
                        os.environ['EXCHANGE_TRACE'] = '1'
                    else:
                        os.environ.pop('EXCHANGE_TRACE', None)
                    verified = run_case(root, item['scenario'], root/'config.json', root/'workload.json',
                                        item['name'], start_child=samplers.start if samplers else None,
                                        guard_interval=.2 if smoke else 5)
                subprocess.run([str(root/'bin/experimentcheck'), str(root/item['name']/'loadgen'),
                                str(root/item['name']/'analysis.json')], check=True)
                result = {'case': item['name'], 'kind': 'trade', 'generatorExit': verified['generatorExit'],
                          'measurementValid': read(root/item['name']/'loadgen/summary.json')['measurementValid'],
                          'fullRecovery': verified['status'], 'trace': item['trace'], 'io': item['io']}
                if options.archive:
                    archived = archive(root/item['name'], options.prune_archived)
                    result['archiveSHA256'] = archived['archiveSHA256']
            results.append(result)
            print(json.dumps({'stage': 'finished', **result}), flush=True)
            if index+1 < len(order):
                time.sleep(cooldown)
        save(root/'suite.json', {'version': 1, 'status': 'PASSED', 'results': results,
                                'meaning': 'execution and evidence checks, not performance qualification'})
        seal(root, completed=True)
        return root
    except BaseException as error:
        save(root/'failed.json', {'error': repr(error), 'completedCases': results, 'finishedUTC': now()})
        seal(root, completed=False)
        raise
    finally:
        if original_trace is None:
            os.environ.pop("EXCHANGE_TRACE",None)
        else:
            os.environ["EXCHANGE_TRACE"]=original_trace
        for sig, handler in previous_handler.items():
            signal.signal(sig, handler)
