"""Read-only adapters for sealed concurrent HTTP and owned-process crash evidence."""
import collections
import json
from pathlib import Path

from common import decode, digest, lines, read, require
from transport_report import (attempt, command_key, finish, integer, key, key_object, observation)


def number(value, label):
    require(isinstance(value, str) and value.isascii() and value.isdecimal()
            and str(int(value)) == value and int(value) <= 2**63-1, 'invalid '+label)
    return int(value)


def command(value, run, wire=False):
    if wire:
        require(set(value) == {'RunID', 'AccountID', 'Kind', 'Place', 'Cancel'} and value['RunID'] == run, 'WAL command identity')
        kind = value['Kind']
        unused = value['Cancel' if kind == 'PLACE_ORDER' else 'Place']
        require(isinstance(unused, dict) and all(v in ('', '0') for v in unused.values()), 'nonzero unused WAL payload')
        value = {'accountId': value['AccountID'], 'kind': kind,
                 'place' if kind == 'PLACE_ORDER' else 'cancel': value['Place' if kind == 'PLACE_ORDER' else 'Cancel']}
    kind = value.get('kind')
    require(kind in ('PLACE_ORDER', 'CANCEL_ORDER'), 'command kind')
    payload = 'place' if kind == 'PLACE_ORDER' else 'cancel'
    require(set(value) == {'accountId', 'kind', payload}, 'command union')
    k = command_key(value, run)
    p = value[payload]
    if payload == 'place':
        require(set(p) == {'clientOrderId', 'symbol', 'side', 'type', 'timeInForce', 'priceTicks', 'quantityLots'}
                and p['symbol'] == 'BTC-USDT' and p['side'] in ('BUY', 'SELL')
                and p['type'] == 'LIMIT' and p['timeInForce'] == 'GTC', 'place payload')
        require(number(p['priceTicks'], 'price') > 0 and number(p['quantityLots'], 'quantity') > 0, 'nonpositive order')
    else:
        require(set(p) == {'cancelRequestId', 'clientOrderId'} and isinstance(p['clientOrderId'], str) and p['clientOrderId'], 'cancel payload')
    return k, value


def journal(rows, run, expected, prefix=None):
    require(isinstance(rows, list) and 0 < len(rows) <= 256, 'journal size')
    actual = {}
    for seq, row in enumerate(rows, 1):
        require(set(row) == {'Seq', 'Command'} and number(row['Seq'], 'WAL seq') == seq, 'WAL sequence')
        k, c = command(row['Command'], run, True)
        require(k not in actual and expected.get(k) == c, 'unexpected/duplicate WAL key or payload')
        actual[k] = (seq, c)
    if prefix is not None:
        require(rows[:len(prefix)] == prefix, 'WAL prefix changed')
    return actual


def checkpoint(cp, run, stored, config):
    require(set(cp) == {'accounts', 'orders', 'book', 'trades', 'results'}, 'checkpoint shape')
    first, commands = {}, {}
    for row in cp['results']:
        k, c = command(row['command'], run)
        result = row['response']
        observation(200, json.dumps(result, separators=(',', ':')), k, 'checkpoint')
        require(k not in first and k in stored and c == stored[k][1]
                and number(result['commandSeq'], 'result seq') == stored[k][0], 'checkpoint result/WAL mismatch')
        first[k], commands[k] = result, c
    require(set(first) == set(stored), 'checkpoint missing WAL results')
    accounts = cp['accounts']
    require(len(accounts) == len(config['accounts']) and len({a['accountId'] for a in accounts}) == len(accounts)
            and {a['accountId'] for a in accounts} == {a['accountId'] for a in config['accounts']}, 'checkpoint accounts')
    base = sum(number(a['availableBaseLots'], 'base')+number(a['reservedBaseLots'], 'base reserved') for a in accounts)
    quote = sum(number(a['availableQuoteAtoms'], 'quote')+number(a['reservedQuoteAtoms'], 'quote reserved') for a in accounts)
    require(base == sum(number(a['baseLots'], 'initial base') for a in config['accounts'])
            and quote == sum(number(a['quoteAtoms'], 'initial quote') for a in config['accounts']), 'asset supply mismatch')
    require(len({t['tradeId'] for t in cp['trades']}) == len(cp['trades']), 'duplicate trade ID')
    return first


