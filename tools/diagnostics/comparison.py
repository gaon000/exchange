"""Read-only raw-evidence comparisons; descriptive changes never imply causality."""
import hashlib
import json
import math
import os
from pathlib import Path
import statistics
import subprocess
import tempfile
from analysis import timer
from archive import verify_archive
from build import go
from common import REPO, child, digest, lines, read, require, save, seal, verify
from metrics import process, stamp
from runner import source_hashes

# Values from different cohorts are deliberately separate and labelled.
METRICS = {
    'requestP99Ms': ('요청 p99', 'ms'),
    'scheduledP99Ms': ('예정 시각 기준 p99', 'ms'),
    'newOrderScheduledP99Ms': ('고유 신규 승인 예정 p99', 'ms'),
    'worst5sRequestP99Ms': ('5초 구간 요청 p99 최댓값', 'ms'),
    'worst5sScheduledP99Ms': ('5초 구간 예정 p99 최댓값', 'ms'),
    'windowsOverReference': ('참조 지연 상한 초과 구간', '구간'),
    'generatorMisses': ('생성기 미전송', '건'),
    'newOrdersPerSecond': ('구간 내 완료 신규 승인', 'orders/s'),
    'serverLifetimeCpuSeconds': ('서버 생애 CPU 증분', 's'),
    'serverPeakRssMiB': ('서버 생애 peak RSS', 'MiB'),
    'snapshotCompleted': ('서버 생애 완료 snapshot', '건'),
    'snapshotMedianWallMs': ('snapshot 호출 UTC 경과 중앙값', 'ms'),
    'restartReadyMs': ('재시작→ready/health', 'ms'),
    'timerSchedulerP99Ms': ('타이머 관찰 p99', 'ms'),
    'timerWorkerP99Ms': ('예정→worker p99', 'ms'),
    'timerFailures': ('타이머/worker 실패', '건'),
}


def canonical(value):
    return json.dumps(value, sort_keys=True, ensure_ascii=False, separators=(',', ':'), allow_nan=False)


def fingerprint(value):
    return hashlib.sha256(canonical(value).encode()).hexdigest()


def spec_inputs(path, resolver=None):
    path = path.resolve()
    data = read(path)
    require(set(data) <= {'version', 'criteria', 'comparisons'} and data['version'] == 1,
            'unsupported comparison spec')
    groups = data['comparisons']
    require(isinstance(groups, list) and 0 < len(groups) <= 20, 'require 1..20 comparisons')
    names, total = set(), 0
    for group in groups:
        require(set(group) == {'name', 'baseline', 'candidate'}, 'invalid comparison fields')
        require(isinstance(group['name'], str) and 0 < len(group['name']) <= 200
                and group['name'] not in names, 'invalid/duplicate comparison name')
        names.add(group['name'])
        for side in ('baseline', 'candidate'):
            require(isinstance(group[side], list) and 0 < len(group[side]) <= 100, 'require 1..100 inputs per side')
            for entry in group[side]:
                require(isinstance(entry, dict) and set(entry) <= {'bundle', 'case'}
                        and isinstance(entry.get('bundle'), str) and entry['bundle'], 'invalid input selector')
                selected = path.parent/entry['bundle']
                entry['bundle'] = str(resolver(selected) if resolver else selected.resolve())
                if 'case' in entry:
                    require(isinstance(entry['case'], str) and len(Path(entry['case']).parts) == 1,
                            'case must name one immediate directory')
                    child(Path(entry['bundle']), entry['case'])
                total += 1
    require(total <= 100, 'maximum total inputs is 100')
    criteria = path.parent/data['criteria'] if 'criteria' in data else (
        'config/benchmarks/criteria-v1.json' if resolver else REPO/'config/benchmarks/criteria-v1.json')
    criteria = resolver(criteria) if resolver else Path(criteria).resolve()
    return data, criteria


def checked_command(args):
    result = subprocess.run(list(map(str, args)), cwd=REPO, capture_output=True, text=True, timeout=600)
    require(result.returncode == 0, f'checker failed: {result.stderr[-2000:]}')


def file_provenance(root, name, files):
    path = child(root, name)
    require(name in files, f'input not covered by bundle inventory: {name}')
    return path


