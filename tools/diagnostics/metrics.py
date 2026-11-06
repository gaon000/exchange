"""Independent request summaries and approximate resource interval observations."""
import bisect
from collections import Counter
import datetime as dt
import math
from common import require

PATHS = [('runtime', 'commandQueueWait'), ('runtime', 'batchWait'), ('runtime', 'append'), ('runtime', 'apply'), ('runtime', 'wal', 'sync'), ('http', 'handler'), ('http', 'encode'), ('http', 'write'), ('runtime', 'snapshotCapture'), ('runtime', 'snapshotPublish')]

def stamp(s):
    return dt.datetime.fromisoformat(s.replace('Z', '+00:00')).timestamp()

def distribution(values):
    v = sorted(values)
    return {'count': len(v), 'p99Ms': v[math.ceil(len(v) * 0.99) - 1] / 1000000.0 if v else None, 'maxMs': v[-1] / 1000000.0 if v else None, 'over20ms': sum((x > 20000000.0 for x in v))}

def at(d, path):
    for k in path:
        d = d[k]
    return d

def histogram(a, b):
    require(not a['sumOverflow'] and (not b['sumOverflow']), 'invalid diagnostic observation')
    aa = {int(x['upperNanos']): int(x['count']) for x in a['buckets']}
    bb = {int(x['upperNanos']): int(x['count']) for x in b['buckets']}
    buckets = {k: bb.get(k, 0) - aa.get(k, 0) for k in aa.keys() | bb.keys()}
    n, total = (int(b['count']) - int(a['count']), int(b['sumNanos']) - int(a['sumNanos']))
    require(n == sum(buckets.values()) and total >= 0 and all((v >= 0 for v in buckets.values())), 'invalid diagnostic observation')
    rank, seen, q = (math.ceil(n * 0.99), 0, None)
    for k in sorted(buckets):
        seen += buckets[k]
        if n and seen >= rank:
            q = k / 1000000.0
            break
    return {'count': n, 'sumNanos': total, 'meanMs': total / n / 1000000.0 if n else None, 'p99BucketUpperMs': q, 'over20ms': sum((v for k, v in buckets.items() if k > 20000000.0))}

def process(a, b):
    pa, pb = (a['process'], b['process'])
    da = {float(x['upperSeconds']): int(x['count']) for x in pa['gcPauses']}
    db = {float(x['upperSeconds']): int(x['count']) for x in pb['gcPauses']}
    positive = [k for k in db if db[k] > da.get(k, 0)]
    require(int(pb['goGcCycles']) >= int(pa['goGcCycles']), 'invalid diagnostic observation')
    return {'osAvailable': bool(pa['osAvailable'] and pb['osAvailable']), 'cpuMs': (int(pb['userCpuNanos']) + int(pb['systemCpuNanos']) - int(pa['userCpuNanos']) - int(pa['systemCpuNanos'])) / 1000000.0 if pa['osAvailable'] and pb['osAvailable'] else None, 'gcCpuMs': (pb['goGcCpuSeconds'] - pa['goGcCpuSeconds']) * 1000, 'gcCycles': int(pb['goGcCycles']) - int(pa['goGcCycles']), 'gcPauseLargestUpperMs': max(positive) * 1000 if positive else None, 'peakRssMiB': int(pb['peakRssBytes']) / 2 ** 20 if pb['osAvailable'] else None, 'liveHeapMiB': int(pb['goLiveHeapBytes']) / 2 ** 20}

def interval(metrics, times, start, end, server=False):
    i, j = (bisect.bisect_right(times, start) - 1, bisect.bisect_right(times, end) - 1)
    require(0 <= i < j < len(metrics), (i, j, len(metrics), start, end))
    a, b = (metrics[i], metrics[j])
    result = {'observedStartUTC': a['finishedUTC'], 'observedEndUTC': b['finishedUTC'], 'startBoundaryLagMs': (start - times[i]) * 1000, 'endBoundaryLagMs': (end - times[j]) * 1000, **process(a, b)}
    if server:
        result['stages'] = {'.'.join(p): histogram(at(a, p), at(b, p)) for p in PATHS}
        result['observedQueueDepthMax'] = max((x['runtime']['commandQueueDepth'] for x in metrics[i:j + 1]))
    else:
        periodic = [x for x in metrics[i + 1:j + 1] if x['stage'] == 'periodic']
        result['skippedIntervals'] = sum((x['skippedIntervals'] for x in periodic))
        result['maxCollectionDelayMs'] = max([(x['startedNanos'] - x['scheduledNanos']) / 1000000.0 for x in periodic], default=0)
        result['maxCollectionDurationMs'] = max([(x['finishedNanos'] - x['startedNanos']) / 1000000.0 for x in metrics[i:j + 1]], default=0)
    return result

def bracket(metrics, times, when, server=False):
    j = bisect.bisect_right(times, when)
    require(0 < j < len(times), 'invalid diagnostic observation')
    return interval(metrics, times, times[j - 1], times[j], server)

def quantities(samples, plan, dispatch):
    started = [s for s in samples if not s.get('localFailure')]
    return {'scheduled': len(samples), 'started': len(started), 'missed': sum((bool(s.get('localFailure')) for s in samples)), 'request': distribution([s['finishedNanos'] - s['startedNanos'] for s in started]), 'scheduledRequest': distribution([s['finishedNanos'] - plan[s['index']]['scheduledNanos'] for s in started]), 'schedulerObservationDelay': distribution([dispatch[s['index']]['dispatchedNanos'] - plan[s['index']]['scheduledNanos'] for s in samples if dispatch[s['index']]['dispatchedNanos'] >= 0]), 'dispatchToWorker': distribution([dispatch[s['index']]['workerNanos'] - dispatch[s['index']]['dispatchedNanos'] for s in samples if dispatch[s['index']]['workerNanos'] >= 0]), 'transportWrite': distribution([s['writtenNanos'] - s['startedNanos'] for s in started if s['writtenNanos'] >= 0]), 'missedBoundaries': dict(Counter((('scheduler' if dispatch[s['index']]['workerNanos'] == -1 else 'worker') if s['localFailure'] == 'GENERATOR_LATE' else s['localFailure'] for s in samples if s.get('localFailure'))))}

def check_summary(quantities, summary):
    require(quantities['scheduled'] == summary['scheduled'], 'invalid diagnostic observation')
    require(quantities['started'] == summary['started'], 'invalid diagnostic observation')
    require(quantities['missed'] == (sum(summary['localFailures'].values()) if isinstance(summary['localFailures'], dict) else summary['localFailures']), 'invalid diagnostic observation')
    for ours, theirs in [('request', 'requestLatency'), ('scheduledRequest', 'scheduledAttemptLatency'), ('transportWrite', 'transportWriteDelay')]:
        if theirs not in summary:
            continue
        q, s = (quantities[ours], summary[theirs])
        require(q['count'] == s['count'], 'invalid diagnostic observation')
        require(q['p99Ms'] == (s['p99Nanos'] / 1000000.0 if s['count'] else None), 'invalid diagnostic observation')
        require(q['maxMs'] == (s['maxNanos'] / 1000000.0 if s['count'] else None), 'invalid diagnostic observation')
