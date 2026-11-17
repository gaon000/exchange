"""Offline transport/UNKNOWN reports. Never query servers or retry commands."""
import collections
import html
import hashlib
import math
import os
from pathlib import Path
import subprocess
import tempfile

from build import build_checker
from common import child, decode, digest, lines, read, require, save, seal, verify
from runner import source_hashes

STAGES = {'request_build', 'connect', 'request_write', 'response_headers', 'response_read', 'response_close', 'round_trip'}
ERRORS = {'deadline_exceeded', 'canceled', 'timeout', 'connection_refused', 'connection_reset', 'broken_pipe',
          'unexpected_eof', 'eof', 'closed', 'network_unreachable', 'other'}
TRANSPORT = {'TRANSPORT_FAILURE', 'TRANSPORT_TIMEOUT'}
UNKNOWN = TRANSPORT | {'SERVER_TIMEOUT', 'SERVER_FAILURE', 'INVALID_RESPONSE'}


def integer(value, label):
    require(type(value) is int and 0 <= value <= 2**63-1, f'invalid {label}')
    return value


def real_path(path):
    path = Path(path).absolute()
    for part in (path, *path.parents):
        require(not part.is_symlink(), f'symlink in report path: {part}')
    return path.resolve()


def spec_inputs(path):
    path = real_path(path)
    spec = read(path)
    require(set(spec) == {'version', 'inputs'} and type(spec['version']) is int and spec['version'] == 1, 'unsupported transport report spec')
    require(isinstance(spec['inputs'], list) and 1 <= len(spec['inputs']) <= 32, 'require 1..32 inputs')
    names, selected = set(), set()
    for entry in spec['inputs']:
        require(isinstance(entry, dict) and set(entry) <= {'name', 'kind', 'bundle', 'case', 'followups'}, 'invalid input fields')
        require(isinstance(entry.get('name'), str) and 0 < len(entry['name']) <= 200 and entry['name'] not in names, 'invalid/duplicate input name')
        names.add(entry['name'])
        require(entry.get('kind') in ('loadgen', 'recoverycheck', 'concurrent', 'concurrent-crash') and isinstance(entry.get('bundle'), str) and entry['bundle'], 'invalid input selector')
        entry['bundle'] = str(real_path(path.parent/entry['bundle']))
        require(Path(entry['bundle']).is_dir(), 'input bundle is not a directory')
        if entry['kind'] in ('loadgen', 'concurrent-crash'):
            require(isinstance(entry.get('case'), str) and len(Path(entry['case']).parts) == 1, 'loadgen requires one case directory')
            child(entry['bundle'], entry['case'])
            if entry['kind'] == 'concurrent-crash':
                require(entry['case'] in ('wal-after-sync', 'snapshot-before-publish', 'snapshot-after-publish')
                        and 'followups' not in entry, 'invalid crash case/followups')
        else:
            require('case' not in entry and 'followups' not in entry, 'scenario uses its own query trace')
        key = (entry['bundle'], entry.get('case'))
        require(key not in selected, 'duplicate input run')
        selected.add(key)
        followups = entry.get('followups', [])
        require(isinstance(followups, list) and len(followups) <= 64, 'too many followup receipts')
        seen = set()
        for follow in followups:
            require(isinstance(follow, dict) and set(follow) == {'bundle', 'result', 'ready'}, 'followup requires bundle/result/ready')
            require(all(isinstance(follow[k], str) and follow[k] for k in follow), 'invalid followup selector')
            follow['bundle'] = str(real_path(path.parent/follow['bundle']))
            for key in ('result', 'ready'):
                child(follow['bundle'], follow[key])
            key = (follow['bundle'], follow['result'])
            require(key not in seen, 'duplicate followup receipt')
            seen.add(key)
    return path, spec


class Inputs:
    def __init__(self):
        self.manifests = {}

    def bundle(self, root):
        root = Path(root)
        if root not in self.manifests:
            status = verify(root)
            manifest = root/('artifact-manifest.json' if status['legacyInventory'] else 'manifest.json')
            self.manifests[root] = (read(manifest)['files'], {**status, 'manifestSHA256': digest(manifest)})
        return self.manifests[root]

    def file(self, root, name):
        files, _ = self.bundle(root)
        require(name in files, f'input not in sealed manifest: {name}')
        path = child(root, name)
        require(path.stat().st_size == files[name]['bytes'] and digest(path) == files[name]['sha256'], f'input changed: {name}')
        return path