def result(raw, k, first):
    obs, value = observation(200, raw, k, 'complete response')
    require(value == first.get(k), 'first response/checkpoint mismatch')
    return obs, value


def add_query(queries, k, status, raw, source, first, **metadata):
    obs, value = observation(status, raw, k, source, observedUTC=None, **metadata)
    if status == 200:
        require(value == first.get(k), 'query differs from first response')
    queries[k].append(obs)


def trace_queries(traces, run, first, queries, source, min_step=None, stage='AFTER_RETRY', before_key=None):
    for line, row in enumerate(traces, 1):
        if row.get('method', 'GET') != 'GET' or not row['path'].startswith('/v1/results/'):
            continue
        if min_step is not None and row['step'] < min_step:
            continue
        parts = row['path'].split('/')
        require(len(parts) == 5 and 'error' not in row, 'result query trace')
        k = key(run, row['account'], parts[3], parts[4])
        query_stage = 'BEFORE_RETRY' if k == before_key and row['status'] == 404 and row['step'] == min_step else stage
        add_query(queries, k, row['status'], row['response'], source, first, stage=query_stage,
                  step=row.get('step'), line=line, orderBasis='scenario step and trace line' if min_step is not None else 'case phase and trace line')


def finish_concurrent(entry, run, attempts, business, queries, retries, execution, provenance):
    report = finish(entry, run, attempts, business, queries,
                    {'scope': 'controlled concurrent scenario, not a load measurement', 'measurementValid': None, 'unknownLogicalCommands': None}, provenance)
    for row in report['unknownCommands']:
        k = key(row['runId'], row['accountId'], row['kind'], row['requestId'])
        before = [q for q in row['queries'] if q.get('stage') == 'BEFORE_RETRY']
        row['firstFollowupResolution'] = before[0]['resolution'] if before else None
        row['retryResolution'] = 'RESULT_OBSERVED' if k in retries else 'UNRESOLVED'
    report['retryObservations'] = [row for rows in retries.values() for row in rows]
    report['execution'] = execution
    return report


def order_rows(rows, run, snapshot_seq, crash_seq=None):
    out = []
    for row in rows:
        k = command_key(row['Command'], wire=True)
        require(k[0] == run, 'mixed WAL runs')
        seq = number(row['Seq'], 'WAL seq')
        segment = 'SNAPSHOT_PREFIX' if seq <= snapshot_seq else 'RECOVERY_TAIL'
        if crash_seq is not None and seq > crash_seq:
            segment = 'AFTER_RETRY'
        out.append({**key_object(k), 'commandSeq': row['Seq'], 'segment': segment})
    return out


