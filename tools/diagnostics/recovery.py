#!/usr/bin/env python3
"""Public HTTP full-state recovery checks for newly created diagnostic runs."""
import argparse
import hashlib
import http.client
import json
import os
from pathlib import Path
import shutil
import signal
import socket
import subprocess
import time
from datetime import datetime, timezone
from common import child, require


def now():
    return datetime.now(timezone.utc).isoformat()


from common import save, digest, read, lines


def key(req):
    c = req['command']
    body = json.loads(req['body'])
    kind = 'place' if c['Kind'] == 'PLACE_ORDER' else 'cancel'
    return c['AccountID'], kind, body['clientOrderId' if kind == 'place' else 'cancelRequestId']


def evidence(case):
    prep, planned = {}, {}
    for r in lines(case / 'loadgen/plan.jsonl'):
        (prep if r['phase'] == 'PREPARE' else planned)[r['index']] = r
    accepted = {}
    samples = [(prep, x) for x in read(case / 'loadgen/preparation.json')['samples']]
    samples += [(planned, x) for x in lines(case / 'loadgen/samples.jsonl')]
    for source, s in samples:
        o = s['outcome']
        if o['class'] != 'BUSINESS':
            continue
        r = source[s['index']]
        k = key(r)
        prior = accepted.get(k)
        if prior and prior[1] != o['fingerprint']:
            raise ValueError('original client fingerprints disagree')
        accepted[k] = (r, o['fingerprint'], int(o['commandSeq']))
    if not accepted:
        raise ValueError('no acknowledged commands')
    seqs = {v[2] for v in accepted.values()}
    if seqs != set(range(1, max(seqs) + 1)):
        raise ValueError('acknowledged sequences are not contiguous')
    return accepted, max(seqs)


class Client:
    def __init__(self, ready):
        host, port = ready['listen'].split(':')
        if host != '127.0.0.1' or not 0 < int(port) < 65536:
            raise ValueError('non-loopback readiness')
        self.conn = http.client.HTTPConnection(host, int(port), timeout=10)
        self.run_id = ready['runId']

    def request(self, account, path, method='GET', body=None):
        headers = {'X-Run-ID': self.run_id, 'X-Account-ID': account}
        if body is not None:
            headers['Content-Type'] = 'application/json'
        self.conn.request(method, path, body, headers)
        r = self.conn.getresponse()
        raw = r.read(1024 * 1024 + 1)
        if r.status != 200 or len(raw) > 1024 * 1024:
            raise ValueError(f'HTTP {r.status}: {path}: {raw[:300]!r}')
        return raw, json.loads(raw)

    def close(self):
        self.conn.close()


def check_supply(balances, config):
    base = sum(int(b['BTC']['availableLots']) + int(b['BTC']['reservedLots']) for b in balances)
    quote = sum(int(b['USDT']['availableAtoms']) + int(b['USDT']['reservedAtoms']) for b in balances)
    if base != sum(int(a['baseLots']) for a in config['accounts']):
        raise ValueError('base supply mismatch')
    if quote != sum(int(a['quoteAtoms']) for a in config['accounts']):
        raise ValueError('quote supply mismatch')
    for b in balances:
        if any(int(x) < 0 for asset in ('BTC', 'USDT') for x in b[asset].values()):
            raise ValueError('negative balance')


def capture(client, case, label, accepted, seq, config):
    path = case / f'{label}.jsonl'
    counts = {'balances': 0, 'orders': 0, 'results': 0, 'trades': 0}
    balances = []
    with open(path, 'x') as f:
        def record(account, url, expected=None):
            raw, value = client.request(account, url)
            if value.get('runId') != client.run_id:
                raise ValueError('response identity mismatch')
            if 'asOfSeq' in value and int(value['asOfSeq']) != seq:
                raise ValueError('state changed while reading')
            if expected and hashlib.sha256(raw).hexdigest() != expected:
                raise ValueError(f'acknowledged result lost or changed: {url}')
            f.write(json.dumps({'account': account, 'path': url, 'response': raw.decode()}, separators=(',', ':')) + '\n')
            return value
        for account in config['accounts']:
            balances.append(record(account['accountId'], '/v1/balances'))
            counts['balances'] += 1
        check_supply(balances, config)
        book = record('alice', '/v1/book?limit=100')
        if max(len(book['bids']), len(book['asks'])) >= 100:
            raise ValueError('book may be truncated')
        url = '/v1/trades?limit=1000'
        visited = set()
        while True:
            require(url not in visited and len(visited)<100000, 'repeated or excessive trade pagination')
            visited.add(url)
            trades = record('alice', url)
            counts['trades'] += len(trades['trades'])
            c = trades['nextCursor']
            if c is None:
                break
            url = f"/v1/trades?limit=1000&throughSeq={seq}&afterSeq={c['afterSeq']}&afterIndex={c['afterIndex']}"
        for (account, kind, request_id), (_, expected, _) in sorted(accepted.items()):
            record(account, f'/v1/results/{kind}/{request_id}', expected)
            counts['results'] += 1
            if kind == 'place':
                record(account, f'/v1/orders/{request_id}')
                counts['orders'] += 1
        f.flush()
        os.fsync(f.fileno())
    return {'sha256': digest(path), 'counts': counts, 'sequence': seq, 'balances': balances}


