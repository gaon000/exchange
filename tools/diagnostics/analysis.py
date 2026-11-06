"""Parameter-free bundle analysis; imported data is read-only and never executed."""
import bisect
import collections
import math
import statistics
import subprocess
import tempfile
from pathlib import Path
from archive import verify_archive
from build import build_checker
from common import REPO, child, digest, lines, read, require, verify
from metrics import PATHS, at, bracket, check_summary, histogram, interval, quantities, stamp
from trace_reader import parse


def distribution(values):
    values = sorted(values)
    return {'count': len(values),
            'p50Ms': values[math.ceil(len(values)*.5)-1]/1e6 if values else None,
            'p99Ms': values[math.ceil(len(values)*.99)-1]/1e6 if values else None,
            'maxMs': values[-1]/1e6 if values else None,
            'over10ms': sum(v > 10_000_000 for v in values),
            'over50ms': sum(v > 50_000_000 for v in values)}


def verify_archives(root):
    result = {}
    for case in sorted(root.iterdir()):
        if case.is_dir() and not case.name.startswith('smoke') and (case/'archive-manifest.json').is_file():
            result[case.name] = verify_archive(case)
    return result


def discover(root):
    if (root/'protocol.json').is_file():
        protocol = read(root/'protocol.json')
        if isinstance(protocol.get('cases'), list):
            return [child(root, x['name']) for x in protocol['cases']]
    # Older frozen bundles had different suite JSON schemas. Only discover
    # their immediate case directories; never infer an executable from them.
    return [p for p in sorted(root.iterdir()) if p.is_dir() and not p.name.startswith('smoke')
            and ((p/'loadgen/run.json').is_file() or ((p/'run.json').is_file() and (p/'samples.jsonl').is_file()))]


def timer(case):
    meta, rows = read(case/'run.json'), lines(case/'samples.jsonl')
    count = meta['durationNanos']//10_000_000
    require(0 < count <= 60000 and len(rows) == meta['count'] == count and not meta.get('aborted'),
            'incomplete timer data')
    require(meta['workers'] in (0, 2048) and meta['requestsPerSecond'] == 100,
            'unsupported timer settings')
    enabled = meta['traceEnabled']
    require((case/'timer.trace').exists() == enabled, 'trace file/config mismatch')
    expected = set()
    for i, r in enumerate(rows):
        require(r['index'] == i and r['scheduledNanos'] == i*10_000_000
                and r['waitEndNanos'] >= r['waitStartNanos']
                and r['waitEndNanos'] >= r['scheduledNanos'], 'timer sample boundary mismatch')
        late, worker, failure = r['waitEndNanos']-r['scheduledNanos'], r['workerNanos'], r.get('failure', '')
        if late > 50_000_000:
            require(failure == 'scheduler' and worker == -1, 'scheduler rejection mismatch')
        elif meta['workers'] == 0:
            require(not failure and worker == -1, 'unexpected worker observation')
        elif failure == 'capacity':
            require(worker == -1, 'capacity observation mismatch')
        else:
            require(worker >= r['waitEndNanos'], 'worker preceded dispatch observation')
            require(failure == ('worker' if worker-r['scheduledNanos'] > 50_000_000 else ''),
                    'worker rejection mismatch')
        if late > 10_000_000:
            expected.add(('scheduler-delay', i))
        if worker >= 0 and worker-r['scheduledNanos'] > 10_000_000:
            expected.add(('worker-delay', i))
    trace = None
    if enabled:
        require(meta['traceBytes'] == (case/'timer.trace').stat().st_size, 'trace size mismatch')
        trace = parse(case/'timer.trace')
        seen = set()
        for marker in trace['markers']:
            r = rows[marker['index']]
            field = 'waitEndNanos' if marker['category'] == 'scheduler-delay' else 'workerNanos'
            require(marker['offset'] == r[field] and marker['delay'] == r[field]-r['scheduledNanos'],
                    'trace marker disagrees with timer sample')
            seen.add((marker['category'], marker['index']))
        require(seen == expected and len(seen) == len(trace['markers']), 'missing/duplicate timer markers')
    else:
        require(meta['traceBytes'] == 0, 'disabled trace recorded bytes')
    windows = []
    for begin in range(0, count, 500):
        group = rows[begin:begin+500]
        windows.append({'startSeconds': begin/100,
                        'scheduler': distribution([r['waitEndNanos']-r['scheduledNanos'] for r in group]),
                        'failures': dict(collections.Counter(r['failure'] for r in group if r.get('failure')))})
    return {'kind': 'timer', 'meta': meta,
            'scheduler': distribution([r['waitEndNanos']-r['scheduledNanos'] for r in rows]),
            'handoff': distribution([r['workerNanos']-r['waitEndNanos'] for r in rows if r['workerNanos'] >= 0]),
            'workerScheduled': distribution([r['workerNanos']-r['scheduledNanos'] for r in rows if r['workerNanos'] >= 0]),
            'failures': dict(collections.Counter(r['failure'] for r in rows if r.get('failure'))),
            'failureRows': [r for r in rows if r.get('failure')], 'windows': windows, 'trace': trace}