class Evidence:
    def __init__(self, scratch, criteria):
        self.scratch, self.criteria = scratch, criteria
        self.checker, self.judge = scratch/'experimentcheck', scratch/'benchreport'
        go('build', '-o', self.checker, './tools/experimentcheck')
        go('build', '-o', self.judge, './tools/benchreport')
        self.inventories, self.cache = {}, {}
        self.counter = 0

    def next_output(self):
        self.counter += 1
        return self.scratch/f'result-{self.counter}.json'

    def is_p6(self, entry):
        root = Path(entry['bundle'])
        return 'case' not in entry and (root/'loadgen/manifest.json').is_file()

    def judge_side(self, entries):
        selected = sorted({e['bundle'] for e in entries if self.is_p6(e)})
        if not selected:
            return {}
        output = self.next_output()
        checked_command([self.judge, '-criteria', self.criteria, '-output', output, *selected])
        return read(output)

    def load(self, entry, judged):
        key = canonical(entry)
        if key in self.cache:
            return self.cache[key]
        record = {'input': entry, 'status': 'error', 'measurementValid': False,
                  'reasons': [], 'metrics': {}, 'windows': []}
        try:
            root = Path(entry['bundle'])
            p6 = self.is_p6(entry)
            case = root if p6 else child(root, entry.get('case', ''))
            if p6:
                decision = next(x for x in judged['cases'] if Path(x['bundle']).resolve() == root)
                record['criteriaDecision'] = decision
                record['reasons'] += decision['reasons']
                require(decision['summaryRecomputed'] and not any(x.startswith('EVIDENCE:') for x in decision['reasons']),
                        'benchreport evidence failure: '+canonical(decision['reasons']))
                raw = {'summary': decision['summary'], 'windows': {'5s': decision['windows']}}
                record['format'] = 'p6-experiment'
                record['identity'] = decision['runId']
                record['series'] = decision['series']
                record['criterionPass'] = decision['pass']
                record['evidence'] = {'completionSHA256': digest(root/'completed.json') if decision['sealed'] else None,
                                      'sealed': decision['sealed']}
                protocol, environment = {}, read(root/'environment.json')
                binaries = {'exchange': decision['serverSHA256'], 'loadgen': decision['generatorSHA256']}
            else:
                if root not in self.inventories:
                    integrity = verify(root, allow_failed=True)
                    manifest = root/('artifact-manifest.json' if integrity['legacyInventory'] else 'manifest.json')
                    self.inventories[root] = (integrity, read(manifest)['files'], digest(manifest))
                integrity, files, manifest_sha = self.inventories[root]
                record['evidence'] = {**integrity, 'manifestSHA256': manifest_sha}
                require(integrity['completed'], 'bundle is incomplete')
                prefix = case.name+'/'
                def covered(name):
                    return file_provenance(root, prefix+name, files)
                protocol = read(file_provenance(root, 'protocol.json', files))
                environment = read(file_provenance(root, 'environment.json', files)) if (root/'environment.json').exists() else {}
                if (case/'archive-manifest.json').exists():
                    covered('archive-manifest.json')
                    record['evidence']['archive'] = verify_archive(case)
                if not (case/'loadgen').is_dir():
                    for name in ('run.json', 'samples.jsonl'):
                        covered(name)
                    if read(case/'run.json')['traceEnabled']:
                        covered('timer.trace')
                    value = timer(case)
                    meta = value['meta']
                    record.update(format='timer-diagnostic', status='verified', identity=fingerprint(
                        {'samples': digest(case/'samples.jsonl'), 'meta': meta}),
                        measurementValid=not value['failures'], conditions={
                            'kind': 'timer', 'durationNanos': meta['durationNanos'], 'workers': meta['workers'],
                            'rate': meta['requestsPerSecond'], 'trace': meta['traceEnabled'],
                            'environment': self.environment(environment, protocol)},
                        metrics={'timerSchedulerP99Ms': value['scheduler']['p99Ms'],
                                 'timerWorkerP99Ms': value['workerScheduled']['p99Ms'],
                                 'timerFailures': sum(value['failures'].values())},
                        windows=value['windows'], timerFailures=value['failures'])
                    if value['failures']:
                        record['reasons'].append('TIMER_FAILURES')
                    record['reasons'].append('DIAGNOSTIC_NOT_P6_BASELINE')
                    record['build'] = {'binarySHA256': {k: digest(file_provenance(root, k, files))
                                       for k in ('bin/timerprobe', 'bin/timer') if k in files}}
                    require(record['build']['binarySHA256'], 'timer binary provenance missing')
                    self.cache[key] = record
                    return record
                for name in ('manifest.json', 'plan.jsonl', 'samples.jsonl', 'summary.json', 'run.json', 'preparation.json'):
                    covered('loadgen/'+name)
                manifest = read(case/'loadgen/manifest.json')
                if manifest.get('diagnosticsIntervalNanos', 0):
                    covered('loadgen/dispatch.jsonl')
                    covered('loadgen/generator-metrics.jsonl')
                output = self.next_output()
                checked_command([self.checker, case/'loadgen', output])
                raw = read(output)
                record['format'] = 'retained-trade'
                record['identity'] = manifest['server']['runId']
                record['reasons'].append('DIAGNOSTIC_OR_NONSTANDARD_BUNDLE')
                for name in ('server-ready.json', 'server-metrics.jsonl', 'verification.json', 'run/commands.wal'):
                    covered(name)
                recovery = read(case/'verification.json')
                require(recovery['runId'] == record['identity'] and recovery['status'] == 'PASSED' and recovery['before'] == recovery['after']
                        == recovery['afterRetries'] and digest(case/'run/commands.wal') == recovery['walSHA256'],
                        'full recovery or WAL evidence mismatch')
                archived = read(case/'archive-manifest.json')['files'] if (case/'archive-manifest.json').exists() else {}
                for stage, field in [('before-restart', 'before'), ('after-restart', 'after'), ('after-retries', 'afterRetries')]:
                    name = stage+'.jsonl'
                    state_sha = digest(covered(name)) if (case/name).exists() else archived[name]['sha256']
                    require(state_sha == recovery[field]['sha256'], 'full-state capture hash mismatch')
                record['recovery'] = {'verified': True, 'restartReadyNanos': recovery['restartReadyNanos'],
                                      'stateSHA256': recovery['before']['sha256']}
                binaries = {name: digest(file_provenance(root, 'bin/'+name, files)) for name in ('exchange', 'loadgen')}
                if (case/'snapshot-events.jsonl').exists():
                    covered('snapshot-events.jsonl')
            manifest = read(case/'loadgen/manifest.json')
            ready = read(case/'server-ready.json')
            require(ready == manifest['server'], 'ready/manifest settings mismatch')
            summary = raw['summary']
            record.update(status='verified', measurementValid=summary['measurementValid'],
                          summary=summary, windows=raw['windows'].get('5s', []))
            record['reasons'] += summary.get('invalidReasons') or []
            record['build'] = {'server': manifest['server']['build'], 'generator': manifest['generator'],
                               'binarySHA256': binaries}
            exp = ready['experiments']
            if not p6 and exp['snapshotLog']:
                covered('snapshot-events.jsonl')
            record['conditions'] = {'kind': 'trade', 'settings': manifest['settings'],
                                    'initialConfig': manifest['initialConfig'], 'runtime': ready['runtime'],
                                    'http': ready['http'], 'metrics': {k: v for k, v in ready['metrics'].items() if k != 'file'},
                                    'snapshotInterval': exp['snapshotInterval'], 'snapshotLimit': exp.get('snapshotLimit'),
                                    'profile': bool(exp['profileDir']), 'memProfileRate': exp['memProfileRate'],
                                    'generatorDiagnosticsNanos': manifest.get('diagnosticsIntervalNanos', 0),
                                    'trace': (case/'server.trace').exists(), 'cIo': (case/'server.trace.sync.jsonl').exists(),
                                    'toolchain': {k: ready['build'][k] for k in ('goVersion', 'goos', 'goarch')},
                                    'generatorToolchain': {k: manifest['generator'][k] for k in ('goVersion', 'goos', 'goarch')},
                                    'environment': self.environment(environment, protocol)}
            record['metrics'] = self.trade_metrics(case, raw)
        except (ValueError, OSError, KeyError, TypeError, StopIteration, subprocess.SubprocessError) as error:
            record['status'] = 'error'
            record['reasons'].append('EVIDENCE: '+str(error)[:2000])
        self.cache[key] = record
        return record

    @staticmethod
    def environment(env, protocol):
        # Time, free space and process build revision are observations, not
        # environmental matching keys. Missing hardware stays explicit.
        return {'platform': env.get('platform', protocol.get('platform')),
                'cpuCount': env.get('cpuCount', env.get('logicalCPUs')),
                'runnerGOMAXPROCS': env.get('runnerGOMAXPROCS'),
                'machine': env.get('machine'), 'hardware': env.get('hardware'),
                'storage': {k: v for k, v in env.get('storage', {}).items() if k != 'availableBytes'} or None,
                'runtimeEnvironment': env.get('runtimeEnvironment', protocol.get('runtimeEnvironment'))}

    def trade_metrics(self, case, raw):
        meter = raw['summary']['measurement']
        windows = raw['windows'].get('5s', [])
        limit = read(self.criteria)['p99LimitNanos']
        def latency(group, key):
            return group[key]['p99Nanos']/1e6 if group[key]['count'] else None
        result = {'requestP99Ms': latency(meter, 'requestLatency'),
                  'scheduledP99Ms': latency(meter, 'scheduledAttemptLatency'),
                  'newOrderScheduledP99Ms': latency(meter, 'uniqueNewOrderScheduledLatency'),
                  'generatorMisses': sum((meter['localFailures'] or {}).values()),
                  'newOrdersPerSecond': meter['newOrdersPerSecond'],
                  'worst5sRequestP99Ms': max((w['requestLatency']['p99Nanos']/1e6 for w in windows), default=None),
                  'worst5sScheduledP99Ms': max((w['scheduledAttemptLatency']['p99Nanos']/1e6 for w in windows), default=None),
                  'windowsOverReference': sum(w['requestLatency']['p99Nanos'] > limit or
                                             w['scheduledAttemptLatency']['p99Nanos'] > limit for w in windows) if windows else None}
        metrics = lines(case/'server-metrics.jsonl')
        require(len(metrics) >= 2 and metrics[0]['stage'] == 'initial' and metrics[-1]['stage'] == 'final',
                'incomplete server metrics')
        resources = process(metrics[0], metrics[-1])
        result['serverLifetimeCpuSeconds'] = resources['cpuMs']/1000 if resources['cpuMs'] is not None else None
        result['serverPeakRssMiB'] = resources['peakRssMiB']
        if (case/'snapshot-events.jsonl').exists():
            events = lines(case/'snapshot-events.jsonl')
            finished = [e for e in events if e['outcome'] == 'COMPLETED']
            elapsed = [(stamp(e['finishedUTC'])-stamp(e['startedUTC']))*1000 for e in finished]
            # UTC clock regressions make this observation unavailable.
            result['snapshotCompleted'] = len(finished)
            result['snapshotMedianWallMs'] = statistics.median(elapsed) if elapsed and min(elapsed) >= 0 else None
        recovery = read(case/'verification.json') if (case/'verification.json').is_file() else {}
        result['restartReadyMs'] = recovery['restartReadyNanos']/1e6 if 'restartReadyNanos' in recovery else None
        return result


