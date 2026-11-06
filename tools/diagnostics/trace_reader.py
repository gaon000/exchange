from common import require
import bisect, collections, datetime as dt, hashlib, json, re, statistics, subprocess, threading
from pathlib import Path
from common import REPO
GO = str(REPO / 'scripts/go')

def stamp(s):
    return dt.datetime.fromisoformat(s.replace('Z', '+00:00')).timestamp()

def digest(p):
    return hashlib.sha256(p.read_bytes()).hexdigest()

def parse(path):
    p = subprocess.Popen([GO, 'tool', 'trace', '-d=parsed', str(path)], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    history = collections.defaultdict(lambda: collections.deque(maxlen=12))
    runnable = {}
    lastRun = {}
    markers = []
    clocks = []
    begins = {}
    syncs = []
    counts = collections.Counter()
    longruns = []
    watchdog = threading.Timer(120, p.kill)
    watchdog.start()
    try:
        for line in p.stdout:
            m = re.search(' G=(-?\\d+) (\\w+) Time=(\\d+)(.*)', line)
            if not m:
                continue
            gid, kind, t, rest = (int(m[1]), m[2], int(m[3]), m[4])
            counts[kind] += 1
            if kind == 'StateTransition':
                q = re.search('GoID=(\\d+) (\\w+)->(\\w+) Reason="([^"]*)"', rest)
                if q:
                    g, old, new, reason = (int(q[1]), q[2], q[3], q[4])
                    history[g].append({'time': t, 'old': old, 'new': new, 'reason': reason})
                    if new == 'Runnable':
                        runnable[g] = t
                    if old == 'Runnable' and new == 'Running' and (g in runnable):
                        d = t - runnable.pop(g)
                        lastRun[g] = {'started': t, 'runnableMs': d / 1000000.0}
                        if d > 10000000.0:
                            longruns.append({'gid': g, **lastRun[g]})
            if kind == 'Log':
                q = re.search('Category="([^"]*)" Message="([^"]*)"', rest)
                if q and q[1] == 'clock':
                    clocks.append({'time': t, 'utc': q[2]})
                elif q and q[1] in ['scheduler-delay', 'worker-delay']:
                    v = {k: int(x) for k, x in re.findall('(index|offset|delay)=(\\d+)', q[2])}
                    markers.append({'gid': gid, 'time': t, 'category': q[1], **v, 'lastRunnable': lastRun.get(gid), 'transitions': list(history[gid])})
            if 'Type="wal.fileSync"' in rest:
                if kind == 'RegionBegin':
                    begins[gid] = t
                if kind == 'RegionEnd':
                    require(gid in begins, 'invalid diagnostic observation')
                    start = begins.pop(gid)
                    syncs.append({'gid': gid, 'start': start, 'end': t, 'durationMs': (t - start) / 1000000.0})
        err = p.stderr.read()
        require(p.wait() == 0, err)
    finally:
        watchdog.cancel()
        if p.poll() is None:
            p.kill()
            p.wait()
        p.stdout.close()
        p.stderr.close()
    require(len(clocks) == 2 and (not begins), (path, len(clocks), len(begins)))

    def utc(t):
        return stamp(clocks[0]['utc']) + (t - clocks[0]['time']) / 1000000000.0
    for m in markers:
        m['utcSeconds'] = utc(m['time'])
    for s in syncs:
        s['utcSeconds'] = utc(s['end'])
    return {'sha256': digest(path), 'bytes': path.stat().st_size, 'eventCounts': dict(counts), 'clocks': clocks, 'clockResidualMs': (stamp(clocks[1]['utc']) - stamp(clocks[0]['utc']) - (clocks[1]['time'] - clocks[0]['time']) / 1000000000.0) * 1000, 'markers': markers, 'longRunnable': sorted(longruns, key=lambda x: x['runnableMs'], reverse=True)[:10], 'syncCount': len(syncs), 'syncOver20ms': sum((x['durationMs'] > 20 for x in syncs)), 'topSyncs': sorted(syncs, key=lambda x: x['durationMs'], reverse=True)[:10]}