def process_io(case):
    calls = lines(case/'server.trace.sync.jsonl')
    observations = lines(case/'server-ready-procio.jsonl')
    valid = [r for r in observations if r['available']]
    require(calls and 2 <= len(valid) < 4000, 'incomplete OS I/O data')
    require(len({r['pid'] for r in valid}) == 1, 'mixed process I/O PIDs')
    require(all(r.get('errno') == 3 for r in observations if not r['available']), 'OS counters unavailable')
    for a, b in zip(valid, valid[1:]):
        require(a['finishedNanos'] <= b['startedNanos'] and a['readBytes'] <= b['readBytes']
                and a['writeBytes'] <= b['writeBytes'], 'non-monotonic process I/O')
    require(all(c['ClockAvailable'] and c['End'] >= c['Start'] and c['CPUEnd'] >= c['CPUStart']
                and c['Attempts'] >= 1 and c['Error'] == '<nil>' for c in calls), 'failed/unavailable C observation')
    times = [x['finishedNanos'] for x in valid]
    wal = [c for c in calls if c['File'] == 'commands.wal']
    require(wal, 'no WAL C observations')
    selected = []
    for c in sorted(wal, key=lambda x: x['End']-x['Start'], reverse=True)[:10]:
        i, j = bisect.bisect_right(times, c['Start'])-1, bisect.bisect_left(times, c['End'])
        # An early startup call may precede sampler startup; retain its absence.
        bracket = None
        if 0 <= i < j < len(valid):
            a, b = valid[i], valid[j]
            bracket = {'elapsedMs': (b['finishedNanos']-a['finishedNanos'])/1e6,
                       'readBytesDelta': b['readBytes']-a['readBytes'],
                       'writeBytesDelta': b['writeBytes']-a['writeBytes']}
        selected.append({**c, 'wallMs': (c['End']-c['Start'])/1e6,
                         'threadCpuMs': (c['CPUEnd']-c['CPUStart'])/1e6, 'osBracket': bracket})
    return {'kernelEventsAvailable': False, 'osRecords': len(valid), 'cCalls': len(calls),
            'walCalls': len(wal), 'walWall': distribution([c['End']-c['Start'] for c in wal]),
            'walThreadCpu': distribution([c['CPUEnd']-c['CPUStart'] for c in wal]),
            'selectedWalCalls': selected}