def differences(a, b, prefix=''):
    if isinstance(a, dict) and isinstance(b, dict):
        result = []
        for key in sorted(a.keys() | b.keys()):
            path = prefix+'.'+key if prefix else key
            result += differences(a.get(key), b.get(key), path)
        return result
    return [] if a == b else [{'field': prefix, 'baseline': a, 'candidate': b}]


def aggregate(records, key):
    values = [r['metrics'][key] for r in records if r['status'] == 'verified'
              and r['metrics'].get(key) is not None]
    require(all(isinstance(v, (int, float)) and not isinstance(v, bool) and math.isfinite(v) for v in values),
            'non-finite comparison metric')
    return {'observedRuns': len(values), 'median': statistics.median(values) if values else None,
            'min': min(values) if values else None, 'max': max(values) if values else None}


def compare_group(name, baseline, candidate, minimum_repeats, qualifications):
    blockers = []
    for label, records in [('baseline', baseline), ('candidate', candidate)]:
        if any(r['status'] != 'verified' for r in records):
            blockers.append(label+':EVIDENCE_ERROR')
        if any(not r['measurementValid'] for r in records):
            blockers.append(label+':INVALID_MEASUREMENT')
        identities = [r.get('identity') for r in records]
        if None in identities or len(set(identities)) != len(identities):
            blockers.append(label+':DUPLICATE_OR_MISSING_RUN_ID')
        if len(set(identities)-{None}) < minimum_repeats:
            blockers.append(label+':INSUFFICIENT_REPEATS')
        condition_set = {canonical(r.get('conditions')) for r in records}
        build_set = {canonical(r.get('build')) for r in records}
        if len(condition_set) != 1 or len(build_set) != 1:
            blockers.append(label+':MIXED_CONDITIONS_OR_BUILDS')
        series = {r.get('series') for r in records if r.get('format') == 'p6-experiment'}
        if series and (None in series or len(series) != 1):
            blockers.append(label+':MIXED_OR_MISSING_BENCHREPORT_SERIES')
        if any(not r.get('conditions', {}).get('environment', {}).get('hardware')
               or not r.get('conditions', {}).get('environment', {}).get('storage') for r in records):
            blockers.append(label+':INCOMPLETE_ENVIRONMENT')
        if not qualifications.get(label, False) or any(r.get('format') != 'p6-experiment'
                                                       or not r.get('criterionPass') for r in records):
            blockers.append(label+':NOT_QUALIFIED_BY_BENCHREPORT')
    overlap = {r.get('identity') for r in baseline} & {r.get('identity') for r in candidate} - {None}
    if overlap:
        blockers.append('SHARED_RUNS_BETWEEN_SIDES')
    known_conditions = all(isinstance(r.get('conditions'), dict) and r['conditions'] for r in baseline+candidate)
    if not known_conditions:
        blockers.append('INCOMPLETE_CONDITIONS')
    condition_diff = differences(baseline[0].get('conditions'), candidate[0].get('conditions'))
    if condition_diff:
        blockers.append('CONDITIONS_DIFFER')
    metrics = {}
    for key, (label, unit) in METRICS.items():
        a, b = aggregate(baseline, key), aggregate(candidate, key)
        delta = b['median']-a['median'] if a['median'] is not None and b['median'] is not None else None
        metrics[key] = {'label': label, 'unit': unit, 'baseline': a, 'candidate': b, 'delta': delta,
                        'percentChange': delta/abs(a['median'])*100 if delta is not None and a['median'] else None}
    return {'name': name, 'baseline': baseline, 'candidate': candidate,
            'conditions': {'differences': condition_diff, 'observedFieldsMatch': bool(known_conditions and not condition_diff)},
            'buildDifferences': differences(baseline[0].get('build'), candidate[0].get('build')),
            'judgement': {'eligibleForReview': not blockers, 'improvementClaimed': False,
                          'blockers': blockers, 'meaning': 'descriptive changes; no causal or statistical improvement claim'},
            'metrics': metrics}