def key(run, account, kind, request_id):
    require(all(isinstance(s, str) and 0 < len(s) <= 200 for s in (run, account, request_id)) and kind in ('place', 'cancel'), 'invalid logical request key')
    return run, account, kind, request_id


def key_object(value):
    return dict(zip(('runId', 'accountId', 'kind', 'requestId'), value))


def command_key(command, run=None, wire=False):
    if wire:
        run, account, kind = command['RunID'], command['AccountID'], command['Kind']
        payload = command['Place'] if kind == 'PLACE_ORDER' else command['Cancel']
    else:
        account, kind = command['accountId'], command['kind']
        payload = command['place'] if kind == 'PLACE_ORDER' else command['cancel']
    require(kind in ('PLACE_ORDER', 'CANCEL_ORDER'), 'unknown command kind')
    namespace = 'place' if kind == 'PLACE_ORDER' else 'cancel'
    return key(run, account, namespace, payload['clientOrderId' if namespace == 'place' else 'cancelRequestId'])


def diagnostic(outcome):
    stage, error = outcome.get('failureStage'), outcome.get('errorType')
    require((stage is None) == (error is None), 'partial transport diagnostic')
    if stage is not None:
        require(outcome['class'] in TRANSPORT and stage in STAGES and error in ERRORS, 'invalid transport diagnostic')
    require(outcome['class'] not in TRANSPORT or outcome['certainty'] == 'UNKNOWN', 'transport cannot prove non-admission')
    return stage, error


def attempt(request_key, phase, index, outcome, latency, **extra):
    require(outcome['certainty'] == 'UNKNOWN' and outcome['class'] in UNKNOWN, 'unexpected UNKNOWN response class')
    stage, error = diagnostic(outcome)
    if latency is not None:
        integer(latency, 'request latency')
    return {**key_object(request_key), 'phase': phase, 'index': index, 'class': outcome['class'],
            'measurementCertainty': 'UNKNOWN', 'failureStage': stage, 'errorType': error,
            'requestLatencyNanos': latency, **extra}


def observation(status, raw, request_key, source, **extra):
    value = decode(raw)
    run, account, kind, request_id = request_key
    if status == 200:
        require(type(value.get('schemaVersion')) is int and value.get('schemaVersion') == 1 and value.get('runId') == run and value.get('accountId') == account
                and value.get('kind') == ('PLACE_ORDER' if kind == 'place' else 'CANCEL_ORDER')
                and value.get('requestId') == request_id, 'query result identity mismatch')
        seq = value.get('commandSeq')
        require(isinstance(seq, str) and seq.isascii() and seq.isdecimal() and 0 < int(seq) <= 2**63-1
                and str(int(seq)) == seq, 'invalid query command sequence')
        require(value.get('code') in ({'ACCEPTED', 'INSUFFICIENT_BALANCE'} if kind == 'place' else
                                    {'CANCELLED', 'ALREADY_CANCELLED', 'ALREADY_FILLED', 'ORDER_NOT_FOUND'}), 'invalid query result code')
        state, resolution = 'COMPLETED', 'RESULT_OBSERVED'
    else:
        require((status, value) in ((404, {'state': 'UNKNOWN'}), (202, {'state': 'PENDING'})), 'invalid pending/unknown query receipt')
        state, resolution, seq = value['state'], 'UNRESOLVED', None
    return {'httpStatus': status, 'state': state, 'resolution': resolution, 'commandSeq': seq,
            'source': source, 'responseSHA256': hashlib.sha256(raw.encode()).hexdigest(), **extra}, value


def distribution(values):
    known = sorted(v for v in values if v is not None)
    return {'observedCount': len(known), 'missingCount': len(values)-len(known),
            'p50Nanos': known[math.ceil(len(known)*.5)-1] if known else None,
            'p99Nanos': known[math.ceil(len(known)*.99)-1] if known else None,
            'maxNanos': known[-1] if known else None}