def host_lifetime(path):
    raw = lines(path)
    columns, samples = None, []
    for record in raw:
        tokens = record['line'].split()
        if tokens[-3:] == ['us', 'sy', 'id']:
            prefix = tokens[:-3]
            require(len(prefix) % 3 == 0 and prefix == ['KB/t', 'tps', 'MB/s']*(len(prefix)//3),
                    'unsupported iostat disk header')
            require(columns is None or columns == tokens, 'iostat columns changed during collection')
            columns = tokens
            continue
        try:
            values = list(map(float, tokens))
        except ValueError:
            continue
        if not values:
            continue
        require(columns is not None and len(values) == len(columns), 'iostat row/header mismatch')
        require(all(math.isfinite(v) and v >= 0 for v in values)
                and all(v <= 100 for v in values[-3:]), 'invalid iostat counters')
        disks = (len(columns)-3)//3
        samples.append({'cpuBusyPercent': 100-values[-1],
                        'diskMBps': sum(values[2:-3:3]) if disks else None})
    samples = samples[1:]  # First numeric row is the since-boot average.
    require(samples, f'no host interval samples; first rows: {[r["line"] for r in raw[:8]]}')
    disks = (len(columns)-3)//3
    return {'records': len(samples), 'cpuBusyMedian': statistics.median(x['cpuBusyPercent'] for x in samples),
            'displayedDiskCount': disks, 'diskAvailable': bool(disks),
            'diskMBpsMedian': statistics.median(x['diskMBps'] for x in samples) if disks else None,
            'diskScope': 'sum of displayed devices; logical devices may overlap',
            'scope': 'whole child lifetime; coarse UTC rows, not request attribution'}


def trade(case, checker, scratch):
    generated = scratch/(case.name+'-raw.json')
    subprocess.run([str(checker), str(case/'loadgen'), str(generated)], check=True, capture_output=True)
    raw = read(generated)
    if (case/'analysis.json').exists():
        require(raw == read(case/'analysis.json'), 'retained Go aggregate disagrees with current recomputation')
    verification = read(case/'verification.json')
    require(verification['status'] == 'PASSED' and verification['before'] == verification['after']
            == verification['afterRetries'], 'full recovery state mismatch')
    require(digest(case/'run/commands.wal') == verification['walSHA256'], 'WAL changed after retries')
    plan = {r['index']: r for r in lines(case/'loadgen/plan.jsonl') if r['phase'] != 'PREPARE'}
    samples = lines(case/'loadgen/samples.jsonl')
    dispatch = {r['index']: r for r in lines(case/'loadgen/dispatch.jsonl')}
    require(len(samples) == len(plan) == len(dispatch), 'missing dispatch/sample rows')
    groups = {phase: [s for s in samples if plan[s['index']]['phase'] == phase]
              for phase in ('WARMUP', 'MEASURE')}
    values = {}
    for phase, field in [('WARMUP', 'warmup'), ('MEASURE', 'measurement')]:
        values[field] = quantities(groups[phase], plan, dispatch)
        check_summary(values[field], raw['summary'][field])
    # Exact Go window summaries, plus independently checked totals above.
    for width, windows in raw['windows'].items():
        for window in windows:
            group = [s for s in groups['MEASURE']
                     if window['startNanos'] <= plan[s['index']]['scheduledNanos'] < window['endNanos']]
            check_summary(quantities(group, plan, dispatch), window)
    metrics = lines(case/'server-metrics.jsonl')
    require(len(metrics) >= 2, 'missing server metrics')
    stages = {'.'.join(p): histogram(at(metrics[0], p), at(metrics[-1], p)) for p in PATHS}
    result = {'kind': 'trade', 'summary': raw['summary'], 'windows': raw['windows'],
              'observations': values, 'serverLifetimeStages': stages, 'recovery': verification,
              'rawRecomputed': True, 'performanceQualified': False,
              'qualificationReason': 'diagnostic suite; use fixed benchreport criteria for baseline'}
    generator = lines(case/'loadgen/generator-metrics.jsonl')
    run = read(case/'loadgen/run.json')
    epoch = stamp(run['scheduleEpoch'])
    times = [stamp(x['finishedUTC']) for x in metrics]
    gtimes = [stamp(x['finishedUTC']) for x in generator]

    def observed(start, end, records, clocks, server=False, around=False):
        # UTC alignment is approximate. Missing brackets and clock regressions
        # are unavailable observations, never zero CPU/GC or exact attribution.
        if clocks != sorted(clocks):
            return {'available': False, 'reason': 'UTC clock regression'}
        i = bisect.bisect_right(clocks, start)-1
        j = bisect.bisect_right(clocks, end)-1
        if around:
            j = i+1
        if not 0 <= i < j < len(records):
            return {'available': False, 'reason': 'missing metric boundary'}
        value = bracket(records, clocks, start, server) if around else interval(records, clocks, start, end, server)
        return {'available': True, **value}

    result['resourceWindows'] = [
        {'startNanos': w['startNanos'], 'endNanos': w['endNanos'],
         'server': observed(epoch+w['startNanos']/1e9, epoch+w['endNanos']/1e9, metrics, times, True),
         'generator': observed(epoch+w['startNanos']/1e9, epoch+w['endNanos']/1e9, generator, gtimes)}
        for w in raw['windows'].get('5s', [])]
    result['missedRequests'] = [
        {'index': s['index'], 'failure': s['localFailure'],
         'dispatch': dispatch[s['index']],
         'serverBracket': observed(epoch+s['finishedNanos']/1e9, 0, metrics, times, True, True),
         'generatorBracket': observed(epoch+s['finishedNanos']/1e9, 0, generator, gtimes, around=True)}
        for s in groups['MEASURE'] if s.get('localFailure')]
    result['wallMinusMonotonicEndMs'] = ((stamp(run['finishedUTC'])-epoch)*1e9-run['elapsedNanos'])/1e6
    trace_paths = [case/(name+'.trace') for name in ['generator', 'server']]
    if any(p.exists() for p in trace_paths):
        require(all(p.exists() for p in trace_paths), 'partial trade trace pair')
        result['traces'] = {p.stem: parse(p) for p in trace_paths}
    if (case/'server.trace.sync.jsonl').exists():
        result['processIO'] = process_io(case)
    host = case.parent/(case.name+'-host.jsonl')
    if host.exists():
        result['hostLifetime'] = host_lifetime(host)
    return result


def analyze_bundle(root):
    verified = verify(root)
    archives = verify_archives(root)
    found = discover(root)
    require(found and len(found) <= 100, 'no cases or excessive case count')
    result = {'version': 1, 'verification': verified, 'archives': archives,
              'cases': {}, 'kernelEventsAvailable': False, 'powerLossTested': False,
              'analysisSourceSHA256': {str(p.relative_to(REPO)): digest(p)
                                       for p in sorted((REPO/'tools/diagnostics').glob('*.py'))}}
    with tempfile.TemporaryDirectory(prefix='exchange-diagnostic-analysis-') as temporary:
        scratch = Path(temporary)
        checker = scratch/'experimentcheck'
        if any((p/'loadgen').is_dir() for p in found):
            build_checker(checker)
        for case in found:
            result['cases'][case.name] = trade(case, checker, scratch) if (case/'loadgen').is_dir() else timer(case)
    return result