def start(argv, case, name):
    with open(case / f'{name}.json', 'xb') as out, open(case / f'{name}.stderr', 'xb') as err:
        return subprocess.Popen(argv, stdout=out, stderr=err)


def stop(p):
    if p is None:
        return
    if p.poll() is None:
        p.terminate()
        try:
            p.wait(timeout=30)
        except subprocess.TimeoutExpired:
            p.kill()
            p.wait()
            raise RuntimeError('own child required forced termination')
    if p.returncode != 0:
        raise RuntimeError(f'child exited {p.returncode}')


def ready(p, path):
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        if p.poll() is not None:
            raise RuntimeError(f'server exited before ready: {p.returncode}')
        try:
            r = read(path)
            if r.get('event') == 'ready':
                c = Client(r)
                try:
                    if c.request('alice', '/health/ready')[1] != {'state': 'READY'}:
                        raise ValueError('not ready')
                finally:
                    c.close()
                return r
        except (FileNotFoundError, json.JSONDecodeError):
            pass
        time.sleep(0.01)
    raise TimeoutError('server readiness exceeded 120s')


def guard(root, p):
    free = shutil.disk_usage(root).free
    used = sum(f.stat().st_size for f in root.rglob('*') if f.is_file())
    rss = int(subprocess.check_output(['/bin/ps', '-o', 'rss=', '-p', str(p.pid)]).strip()) * 1024
    if free < 8 * 1024**3 or used > 8 * 1024**3 or rss > 2 * 1024**3:
        raise RuntimeError(f'resource budget exceeded: free={free} used={used} rss={rss}')
    return {'atUTC': now(), 'freeBytes': free, 'bundleBytes': used, 'serverRSSBytes': rss}