def finish(entry, identity, attempts, business, followups, original, provenance):
    groups = collections.defaultdict(list)
    logical = {}
    for row in attempts:
        k = key(row['runId'], row['accountId'], row['kind'], row['requestId'])
        logical.setdefault(k, []).append(row)
        if row['class'] in TRANSPORT:
            groups[(row['phase'], row['class'], row['failureStage'], row['errorType'])].append(row)
    unknown = []
    for k, rows in sorted(logical.items()):
        queries = followups.get(k, [])
        complete = [q for q in queries if q['resolution'] == 'RESULT_OBSERVED']
        fingerprints = {q['responseSHA256'] for q in complete}
        fingerprints.update(business.get(k, set()))
        require(len(fingerprints) <= 1, 'same key returned different first response bytes')
        unknown.append({**key_object(k), 'unknownAttempts': len(rows), 'phases': sorted({r['phase'] for r in rows}),
                        'withinRunResolution': 'RESULT_OBSERVED' if business.get(k) else 'UNRESOLVED',
                        'subsequentResolution': 'RESULT_OBSERVED' if complete else 'UNRESOLVED', 'queries': queries})
    return {'name': entry['name'], 'kind': entry['kind'], 'runId': identity, 'evidence': provenance,
            'originalMeasurement': original,
            'counts': {'unknownAttempts': len(attempts), 'unknownLogicalKeys': len(unknown),
                       'transportFailureAttempts': sum(r['class'] in TRANSPORT for r in attempts),
                       'keysWithSubsequentResult': sum(x['subsequentResolution'] == 'RESULT_OBSERVED' for x in unknown),
                       'keysWithoutObservedResult': sum(x['subsequentResolution'] == x['withinRunResolution'] == 'UNRESOLVED' for x in unknown)},
            'transportGroups': [{'phase': p, 'class': c, 'failureStage': s, 'errorType': e, 'attempts': len(rows),
                                 'logicalKeys': len({(r['runId'], r['accountId'], r['kind'], r['requestId']) for r in rows}),
                                 'latency': distribution([r['requestLatencyNanos'] for r in rows])}
                                for (p, c, s, e), rows in sorted(groups.items(), key=lambda x: str(x[0]))],
            'attempts': attempts, 'unknownCommands': unknown}


