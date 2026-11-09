"""Read-only P6 snapshot/cohort alignment, including responses after window end."""
import argparse
import bisect
import json
from pathlib import Path
import subprocess
import tempfile
from build import go
from common import REPO, digest, lines, read, require, save, seal
from metrics import at, distribution, histogram, interval, stamp


def enclosing(times, collection_starts, start, end):
    """Use counters collected wholly before/after the response cohort."""
    require(len(times) == len(collection_starts), 'metrics collection boundary mismatch')
    i = bisect.bisect_right(times, start) - 1
    # A collection ending after the response may have read a counter before
    # that response. Its start must also follow the response to cover it.
    j = bisect.bisect_left(collection_starts, end)
    require(0 <= i < j < len(times), 'metrics do not enclose the response cohort')
    return i, j


def host_rows(path):
    """Retain coarse host UTC rows, excluding iostat's since-boot average."""
    result = []
    for r in lines(path):
        try:
            values = list(map(float, r['line'].split()))
        except ValueError:
            continue
        if len(values) >= 3:
            result.append({'utc': r['utc'], 'busyPercent': 100-values[-1],
                           'displayedDiskMBps': sum(values[2:-3:3]) if len(values) > 3 else None})
    return result[1:]


def align(case, decision):
    """Align verified request cohorts to coarse cumulative server observations."""
    run = read(case/'loadgen/run.json')
    epoch = stamp(run['scheduleEpoch'])
    plan = {r['index']: r for r in lines(case/'loadgen/plan.jsonl') if r['phase'] == 'MEASURE'}
    samples = [r for r in lines(case/'loadgen/samples.jsonl') if r['index'] in plan]
    metrics = lines(case/'server-metrics.jsonl')
    times = [stamp(r['finishedUTC']) for r in metrics]
    collection_starts = [stamp(r['startedUTC']) for r in metrics]
    require(all(a < b for a, b in zip(times, times[1:]))
            and all(a < b for a, b in zip(collection_starts, collection_starts[1:]))
            and all(a <= b for a, b in zip(collection_starts, times)),
            'non-monotonic server UTC observations')
    snapshots = lines(case/'snapshot-events.jsonl')
    host_path = case.parent/(case.name+'-host.jsonl')
    host = host_rows(host_path) if host_path.is_file() else []
    windows = []
    for expected in decision['windows']:
        lo, hi = expected['startNanos'], expected['endNanos']
        cohort = [r for r in samples if lo <= plan[r['index']]['scheduledNanos'] < hi]
        started = [r for r in cohort if not r.get('localFailure')]
        request = distribution([r['finishedNanos']-r['startedNanos'] for r in started])
        require(len(cohort) == expected['scheduled'] and len(started) == expected['started']
                and request['p99Ms'] == expected['requestLatency']['p99Nanos']/1e6,
                'request cohort disagrees with benchreport')
        response_end = max([hi]+[r['finishedNanos'] for r in started])
        start, end = epoch+lo/1e9, epoch+response_end/1e9
        i, j = enclosing(times, collection_starts, start, end)
        a, b = metrics[i], metrics[j]
        observation = interval(metrics, times, times[i], times[j], True)
        observation['snapshotStages'] = {}
        zero = {'count': '0', 'sumNanos': '0', 'sumOverflow': False, 'buckets': [], 'maxNanos': '0'}
        for key, value in b['runtime']['snapshotStages'].items():
            observation['snapshotStages'][key] = histogram(a['runtime']['snapshotStages'].get(key, zero), value)
        observation['newCumulativeMaximaMs'] = {}
        for path in (('runtime', 'wal', 'sync'), ('runtime', 'snapshotCapture'), ('runtime', 'commandQueueWait')):
            old, new = at(a, path), at(b, path)
            observation['newCumulativeMaximaMs']['.'.join(path)] = int(new['maxNanos'])/1e6 if int(new['maxNanos']) > int(old['maxNanos']) else None
        windows.append({'startNanos': lo, 'endNanos': hi, 'responseEndNanos': response_end,
                        'request': request, 'leftExpansionMs': (start-times[i])*1000,
                        'rightExpansionMs': (times[j]-end)*1000, 'server': observation,
                        'hostRows': [r for r in host if times[i] <= stamp(r['utc']) <= times[j]],
                        'overlappingSnapshotCalls': [r for r in snapshots
                            if stamp(r['startedUTC']) <= end and stamp(r['finishedUTC']) >= start]})
    slowest = sorted([r for r in samples if not r.get('localFailure')],
                     key=lambda r: r['finishedNanos']-r['startedNanos'], reverse=True)[:10]
    slow_intervals = []
    seen = set()
    for r in slowest:
        i, j = enclosing(times, collection_starts, epoch+r['startedNanos']/1e9, epoch+r['finishedNanos']/1e9)
        if (i, j) not in seen:
            seen.add((i, j))
            slow_intervals.append(interval(metrics, times, times[i], times[j], True))
    names = ('loadgen/run.json', 'loadgen/plan.jsonl', 'loadgen/samples.jsonl',
             'snapshot-events.jsonl', 'server-metrics.jsonl')
    hashes = {name: digest(case/name) for name in names}
    if host_path.is_file():
        hashes['../'+host_path.name] = digest(host_path)
    return {'case': str(case), 'runId': decision['runId'], 'criterionPass': decision['pass'],
            'reasons': decision['reasons'], 'inputSHA256': hashes, 'sourceSHA256': digest(Path(__file__)),
            'utcMinusMonotonicAtEndMs': ((stamp(run['finishedUTC'])-epoch)*1e9-run['elapsedNanos'])/1e6,
            'windows': windows, 'slowestRequests': slowest, 'slowestEnclosingIntervals': slow_intervals}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('cases', type=Path, nargs='+')
    options = parser.parse_args()
    cases = [p.resolve() for p in options.cases]
    output = options.output.resolve()
    require(0 < len(cases) <= 10 and len(set(cases)) == len(cases), 'require 1..10 distinct cases')
    require(not options.output.is_symlink() and all(not output.is_relative_to(p) for p in cases), 'output overlaps input')
    output.parent.mkdir(parents=True, exist_ok=True)
    output.mkdir(mode=0o700)
    try:
        with tempfile.TemporaryDirectory() as temp:
            judge = Path(temp)/'benchreport'
            go('build', '-o', judge, './tools/benchreport')
            result = subprocess.run([str(judge), '-output', str(output/'qualification.json'), *map(str, cases)],
                                    cwd=REPO, capture_output=True, text=True, timeout=300)
            require(result.returncode == 0, 'benchreport failed: '+result.stderr)
        qualification = read(output/'qualification.json')
        require(all(c['summaryRecomputed'] and c['sealed'] and c['scenario'] == 'snapshot'
                    and not any(r.startswith('EVIDENCE:') for r in c['reasons']) for c in qualification['cases']),
                'snapshot raw verification failed')
        records = [align(Path(c['bundle']), c) for c in qualification['cases']]
        save(output/'report.json', {'version': 1, 'diagnosticOnly': True, 'cases': records,
             'notes': ['UTC alignment is approximate; endpoint drift is not an error bound.',
                       'Server intervals enclose the full response cohort and may contain unrelated work.',
                       'The ending collection starts after the last response; collection finish alone is insufficient.',
                       'Histogram deltas measure completed calls; stage durations are not request-attributed.',
                       'Host UTC rows are coarse whole-host observations, not per-request attribution.',
                       'Generator GC is available only for its full lifetime, not per-window.',
                       'A new cumulative maximum belongs to the interval; an unchanged maximum is not its maximum.',
                       'Overlap does not establish a unique or kernel I/O cause.']})
        rows = ['# Snapshot window alignment', '', '| Case | Window, s | Request p99, ms | Response tail, ms | WAL sync >20ms | Capture count | GC pause upper, ms |',
                '| --- | --- | ---: | ---: | ---: | ---: | ---: |']
        for c in records:
            for w in c['windows']:
                s = w['server']
                rows.append(f"| {Path(c['case']).name} | {w['startNanos']/1e9:g}–{w['endNanos']/1e9:g} | {w['request']['p99Ms']:.6f} | {(w['responseEndNanos']-w['endNanos'])/1e6:.6f} | {s['stages']['runtime.wal.sync']['over20ms']} | {s['stages']['runtime.snapshotCapture']['count']} | {s['gcPauseLargestUpperMs']} |")
        (output/'report.md').write_text('\n'.join(rows)+'\n', encoding='utf-8')
        seal(output, True)
    except BaseException as error:
        save(output/'failed.json', {'error': repr(error)})
        seal(output, False)
        raise
    print(json.dumps({'output': str(output), 'cases': len(cases)}))


if __name__ == '__main__':
    main()