def concurrent(entry, inputs):
    root = Path(entry['bundle'])
    def file(name): return inputs.file(root, name)
    scenario, summary, config = (read(file(n)) for n in ('scenario.json', 'summary.json', 'config.json'))
    require(scenario['version'] in (2,3) and 1 <= len(scenario['actions']) <= 256 and summary['status'] == read(root/'completed.json')['status'] == 'PASSED'
            and summary['scenarioSHA256'] == digest(file('scenario.json')) and summary['powerLossTested'] is False and summary['processKillTested'] is False, 'concurrent completion/schema')
    if scenario['version'] == 3:
        require(scenario.get('profile') in ('cancel-race','duplicate-storm','id-conflict')
                and scenario['profile'] == summary.get('profile'), 'v3 profile')
    final = read(file('final-actual.json'))
    require(final == read(file('final-expected.json')) and final['results'], 'final checkpoint mismatch')
    run = final['results'][0]['response']['runId']
    planned, previous, all_attempts, business = {}, [], [], collections.defaultdict(set)
    queries, retries, groups = collections.defaultdict(list), collections.defaultdict(list), []
    traces, events = lines(file('http.jsonl')), lines(file('events.jsonl'))
    loss_count = 0
    conflicts = []
    for step, action in enumerate(scenario['actions']):
        if action['kind'] == 'command':
            k, c = command(action['command'], run)
            require(k not in planned or planned[k] == c, 'conflicting plan input')
            planned[k] = c
            continue
        if action['kind'] in ('snapshot', 'restart'):
            continue
        require(action['kind'] == 'concurrent', 'unsupported mixed concurrent plan')
        g = action['concurrent']
        clients = len(g['initial'])
        require(4 <= clients <= 12 and len(g['retries']) == clients
                and (scenario['version'] == 3 or clients == 4), 'concurrent clients')
        rows = read(file(f'concurrent-{step:03d}-observations.json'))
        expected = {}
        before_count = len(planned)
        before = dict(planned)
        for phase, name in (('INITIAL', 'initial'), ('RETRY', 'retries')):
            require({a['client'] for a in g[name]} == set(range(clients)) and all(type(a['client']) is int for a in g[name]), 'client IDs')
            for a in g[name]:
                require(a.get('fault', '') in ('', 'drop-headers', 'truncate-body', 'held-original')
                        and (phase != 'RETRY' or not a.get('fault')), 'phase fault')
                k, c = command(a['command'], run)
                require(type(a.get('expectConflict',False)) is bool, 'conflict flag')
                if a.get('expectConflict'):
                    require(scenario['version'] == 3 and k in before and before[k] != c and not a.get('fault')
                            and (phase != 'INITIAL' or a['client'] != g['snapshotClient']), 'conflict needs a completed canonical key')
                    expected[(phase,a['client'])] = a
                    continue
                require(k not in planned or planned[k] == c, 'conflicting concurrent key')
                planned[k] = c
                expected[(phase, a['client'])] = a
        records = read(file(f'concurrent-{step:03d}-wal.json'))
        require(records == read(file(f'concurrent-{step:03d}-live-order.json')), 'live order/WAL mismatch')
        stored = journal(records, run, planned, previous)
        require(set(stored) == set(planned), 'planned WAL key missing')
        cp = read(file(f'concurrent-{step:03d}-snapshot-actual.json'))
        require(cp == read(file(f'concurrent-{step:03d}-snapshot-expected.json')), 'concurrent snapshot checkpoint mismatch')
        snap_events = [e for e in events if e['step'] == step and e['phase'] == 'concurrent-snapshot']
        require(len(snap_events) == 1 and snap_events[0]['clientStillInFlight'] is True and snap_events[0]['witnessClient'] == g['snapshotClient'], 'snapshot witness')
        snap = number(snap_events[0]['snapshotSeq'], 'snapshot seq')
        require(len(previous) < snap < len(records), 'snapshot needs fresh prefix and tail')
        checkpoint(cp, run, {k:v for k,v in stored.items() if v[0] <= snap}, config)
        if g.get('recorded'):
            require(g['recorded']['order'] == [command(r['Command'],run,True)[1] for r in records[before_count:]]
                    and number(g['recorded']['snapshotAfter'],'recorded snapshot') == snap-before_count, 'recorded concurrent schedule')
        recovery = read(file(f'restart-{step:03d}.json'))
        require(number(recovery['SnapshotSeq'], 'recovery snapshot') == snap
                and number(recovery['ReplayedCommands'], 'recovery tail') == len(records)-snap and recovery['Skipped'] == [], 'snapshot/tail recovery mismatch')
        first = {command_key(r['command'], run):r['response'] for r in final['results']}
        require(len(rows) == len(expected), 'missing phase observations')
        seen, upstream, unknown = set(), [], 0
        group_conflicts = 0
        for index, row in enumerate(rows):
            identity = (row['phase'], row['client'])
            require(identity in expected and identity not in seen, 'observation client/phase duplicate')
            seen.add(identity); a = expected[identity]
            require(row['command'] == a['command'] and row.get('fault', '') == a.get('fault', '')
                    and row.get('expectConflict',False) == a.get('expectConflict',False), 'phase observation input')
            k, _ = command(row['command'], run)
            if a.get('expectConflict'):
                outcome = row['outcome']
                error = decode(row['upstreamResponse'])
                require(row['upstreamStatus'] == outcome['httpStatus'] == 409 and not row.get('upstreamError')
                        and error.get('error',{}).get('code') == outcome.get('code') == 'IDEMPOTENCY_CONFLICT'
                        and error['error'].get('outcome') == outcome['certainty'] == 'NOT_ADMITTED'
                        and outcome['class'] == 'SERVER_FAILURE', 'conflict response changed')
                if row.get('requestLatencyNanos') is not None: integer(row['requestLatencyNanos'],'conflict latency')
                conflicts.append({**key_object(k),'step':step,'client':row['client'],'phase':row['phase'],'httpStatus':409,'certainty':'NOT_ADMITTED','requestLatencyNanos':row.get('requestLatencyNanos')})
                group_conflicts += 1
                continue
            require(row['upstreamStatus'] == 200, 'incomplete upstream response')
            obs, value = result(row['upstreamResponse'], k, first)
            require(number(value['commandSeq'], 'upstream seq') == stored[k][0], 'upstream/WAL sequence mismatch')
            latency = row.get('requestLatencyNanos')
            if latency is not None: integer(latency, 'latency')
            outcome = row['outcome']
            if a.get('fault'):
                require(outcome['certainty'] == 'UNKNOWN' and outcome['class'] in ('TRANSPORT_FAILURE', 'TRANSPORT_TIMEOUT')
                        and outcome['httpStatus'] == (200 if a['fault'] == 'truncate-body' else 0), 'lost client reclassified')
                all_attempts.append(attempt(k, 'CONCURRENT_'+row['phase'], index, outcome, latency, client=row['client'], step=step, fault=a['fault'], source=f'concurrent-{step:03d}-observations.json'))
                loss_count += 1; unknown += 1
            else:
                require(outcome['class'] == 'BUSINESS' and outcome['certainty'] == 'COMPLETED'
                        and outcome['httpStatus'] == 200 and outcome['fingerprint'] == obs['responseSHA256'], 'complete client response mismatch')
                if row['phase'] == 'INITIAL': business[k].add(obs['responseSHA256'])
                else: retries[k].append({**key_object(k), 'step':step, 'client':row['client'], 'phase':'RETRY', 'commandSeq':value['commandSeq'], 'responseSHA256':obs['responseSHA256']})
            upstream.append({**key_object(k), 'phase':row['phase'], 'client':row['client'], 'commandSeq':value['commandSeq'], 'clientCertainty':outcome['certainty']})
        held = [a for a in g['initial'] if a.get('fault') == 'held-original']
        require(len(held) == 1, 'missing held-original witness')
        held_key = command(held[0]['command'],run)[0]
        trace_queries(traces, run, first, queries, 'http.jsonl', step, before_key=held_key)
        require(any(q['httpStatus'] == 404 and q.get('stage') == 'BEFORE_RETRY' and q['step'] == step for q in queries[held_key]), 'missing pre-retry UNKNOWN witness')
        groups.append({'step':step, 'boundary':'normal-restart', 'clients':clients, 'phaseAttempts':len(rows), 'originalUnknown':unknown,
                       'conflicts':group_conflicts, 'sameIDDuplicates':len(rows)-(len(records)-before_count)-group_conflicts,
                       'snapshotSeq':str(snap), 'tailCommands':str(len(records)-snap), 'walOrder':order_rows(records,run,snap), 'upstreamResults':upstream})
        previous = records
    require(groups and len(groups) == summary['concurrentGroups'] and loss_count == summary['responseLosses'], 'concurrent summary mismatch')
    if scenario['version'] == 3:
        require(summary['conflicts'] == len(conflicts) and summary['retries'] == sum(g['sameIDDuplicates'] for g in groups), 'v3 conflict/duplicate summary')
    stored = journal(previous, run, planned)
    require(number(summary['appliedSeq'], 'summary seq') == len(stored), 'final sequence mismatch')
    checkpoint(final, run, stored, config)
    # Traces can be selected by several waves; do not duplicate the same query.
    for k in queries:
        queries[k] = list({(q['line'], q['source']):q for q in queries[k]}.values())
    return finish_concurrent(entry,run,all_attempts,business,queries,retries,
                             {'verificationScope':'sealed evidence consistency; no matching/recovery rerun', 'processKillClaim':False,
                              'profile':scenario.get('profile'), 'conflictObservations':conflicts, 'groups':groups},
                             {'bundle':inputs.bundle(root)[1], 'scenarioSHA256':summary['scenarioSHA256'], 'fullRecoveryClaim':summary['status']})