def loadgen(entry, inputs, checker, scratch):
    root, case = Path(entry['bundle']), entry['case']
    def file(name): return inputs.file(root, case+'/loadgen/'+name)
    manifest = read(file('manifest.json'))
    identity = manifest['server']['runId']
    generated = scratch/(str(len(list(scratch.iterdir())))+'-raw.json')
    checked = subprocess.run([str(checker), '--verify-plan', str(child(root, case+'/loadgen')), str(generated)], capture_output=True, text=True, timeout=600)
    require(checked.returncode == 0, 'loadgen raw validation failed: '+checked.stderr[-2000:])
    raw = read(generated)
    require(raw['rawVerified'] is True and raw.get('planVerified') is True and type(raw['loadgenComplete']) is bool, 'loadgen raw/plan verification missing')
    # Invalid measurements intentionally have failed.json instead of completed.
    # The enclosing immutable bundle seals their raw files; never hide failures.
    marker = 'completed.json' if raw['loadgenComplete'] else 'failed.json'
    read(file(marker))
    for name, expected in raw['inputSHA256'].items():
        require(digest(file(name)) == expected, 'raw checker/bundle input hash mismatch')
    # Strict Python parsing supplements the typed Go replay of every raw sample.
    for name in ('summary.json', 'run.json'):
        read(file(name))
    plans = {'PREPARE': {}, 'RUN': {}}
    for r in lines(file('plan.jsonl')):
        k = command_key(r['command'], wire=True)
        require(k[0] == identity, 'plan run mismatch')
        group = 'PREPARE' if r['phase'] == 'PREPARE' else 'RUN'
        require(r['index'] not in plans[group], 'duplicate planned index')
        plans[group][r['index']] = (r, k)
    attempts, business = [], collections.defaultdict(set)
    samples = [('PREPARE', x) for x in read(file('preparation.json'))['samples']]
    samples.extend(('RUN', x) for x in lines(file('samples.jsonl')))
    for group, sample in samples:
        r, k = plans[group][sample['index']]
        o = sample['outcome']
        if not o.get('class'):
            continue # Unsent generator misses remain in the original summary.
        diagnostic(o)
        if o['class'] == 'BUSINESS':
            business[k].add(o['fingerprint'])
        if o['certainty'] == 'UNKNOWN':
            latency = sample['finishedNanos']-sample['startedNanos'] if group == 'RUN' else None
            attempts.append(attempt(k, r['phase'], sample['index'], o, latency,
                                    finishedUTC=sample.get('finishedUTC'), source=f'{case}/loadgen/'+('preparation.json' if group == 'PREPARE' else 'samples.jsonl')))
    followups, query_values = collections.defaultdict(list), {}
    unknown_keys = {(r['runId'], r['accountId'], r['kind'], r['requestId']) for r in attempts}
    receipts = []
    for follow in entry.get('followups', []):
        receipt = read(inputs.file(follow['bundle'], follow['result']))
        ready = read(inputs.file(follow['bundle'], follow['ready']))
        k = key(ready['runId'], receipt['account'], receipt['kind'], receipt['id'])
        require(k in unknown_keys and receipt['measurementCertainty'] == 'UNKNOWN', 'followup does not match an original UNKNOWN key')
        # Bind the archived sample index too; identity alone cannot select a different attempt.
        require(any(r['index'] == receipt['measuredIndex'] and (r['runId'],r['accountId'],r['kind'],r['requestId']) == k for r in attempts), 'followup sample index mismatch')
        require(decode(receipt['responseBytes']) == receipt['response'], 'followup decoded/body mismatch')
        obs, value = observation(receipt['httpStatus'], receipt['responseBytes'], k,
                                 str(Path(follow['bundle'])/follow['result']), observedUTC=receipt.get('observedUTC'), orderBasis='specification order')
        if obs['resolution'] == 'RESULT_OBSERVED':
            require(k not in query_values or value == query_values[k], 'query first result changed')
            query_values[k] = value
        followups[k].append(obs)
        receipts.append({'selector': follow, 'integrity': inputs.bundle(follow['bundle'])[1]})
    return finish(entry, identity, attempts, business, followups,
                  {'measurementValid': raw['summary']['measurementValid'], 'loadgenComplete': raw['loadgenComplete'],
                   'unknownLogicalCommands': raw['summary']['measurement']['unknownLogicalCommands'],
                   'measurement': raw['summary']['measurement'], 'scope': 'original Go aggregate; followup queries excluded'},
                  {'bundle': inputs.bundle(root)[1], 'rawRecomputed': True, 'planVerified': True, 'followups': receipts})


def recoverycheck(entry, inputs):
    root = Path(entry['bundle'])
    def file(name): return inputs.file(root, name)
    scenario, summary = read(file('scenario.json')), read(file('summary.json'))
    require(summary['status'] == 'PASSED' and read(root/'completed.json')['status'] == 'PASSED'
            and summary['scenarioSHA256'] == digest(file('scenario.json')), 'scenario completion mismatch')
    actual, expected = read(file('final-actual.json')), read(file('final-expected.json'))
    require(actual == expected and actual['results'], 'scenario final checkpoint mismatch')
    identities = {r['response']['runId'] for r in actual['results']}
    require(len(identities) == 1, 'mixed scenario runs')
    identity = identities.pop()
    first_results = {key(identity, r['response']['accountId'], 'place' if r['response']['kind'] == 'PLACE_ORDER' else 'cancel',
                         r['response']['requestId']): r['response'] for r in actual['results']}
    events = [x for x in lines(file('events.jsonl')) if x['phase'] == 'transport-observed']
    by_step = {x['step']: x for x in events}
    require(len(events) == len(by_step), 'duplicate transport event')
    traces = lines(file('http.jsonl'))
    attempts, followups, values = [], collections.defaultdict(list), {}
    steps = [(i, a) for i, a in enumerate(scenario['actions']) if a['kind'] == 'response-loss']
    require(len(steps) == summary['responseLosses'] == len(events) and set(by_step) == {i for i,_ in steps}, 'missing fault evidence')
    for i, action in steps:
        k = command_key(action['command'], identity)
        proof = read(file(f'transport-{i:03d}.json'))
        event = by_step[i]
        require(proof['fault'] == event['fault'] == action['fault'] and proof['outcome'] == event['outcome']
                and proof['sameIDVerified'] is True and proof['forwardedRequests'] == 1, 'fault/event mismatch')
        require(proof['outcome']['class'] in TRANSPORT and proof['upstreamFirstResponse'] == first_results.get(k), 'fault first result/checkpoint mismatch')
        latency = proof.get('requestLatencyNanos')
        require(latency == event.get('requestLatencyNanos'), 'fault timing mismatch')
        attempts.append(attempt(k, 'FAULT_SCENARIO', i, proof['outcome'], latency, fault=action['fault'], source=f'transport-{i:03d}.json'))
        path = '/v1/results/'+k[2]+'/'+k[3]
        for n, trace in enumerate(traces):
            if trace['step'] < i or trace['method'] != 'GET' or trace['account'] != k[1] or trace['path'] != path:
                continue
            require('error' not in trace, 'query trace contains a transport error')
            obs, value = observation(trace['status'], trace['response'], k, 'http.jsonl', step=trace['step'], line=n+1,
                                     observedUTC=None, orderBasis='scenario step and trace line')
            if obs['resolution'] == 'RESULT_OBSERVED':
                require(value == proof['upstreamFirstResponse'], 'query differs from upstream first response')
                require(k not in values or value == values[k], 'scenario first result changed')
                values[k] = value
            followups[k].append(obs)
        require(followups[k], 'scenario missing result query evidence')
        if proof['unknownLookup']:
            require(decode(proof['unknownLookup']) == {'state': 'UNKNOWN'}, 'invalid UNKNOWN witness')
            require(any(q['httpStatus'] == 404 for q in followups[k]), 'missing UNKNOWN query witness')
    return finish(entry, identity, attempts, {}, followups,
                  {'scope': 'controlled fault scenario, not a load measurement', 'measurementValid': None, 'unknownLogicalCommands': None},
                  {'bundle': inputs.bundle(root)[1], 'scenarioSHA256': summary['scenarioSHA256'], 'fullRecoveryClaim': summary['status']})