def escape(value):
    return str(value).replace('&', '&amp;').replace('<', '&lt;').replace('>', '&gt;').replace('|', '&#124;').replace('`', '&#96;').replace('\r', ' ').replace('\n', '<br>')


def number(value):
    return '—' if value is None else format(value, '.9g')


def markdown(report):
    output = ['# 실험 결과 비교', '', '차이는 후보 − 기준이다. 각 수치는 실행별 값의 중앙값이며 표본을 합쳐 p99를 다시 계산하지 않는다.',
              '실패·무효 실행도 목록에 보존한다. 수치 차이는 관찰값이며 인과 효과·성능 개선 채택을 뜻하지 않는다.',
              '서버 자원/snapshot은 프로세스 생애, 지연/처리량은 측정 cohort, 복구는 정상 재시작 관찰이다. 누락은 —로 표시한다.', '']
    for group in report['comparisons']:
        output += ['## '+escape(group['name']), '',
                   '검토 가능: '+str(group['judgement']['eligibleForReview']).lower(), '',
                   '판정 제한: '+escape(', '.join(group['judgement']['blockers']) or '없음; 자동 개선 판정은 하지 않음'), '',
                   '| 지표 | 단위 | 기준 중앙값 [최소~최대; n] | 후보 중앙값 [최소~최대; n] | 차이 | 변화율 % |',
                   '| --- | --- | --- | --- | --- | --- |']
        for metric in group['metrics'].values():
            if metric['baseline']['observedRuns']+metric['candidate']['observedRuns'] == 0:
                continue
            def cell(value):
                return f"{number(value['median'])} [{number(value['min'])}~{number(value['max'])}; {value['observedRuns']}]"
            output.append('| '+' | '.join([metric['label'], metric['unit'], cell(metric['baseline']), cell(metric['candidate']),
                                           number(metric['delta']), number(metric['percentChange'])])+' |')
        output += ['', '### 입력과 유효성', '', '| 측 | 묶음 / case | 원본 재계산 | 무결성 형식 | 측정 유효 | 사유 |', '| --- | --- | --- | --- | --- | --- |']
        for side in ('baseline', 'candidate'):
            for record in group[side]:
                evidence = record.get('evidence', {})
                integrity = ('legacy inventory' if evidence.get('legacyInventory') else
                             'completed' if evidence.get('completed', evidence.get('sealed', False)) else 'unsealed/unknown')
                output.append('| '+' | '.join(map(escape, [side, record['input']['bundle']+' / '+record['input'].get('case', '(P6 root)'),
                                                            record['status'], integrity, record['measurementValid'], ', '.join(record['reasons'])]))+' |')
        output += ['', '### 조건 차이', '', '| 필드 | 기준 | 후보 |', '| --- | --- | --- |']
        for diff in group['conditions']['differences']:
            output.append('| '+' | '.join([escape(diff['field']), escape(canonical(diff['baseline'])), escape(canonical(diff['candidate']))])+' |')
        if not group['conditions']['differences']:
            output += ['| 기록된 필드 | 같음 | 같음 |' if group['conditions']['observedFieldsMatch']
                       else '| 조건 자료 | 알 수 없음 | 알 수 없음 |']
        output += ['', '코드·바이너리 차이: '+escape(canonical(group['buildDifferences'])), '']
    output += ['실제 전원 장애 검증은 보류이며 이 보고서는 해당 검증을 수행하지 않는다.', '']
    return '\n'.join(output)