def concurrent_crash(entry, inputs):
    root, case = Path(entry['bundle']), entry['case']
    def file(name): return inputs.file(root, case+'/'+name)
    scenario = read(inputs.file(root, 'scenario.json'))
    summary = read(inputs.file(root, 'summary.json'))
    config = read(inputs.file(root, 'config.json'))
    completed = read(root/'completed.json')
    require(scenario['version'] == 1 and len(scenario['cases']) == 3 and summary['status'] == completed['status'] == 'PASSED'
            and summary['processKillTested'] is completed['processKillTested'] is True
            and summary['powerLossTested'] is completed['powerLossTested'] is False, 'crash completion')
    selected = [c for c in scenario['cases'] if c['boundary'] == case]
    require(len(selected) == 1, 'missing/duplicate crash case')
    c = selected[0]
    require(c == read(file('case.json')) and len(c['setup']) == 2 and len(c['attempts']) == 4, 'crash case input')
    actual, expected = read(file('final-actual.json')), read(file('final-expected.json'))
    recovered = read(file('recovered-actual.json'))
    require(actual == expected and recovered == read(file('recovered-expected.json')) and actual['results'], 'crash checkpoint mismatch')
    run = actual['results'][0]['response']['runId']
    planned = {}
    for cmd in c['setup']+c['attempts']+([c['tail']] if c.get('tail') else []):
        k, norm = command(cmd,run)
        require(k not in planned or planned[k] == norm, 'crash key conflict')
        planned[k] = norm
    records, final_records = read(file('crash-wal.json')), read(file('final-wal.json'))
    stored = journal(records,run,planned)
    final = journal(final_records,run,planned,records)
    require(set(final) == set(planned), 'retry missing planned WAL key')
    first = checkpoint(recovered,run,stored,config)
    final_first = checkpoint(actual,run,final,config)
    require(all(final_first[k] == v for k,v in first.items()), 'first response changed after retry')
    require([command(r['Command'],run,True)[1] for r in records[:2]] == c['setup'], 'setup WAL prefix')
    wal_case = case == 'wal-after-sync'
    require(len(records) == (3 if wal_case else 6) and len(final_records) == (5 if wal_case else 6)
            and bool(c.get('tail')) != wal_case, 'crash bounded scenario counts')
    recovery = read(file('recovery.json'))
    snap = 5 if case == 'snapshot-after-publish' else 2
    require(number(recovery['SnapshotSeq'],'snapshot seq') == snap and recovery['SnapshotFile']
            and number(recovery['ReplayedCommands'],'tail count') == len(records)-snap and recovery['Skipped'] == [], 'crash snapshot/tail')
    boundary, confirmed = read(file('kill-boundary.json')), read(file('kill-confirmed.json'))
    require(boundary['boundary'] == case and boundary['signal'] == confirmed['signal'] == 'SIGKILL'
            and confirmed['waitStatusVerified'] is True and boundary['initialCallsStillInFlight'] == 4
            and type(boundary['pid']) is int and boundary['pid'] > 0 and boundary['powerLossTested'] is False, 'kill confirmation')
    message = boundary['message']
    require(message['phase'] == 'boundary', 'kill marker phase')
    if wal_case:
        require(message['records'] == records[2:3], 'sync marker/WAL mismatch')
    else:
        require(number(message['snapshotSeq'],'capture seq') == 5
                and command(records[-1]['Command'],run,True)[1] == c['tail'], 'snapshot tail input')
    messages = lines(file('child.stdout.jsonl'))
    require([m['phase'] for m in messages] == (['ready','wave-entered','boundary'] if wal_case else ['ready','wave-entered','wave-complete','boundary'])
            and messages[0]['run'] == run and messages[-1] == message, 'child boundary trace')
    ack = read(file('acknowledged.json'))
    expected_ack = {command(v,run)[0] for v in c['setup']+([c['tail']] if c.get('tail') else [])}
    seen_ack = set()
    for row in ack:
        k,_ = command(row['command'],run)
        require(row['status'] == 200 and k not in seen_ack and k in expected_ack, 'acknowledged identity')
        result(row['response'],k,first); seen_ack.add(k)
    require(seen_ack == expected_ack, 'missing acknowledged result')
    if c.get('recorded'):
        end = len(records) if wal_case else len(records)-1
        require(c['recorded']['beforeKill'] == [command(r['Command'],run,True)[1] for r in records[2:end]], 'recorded pre-kill order')
        if 'afterRetry' in c['recorded']:
            require(c['recorded']['afterRetry'] == [command(r['Command'],run,True)[1] for r in final_records[len(records):]], 'recorded retry order')
    observations = read(file('initial-observations.json'))
    require(len(observations) == 4 and {r['client'] for r in observations} == {0,1,2,3}, 'crash initial clients')
    attempts = []
    for row in observations:
        require(row['command'] == c['attempts'][row['client']] and row['outcome'] == 'UNKNOWN'
                and row['status'] == 0 and row.get('error') and not row.get('response'), 'crash client reclassified')
        k,_ = command(row['command'],run)
        # Raw error text is neither reproduced nor guessed into stage/type.
        outcome = {'class':'TRANSPORT_FAILURE', 'certainty':'UNKNOWN'}
        attempts.append(attempt(k,'PROCESS_CRASH',row['client'],outcome,row.get('elapsedNs'),client=row['client'],fault=case,source=case+'/initial-observations.json'))
    queries,retries = collections.defaultdict(list),collections.defaultdict(list)
    followup = read(file('followup-results.json'))
    wave_keys = {command(v,run)[0] for v in c['attempts']}; seen = set(); unresolved = 0
    for line,row in enumerate(followup,1):
        k,_ = command(row['command'],run)
        require(k in wave_keys and k not in seen and row['originalOutcome'] == 'UNKNOWN', 'followup identity/duplicate')
        require(row['status'] == (200 if k in first else 404), 'pre-retry result contradicts persisted prefix')
        if row['status'] == 404: unresolved += 1
        add_query(queries,k,row['status'],row['response'],case+'/followup-results.json',first,stage='BEFORE_RETRY',line=line,orderBasis='before same-ID retry')
        seen.add(k)
    require(seen == wave_keys, 'missing pre-retry lookup')
    responses = read(file('retry-responses.json'))
    require(len(responses) == 4 and {r['client'] for r in responses} == {0,1,2,3}, 'retry clients')
    for row in responses:
        require(row['command'] == c['attempts'][row['client']] and row['originalOutcome'] == 'UNKNOWN'
                and row['status'] == 200 and 'error' not in row, 'retry observation mismatch')
        k,_ = command(row['command'],run); obs,value = result(row['response'],k,final_first)
        retries[k].append({**key_object(k),'phase':'AFTER_RECOVERY_RETRY','client':row['client'],'commandSeq':value['commandSeq'],'responseSHA256':obs['responseSHA256']})
    trace_queries(read(file('recovered-http.json')),run,first,collections.defaultdict(list),'recovered-http.json',stage='BEFORE_RETRY')
    trace_queries(read(file('second-recovery-http.json')),run,final_first,queries,case+'/second-recovery-http.json',stage='SECOND_RECOVERY')
    local = read(file('summary.json'))
    selected_summary = [s for s in summary['cases'] if s['boundary'] == case]
    require(selected_summary == [local] and local['originalUnknown'] == len(attempts)
            and local['followup404Unknown'] == unresolved and local['crashCommands'] == len(records)
            and local['finalCommands'] == len(final_records) and local['snapshotSeq'] == str(snap)
            and local['tailCommands'] == str(len(records)-snap) and local['trades'] == len(actual['trades'])
            and local['signal'] == 'SIGKILL' and local['concurrentRetries'] == 4
            and local['secondRecoveryRetries'] == len(final_records) and local['powerLossTested'] is False, 'crash summary mismatch')
    group = {'boundary':case,'clients':4,'phaseAttempts':8,'originalUnknown':len(attempts),'snapshotSeq':str(snap),
             'tailCommands':str(len(records)-snap),'crashCommands':len(records),'finalCommands':len(final_records),
             'preRetry404Unknown':unresolved,'walOrder':order_rows(final_records,run,snap,len(records))}
    return finish_concurrent(entry,run,attempts,{},queries,retries,
                             {'verificationScope':'sealed evidence consistency; no matching/recovery rerun','processKillClaim':True,'case':case,'groups':[group]},
                             {'bundle':inputs.bundle(root)[1],'scenarioSHA256':digest(inputs.file(root,'scenario.json')),'fullRecoveryClaim':summary['status']})