def cell(value):
    if value is None: return '미계측'
    return html.escape(str(value)).replace('|', '&#124;').replace('\n', '<br>').replace('`', '&#96;')


def ms(value):
    return '미계측' if value is None else f'{value/1e6:.6f}'


def markdown(report):
    out = ['# 전송 오류·UNKNOWN 분석', '',
           '측정 당시 UNKNOWN과 후속 조회는 별개다. 404 UNKNOWN·202 PENDING은 미확정이며 미접수 증거가 아니다.',
           '후속 200은 같은 키의 결과 관측이며 원래 전송 시도의 성공/접수 시점이나 성능 판정을 바꾸지 않는다.',
           '누락된 단계/유형·지연은 미계측이다. 장애 재현과 부하 측정의 수치를 합쳐 성능으로 해석하지 않는다.', '']
    for source in report['inputs']:
        out += ['## '+cell(source['name']), '', f"종류: {cell(source['kind'])}, run: {cell(source['runId'])}", '',
                f"원래 measurementValid: {cell(source['originalMeasurement']['measurementValid'])}; 본 측정 UNKNOWN 논리 명령: {cell(source['originalMeasurement']['unknownLogicalCommands'])}", '',
                '| phase | 응답 분류 | 실패 단계 | 오류 유형 | 시도 | 논리 키 | 지연 관측/누락 | p50 ms | p99 ms | 최대 ms |',
                '| --- | --- | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |']
        for g in source['transportGroups']:
            d = g['latency']
            out.append('| '+' | '.join(map(cell, [g['phase'],g['class'],g['failureStage'],g['errorType'],g['attempts'],g['logicalKeys'],
                       f"{d['observedCount']}/{d['missingCount']}",ms(d['p50Nanos']),ms(d['p99Nanos']),ms(d['maxNanos'])]))+' |')
        if not source['transportGroups']: out += ['| — | 전송 실패 없음 | — | — | 0 | 0 | — | — | — | — |']
        out += ['', '| phase/index | 계정/종류/요청 ID | 측정 당시 | 분류·단계·유형 | 요청 지연 ms |', '| --- | --- | --- | --- | ---: |']
        for r in source['attempts']:
            out.append('| '+' | '.join(map(cell, [f"{r['phase']}/{r['index']}",f"{r['accountId']}/{r['kind']}/{r['requestId']}",
                      r['measurementCertainty'], '/'.join(cell(r[k]) for k in ('class','failureStage','errorType')),ms(r['requestLatencyNanos'])]))+' |')
        out += ['', '| 계정/종류/요청 ID | 실험 내 완전 응답 관측 | 후속 조회 결과 관측 |', '| --- | --- | --- |']
        for r in source['unknownCommands']:
            out += ['| '+' | '.join(map(cell,[f"{r['accountId']}/{r['kind']}/{r['requestId']}",r['withinRunResolution'],r['subsequentResolution']]))+' |']
        out += ['', '| 요청 ID | 조회 HTTP/상태 | 해당 조회 해석 | step/line | UTC |', '| --- | --- | --- | --- | --- |']
        for r in source['unknownCommands']:
            for q in r['queries']:
                out += ['| '+' | '.join(map(cell,[r['requestId'],f"{q['httpStatus']}/{q['state']}",q['resolution'],
                         f"{q.get('step','—')}/{q.get('line','—')}",q['observedUTC']]))+' |']
        if 'execution' in source:
            out += ['', '자료 검증: 봉인·입력/저장 순서·상태/최초 응답의 일관성. 매칭/복구를 다시 실행한 결과가 아니다.', '',
                    '| 요청 ID | 재시도 전 첫 조회 | 같은 ID 재시도 관측 |', '| --- | --- | --- |']
            for r in source['unknownCommands']:
                out.append('| '+' | '.join(map(cell,[r['requestId'],r['firstFollowupResolution'],r['retryResolution']]))+' |')
            for g in source['execution']['groups']:
                out += ['', '| 경계/step | 클라이언트 | phase 시도 | 원래 UNKNOWN | snapshot seq | 복구 tail |', '| --- | ---: | ---: | ---: | ---: | ---: |']
                out.append('| '+' | '.join(map(cell,[f"{g['boundary']}/{g.get('step','—')}",g['clients'],g['phaseAttempts'],g['originalUnknown'],g['snapshotSeq'],g['tailCommands']]))+' |')
                out += ['', '| 실제 WAL seq | 계정/종류/요청 ID | 경계 구분 |', '| ---: | --- | --- |']
                for row in g['walOrder']:
                    out.append('| '+' | '.join(map(cell,[row['commandSeq'],f"{row['accountId']}/{row['kind']}/{row['requestId']}",row['segment']]))+' |')
                out += ['']
        out += ['']
    out += ['실제 전원 장애 검증: 미완료·보류.원본·성공 건수·실패율·measurementValid를 수정하지 않는다.', '']
    return '\n'.join(out)