def compare(spec_path, output, resolver=None):
    spec, criteria = spec_inputs(spec_path, resolver)
    criteria_data = read(criteria)
    minimum = criteria_data['minimumRepeats']
    require(type(minimum) is int and 3 <= minimum <= 100 and criteria_data['version'] == 1
            and criteria_data['windowNanos'] == 5_000_000_000 and criteria_data['p99LimitNanos'] > 0,
            'comparison v1 requires v1 criteria, 5s windows and 3..100 repeats')
    require(not output.is_symlink(), 'output directory already exists as a symlink')
    output = output.resolve()
    for group in spec['comparisons']:
        for side in ('baseline', 'candidate'):
            for entry in group[side]:
                require(not output.is_relative_to(Path(entry['bundle'])), 'output must be outside every input bundle')
    require(not output.exists() and not output.is_symlink(), 'output directory already exists')
    # Build and inspect before publishing; imported binaries are never executed.
    with tempfile.TemporaryDirectory(prefix='exchange-comparison-') as temp:
        adapter = Evidence(Path(temp), criteria)
        groups = []
        for group in spec['comparisons']:
            sides, decisions, criteria_groups = {}, {}, {}
            for side in ('baseline', 'candidate'):
                judged = adapter.judge_side(group[side])
                sides[side] = [adapter.load(entry, judged) for entry in group[side]]
                decisions[side] = bool(judged.get('groups')) and all(x['qualified'] for x in judged['groups'])
                criteria_groups[side] = judged.get('groups', [])
            compared = compare_group(group['name'], sides['baseline'], sides['candidate'], minimum, decisions)
            compared['criteriaGroups'] = criteria_groups
            groups.append(compared)
    report = {'version': 1, 'specSHA256': digest(spec_path), 'resolvedSpec': spec,
              'criteriaSHA256': digest(criteria), 'minimumRepeats': minimum,
              'sourceSHA256': source_hashes(), 'comparisons': groups, 'powerLossTested': False,
              'notes': ['Missing environment/metrics remain unknown.', 'All verified runs, including invalid measurements, enter descriptive statistics.']}
    output.parent.mkdir(parents=True, exist_ok=True)
    output.mkdir(mode=0o700)
    try:
        save(output/'report.json', report)
        with (output/'report.md').open('x') as target:
            target.write(markdown(report))
            target.flush()
            os.fsync(target.fileno())
        seal(output, completed=True)
    except BaseException as error:
        save(output/'failed.json', {'error': repr(error)})
        raise
    return report