def run_case(root, scenario, config_path, workload_path, case_name=None, *, start_child=None, snapshot_interval="5s", guard_interval=5):
    start = start_child or globals()["start"]
    case = child(root, case_name or scenario)
    case.mkdir()
    for source in (config_path, workload_path):
        shutil.copy2(source, case / source.name)
    config = read(config_path)
    server = generator = None
    result = {'scenario': scenario, 'startedUTC': now(), 'status': 'FAILED', 'powerLossTested': False}
    try:
        exchange = str(root / 'bin/exchange')
        run_dir = str(case / 'run')
        init = start([exchange, 'init', '--config', str(config_path), '--data-dir', run_dir], case, 'init')
        try:
            init.wait(timeout=30)
        finally:
            stop(init)
        serve = [exchange, 'serve', '--data-dir', run_dir, '--listen', '127.0.0.1:0',
                 '--metrics-file', str(case / 'server-metrics.jsonl'), '--metrics-interval', '500ms']
        if scenario == 'snapshot':
            serve += ['--snapshot-interval', snapshot_interval, '--snapshot-log', str(case / 'snapshot-events.jsonl')]
        save(case / 'invocation.json', {'serve': serve, 'config': str(config_path), 'workload': str(workload_path), 'diagnosticsIntervalNanos': 500000000})
        server = start(serve, case, 'server-ready')
        first = ready(server, case / 'server-ready.json')
        result['runId'] = first['runId']
        generator = start([str(root / 'bin/loadgen'), '--config', str(config_path), '--workload', str(workload_path),
                           '--server-report', str(case / 'server-ready.json'), '--output', str(case / 'loadgen'), '--diagnostics-interval', '500ms'], case, 'generator')
        workload = read(workload_path)['workload']
        deadline = time.monotonic() + (workload['durationNanos'] + workload['warmupNanos']) / 1e9 + 180
        tick = 0
        with open(case / 'guard.jsonl', 'x') as f:
            while generator.poll() is None:
                if server.poll() is not None or time.monotonic() > deadline:
                    raise RuntimeError('server exited or workload deadline expired')
                observation = guard(root, server)
                f.write(json.dumps(observation) + '\n')
                f.flush()
                if tick % 6 == 0:
                    print(json.dumps({'stage': 'load', 'scenario': scenario, **observation}), flush=True)
                tick += 1
                time.sleep(guard_interval)
        result['generatorExit'] = generator.returncode
        generator = None
        accepted, seq = evidence(case)
        client = Client(first)
        try:
            before = capture(client, case, 'before-restart', accepted, seq, config)
        finally:
            client.close()
        result['before'] = before
        print(json.dumps({'stage': 'pre-restart-verified', 'scenario': scenario, 'commands': len(accepted)}), flush=True)
        stop(server)
        server = None
        wal_hash = digest(case / 'run/commands.wal')
        t0 = time.perf_counter_ns()
        restart = [exchange, 'serve', '--data-dir', run_dir, '--listen', '127.0.0.1:0']
        server = start(restart, case, 'restarted')
        second = ready(server, case / 'restarted.json')
        result['restartReadyNanos'] = time.perf_counter_ns() - t0
        result['recovery'] = second['recovery']
        if (second['runId'], second['configHash']) != (first['runId'], first['configHash']):
            raise ValueError('restart identity mismatch')
        recovery = second['recovery']
        if int(recovery['snapshotSeq']) + int(recovery['replayedCommands']) != seq or recovery['skippedSnapshots']:
            raise ValueError('unexpected recovery report')
        if bool(recovery['snapshotFile']) != (scenario == 'snapshot'):
            raise ValueError('unexpected snapshot selection')
        client = Client(second)
        try:
            after = capture(client, case, 'after-restart', accepted, seq, config)
            result['after'] = after
            if before != after:
                raise ValueError('recovered full HTTP state differs')
            # Read-only verification above must finish before any retry mutation.
            selected = {}
            entries = sorted(accepted.items(), key=lambda x: x[1][2])
            for item in entries[:3] + entries[len(entries)//2:len(entries)//2+3] + entries[-3:]:
                selected[item[0]] = item[1]
            for k, value in entries:
                if k[1] == 'cancel' or k[0] == 'reject':
                    selected[k] = value
                    if len(selected) >= 12:
                        break
            with open(case / 'retries.jsonl', 'x') as f:
                for k, (r, expected, _) in selected.items():
                    raw, _ = client.request(k[0], r['path'], 'POST', r['body'])
                    if hashlib.sha256(raw).hexdigest() != expected:
                        raise ValueError('retry changed original result')
                    f.write(json.dumps({'key': k, 'response': raw.decode()}) + '\n')
            result['retryCount'] = len(selected)
            result['afterRetries'] = capture(client, case, 'after-retries', accepted, seq, config)
            if result['afterRetries'] != before:
                raise ValueError('retry changed full state')
        finally:
            client.close()
        stop(server)
        server = None
        if digest(case / 'run/commands.wal') != wal_hash:
            raise ValueError('read/retry verification changed WAL')
        for r in (first, second):
            host, port = r['listen'].split(':')
            with socket.socket() as s:
                s.settimeout(1)
                if s.connect_ex((host, int(port))) == 0:
                    raise ValueError('owned server still listening')
        result['walSHA256'] = wal_hash
        result['status'] = 'PASSED'
    except BaseException as exc:
        result['error'] = repr(exc)
        raise
    finally:
        errors = []
        for p in (generator, server):
            try:
                stop(p)
            except Exception as exc:
                errors.append(repr(exc))
        if errors:
            result['cleanupErrors'] = errors
            result['status'] = 'FAILED'
        result['finishedUTC'] = now()
        save(case / 'verification.json', result)
        print(json.dumps({'stage': 'finished', **{k: result[k] for k in ('scenario', 'status', 'generatorExit', 'error') if k in result}}, ensure_ascii=False), flush=True)
    require(result['status']=='PASSED', 'child cleanup failed')
    return result