def generate(spec_path, output):
    spec_path, spec = spec_inputs(spec_path)
    output = real_path(output)
    require(not output.exists(), 'report output already exists')
    require(output.parent.is_dir(), 'output parent must exist')
    roots = {Path(x['bundle']) for x in spec['inputs']}
    roots.update(Path(f['bundle']) for x in spec['inputs'] for f in x.get('followups', []))
    require(all(not output.is_relative_to(p) and not p.is_relative_to(output) for p in roots), 'output overlaps input bundle')
    require(not spec_path.is_relative_to(output), 'output overlaps specification')
    inputs = Inputs()
    with tempfile.TemporaryDirectory(prefix='exchange-transport-report-') as temp:
        scratch = Path(temp)
        checker = scratch/'experimentcheck'
        if any(x['kind'] == 'loadgen' for x in spec['inputs']): build_checker(checker)
        from concurrent_report import concurrent, concurrent_crash
        adapters = {'recoverycheck': recoverycheck, 'concurrent': concurrent, 'concurrent-crash': concurrent_crash}
        records = [loadgen(x, inputs, checker, scratch) if x['kind'] == 'loadgen' else adapters[x['kind']](x, inputs) for x in spec['inputs']]
    report = {'version': 1, 'specSHA256': digest(spec_path), 'resolvedSpec': spec, 'sourceSHA256': source_hashes(),
              'inputs': records, 'readOnly': True, 'powerLossTested': False,
              'interpretation': {'measurementReclassified': False, 'unknown404ProvesNonAdmission': False,
                                 'subsequentResultProvesOriginalAttemptSucceeded': False}}
    output.mkdir(mode=0o700)
    try:
        save(output/'report.json', report)
        with (output/'report.md').open('x') as f:
            f.write(markdown(report)); f.flush(); os.fsync(f.fileno())
        seal(output, completed=True)
    except BaseException as error:
        save(output/'failed.json', {'error': repr(error)})
        raise
    return report
