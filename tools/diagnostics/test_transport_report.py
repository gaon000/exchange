"""Transport counts, immutable UNKNOWN and sealed later observations."""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from common import digest, read, save, seal, verify
from transport_report import attempt, finish, generate, key, markdown, observation, spec_inputs

RUN = '00000000-0000-4000-8000-000000000001'
KEY = key(RUN, 'bob', 'place', 'lost')
OUTCOME = {'class': 'TRANSPORT_FAILURE', 'httpStatus': 0, 'certainty': 'UNKNOWN',
           'failureStage': 'response_headers', 'errorType': 'eof'}
RESULT = {'schemaVersion': 1, 'runId': RUN, 'accountId': 'bob', 'kind': 'PLACE_ORDER',
          'requestId': 'lost', 'commandSeq': '1', 'code': 'ACCEPTED', 'tradeCount': '0',
          'order': {'clientOrderId': 'lost', 'status': 'OPEN'}}
COMMAND = {'accountId': 'bob', 'kind': 'PLACE_ORDER', 'place': {'clientOrderId': 'lost'}}


def jsonl(path, rows):
    path.write_text(''.join(json.dumps(x, separators=(',', ':'))+'\n' for x in rows))


def reseal(root, scenario=False):
    for name in ('completed.json', 'manifest.json'):
        (root/name).unlink(missing_ok=True)
    seal(root, True)
    if scenario:
        completed = read(root/'completed.json')
        completed['status'] = 'PASSED'
        (root/'completed.json').write_text(json.dumps(completed)+'\n')


def scenario(root, queries=None, latency=2_000_000):
    root.mkdir()
    plan = {'version': 1, 'seed': 0, 'actions': [{'kind': 'response-loss', 'fault': 'held-original', 'command': COMMAND}]}
    save(root/'scenario.json', plan)
    save(root/'summary.json', {'status': 'PASSED', 'scenarioSHA256': digest(root/'scenario.json'), 'responseLosses': 1})
    cp = {'results': [{'response': RESULT}]}
    save(root/'final-actual.json', cp); save(root/'final-expected.json', cp)
    proof = {'fault': 'held-original', 'outcome': OUTCOME, 'sameIDVerified': True, 'forwardedRequests': 1,
             'upstreamFirstResponse': RESULT, 'unknownLookup': '{"state":"UNKNOWN"}'}
    event = {'step': 0, 'phase': 'transport-observed', 'fault': 'held-original', 'outcome': OUTCOME}
    if latency is not None:
        proof['requestLatencyNanos'] = event['requestLatencyNanos'] = latency
    save(root/'transport-000.json', proof)
    jsonl(root/'events.jsonl', [event])
    queries = queries if queries is not None else [(404, {'state': 'UNKNOWN'}), (200, RESULT)]
    jsonl(root/'http.jsonl', [{'step': 0, 'method': 'GET', 'account': 'bob', 'path': '/v1/results/place/lost',
                             'status': status, 'response': json.dumps(value, separators=(',', ':'))} for status, value in queries])
    reseal(root, True)


def spec(root, entries):
    path = root/'spec.json'
    save(path, {'version': 1, 'inputs': entries})
    return path


class TransportReportTests(unittest.TestCase):
    def test_scenario_json_markdown_reproduction_and_original_unchanged(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve(); source = root/'source'; scenario(source)
            before = {p.name: digest(p) for p in source.iterdir()}
            path = spec(root, [{'name': 'response loss', 'kind': 'recoverycheck', 'bundle': 'source'}])
            first = generate(path, root/'report'); again = generate(path, root/'again')
            self.assertEqual(first, again)
            self.assertTrue(verify(root/'report')['completed'])
            r = first['inputs'][0]
            self.assertEqual(r['counts']['unknownAttempts'], 1)
            self.assertEqual(r['transportGroups'][0]['latency']['p99Nanos'], 2_000_000)
            self.assertEqual(r['attempts'][0]['measurementCertainty'], 'UNKNOWN')
            command = r['unknownCommands'][0]
            self.assertEqual(command['withinRunResolution'], 'UNRESOLVED')
            self.assertEqual(command['subsequentResolution'], 'RESULT_OBSERVED')
            self.assertEqual(command['queries'][0]['resolution'], 'UNRESOLVED')
            self.assertIn('404/UNKNOWN', (root/'report/report.md').read_text())
            self.assertIn('2.000000', (root/'report/report.md').read_text())
            self.assertEqual(before, {p.name: digest(p) for p in source.iterdir()})
            with self.assertRaises(ValueError): generate(path, root/'report')

    def test_404_only_old_timing_is_unresolved_not_zero_or_not_admitted(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve(); scenario(root/'source', [(404, {'state': 'UNKNOWN'})], None)
            path = spec(root, [{'name': 'old', 'kind': 'recoverycheck', 'bundle': 'source'}])
            r = generate(path, root/'report')['inputs'][0]
            self.assertEqual(r['counts']['keysWithoutObservedResult'], 1)
            self.assertIsNone(r['attempts'][0]['requestLatencyNanos'])
            self.assertEqual(r['transportGroups'][0]['latency']['missingCount'], 1)
            self.assertIsNone(r['transportGroups'][0]['latency']['maxNanos'])
            self.assertEqual(r['unknownCommands'][0]['subsequentResolution'], 'UNRESOLVED')
            self.assertIn('미계측', (root/'report/report.md').read_text())

    def test_later_404_does_not_erase_200_and_pending_is_not_completion(self):
        rows = [attempt(KEY, 'MEASURE', 1, OUTCOME, 1)]
        done, _ = observation(200, json.dumps(RESULT), KEY, 'query')
        pending, _ = observation(202, '{"state":"PENDING"}', KEY, 'query')
        missing, _ = observation(404, '{"state":"UNKNOWN"}', KEY, 'query')
        for queries, expected in [([pending, missing], 'UNRESOLVED'), ([done, missing], 'RESULT_OBSERVED')]:
            r = finish({'name': 'one', 'kind': 'loadgen'}, RUN, rows, {}, {KEY: queries}, {}, {})
            self.assertEqual(r['unknownCommands'][0]['subsequentResolution'], expected)
            self.assertEqual(r['attempts'][0]['measurementCertainty'], 'UNKNOWN')
        changed = dict(done, responseSHA256='0'*64)
        with self.assertRaisesRegex(ValueError, 'first response'):
            finish({'name': 'one', 'kind': 'loadgen'}, RUN, rows, {KEY: {'1'*64}}, {KEY: [changed]}, {}, {})

    def test_group_attempts_vs_keys_and_phase_scope_and_namespaces(self):
        other = key(RUN, 'bob', 'cancel', 'lost')
        old = {'class': 'TRANSPORT_FAILURE', 'certainty': 'UNKNOWN'}
        rows = [attempt(KEY, 'WARMUP', 0, old, None), attempt(KEY, 'MEASURE', 1, OUTCOME, 2),
                attempt(KEY, 'MEASURE', 2, OUTCOME, 4), attempt(other, 'MEASURE', 3, OUTCOME, 8)]
        r = finish({'name':'one', 'kind':'loadgen'}, RUN, rows, {}, {}, {}, {})
        self.assertEqual(r['counts']['unknownAttempts'], 4)
        self.assertEqual(r['counts']['unknownLogicalKeys'], 2)
        measured = next(g for g in r['transportGroups'] if g['phase'] == 'MEASURE')
        self.assertEqual((measured['attempts'], measured['logicalKeys']), (3, 2))
        self.assertEqual(measured['latency']['p50Nanos'], 4)
        self.assertIsNone(next(g for g in r['transportGroups'] if g['phase'] == 'WARMUP')['failureStage'])

    def test_bad_diagnostic_identity_status_and_nonfinite_data_rejected(self):
        for edit in [{'failureStage': 'raw private error'}, {'errorType':None}, {'certainty':'NOT_ADMITTED'}]:
            with self.subTest(edit=edit), self.assertRaises((ValueError, TypeError)):
                attempt(KEY, 'MEASURE', 1, dict(OUTCOME, **edit), 1)
        for field, value in [('runId','other'),('accountId','alice'),('kind','CANCEL_ORDER'),('requestId','other'),('commandSeq','0')]:
            with self.subTest(field=field), self.assertRaises(ValueError): observation(200,json.dumps(dict(RESULT,**{field:value})),KEY,'query')
        for status, body in [(404,'{"state":"COMPLETED"}'), (202,'{"state":"UNKNOWN"}'), (500,'{}'),(200,'{"x":NaN}')]:
            with self.assertRaises(ValueError): observation(status,body,KEY,'query')
        for latency in [-1, True, float('nan')]:
            with self.assertRaises(ValueError): attempt(KEY,'MEASURE',1,OUTCOME,latency)

    def test_hash_and_resealed_semantic_tamper_fail_without_publishing(self):
        for fault in ['bytes','event','latency','identity','missing','checkpoint']:
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as temp:
                root = Path(temp).resolve(); source = root/'source'; scenario(source)
                path = spec(root,[{'name':'one','kind':'recoverycheck','bundle':'source'}])
                if fault == 'bytes': (source/'http.jsonl').write_text('{}\n')
                elif fault == 'event': jsonl(source/'events.jsonl', [])
                elif fault == 'latency':
                    proof=read(source/'transport-000.json');proof['requestLatencyNanos']+=1
                    (source/'transport-000.json').write_text(json.dumps(proof))
                elif fault == 'identity':
                    traces=[{'step':0,'method':'GET','account':'bob','path':'/v1/results/place/lost','status':200,'response':json.dumps(dict(RESULT,runId='other'))}]
                    jsonl(source/'http.jsonl',traces)
                elif fault == 'missing': (source/'transport-000.json').unlink()
                else: (source/'final-actual.json').write_text('{}')
                if fault != 'bytes': reseal(source,True)
                with self.assertRaises((ValueError,KeyError)): generate(path,root/'report')
                self.assertFalse((root/'report').exists())

    def test_output_protection_symlinks_specs_and_escape(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp).resolve();source=root/'source';scenario(source)
            entry={'name':'<script>|line\nnext','kind':'recoverycheck','bundle':'source'}
            path=spec(root,[entry]);text=markdown(generate(path,root/'report'))
            self.assertIn('&lt;script&gt;&#124;line<br>next',text)
            self.assertNotIn('<script>',text)
            for out in [source/'report',root/'report']:
                with self.assertRaises(ValueError):generate(path,out)
            (root/'link').symlink_to(source,target_is_directory=True)
            with self.assertRaises(ValueError):generate(path,root/'link/report')
            for entries in [[entry,entry],[dict(entry,case='x')],[dict(entry,unknown=1)]]:
                p=root/'bad.json';p.write_text(json.dumps({'version':1,'inputs':entries}))
                with self.assertRaises(ValueError):spec_inputs(p)
            (root/'bad.json').write_text('{"version":1,"version":1,"inputs":[]}')
            with self.assertRaises(ValueError):spec_inputs(root/'bad.json')

    def test_loadgen_legacy_diagnostic_and_sealed_404_receipt_remain_unknown(self):
        from types import SimpleNamespace
        from hashlib import sha256
        for within_run in (False, True):
            with self.subTest(within_run=within_run), tempfile.TemporaryDirectory() as temp:
                root=Path(temp).resolve();bundle=root/'bundle';data=bundle/'on/loadgen';data.mkdir(parents=True)
                outcome={'class':'TRANSPORT_FAILURE','certainty':'UNKNOWN','httpStatus':0}
                command={'RunID':RUN,'AccountID':'bob','Kind':'PLACE_ORDER','Place':{'clientOrderId':'lost'}}
                plan=[{'index':0,'phase':'MEASURE','command':command}]
                samples=[{'index':0,'outcome':outcome,'startedNanos':10,'finishedNanos':5_000_010}]
                raw_body=json.dumps(RESULT,separators=(',',':'))
                if within_run:
                    plan.append({'index':1,'phase':'MEASURE','command':command})
                    samples.append({'index':1,'outcome':{'class':'BUSINESS','certainty':'COMPLETED','fingerprint':sha256(raw_body.encode()).hexdigest()}})
                save(data/'manifest.json',{'server':{'runId':RUN}})
                jsonl(data/'plan.jsonl',plan);jsonl(data/'samples.jsonl',samples)
                save(data/'preparation.json',{'samples':[]});save(data/'failed.json',{'error':'invalid measurement'})
                measurement={'unknownLogicalCommands':0 if within_run else 1}
                save(data/'summary.json',{'measurementValid':False,'measurement':measurement});save(data/'run.json',{})
                reseal(bundle)
                follow=root/'follow';follow.mkdir()
                receipt={'measuredIndex':0,'account':'bob','kind':'place','id':'lost','measurementCertainty':'UNKNOWN',
                         'httpStatus':404,'response':{'state':'UNKNOWN'},'responseBytes':'{"state":"UNKNOWN"}'}
                save(follow/'result.json',receipt);save(follow/'ready.json',{'runId':RUN});reseal(follow)
                selector={'name':'legacy','kind':'loadgen','bundle':'bundle','case':'on',
                          'followups':[{'bundle':'follow','result':'result.json','ready':'ready.json'}]}
                path=spec(root,[selector])
                def checker(args,**kwargs):
                    self.assertEqual(args[1], '--verify-plan')
                    save(Path(args[-1]),{'rawVerified':True,'planVerified':True,'loadgenComplete':False,'summary':read(data/'summary.json'), 'inputSHA256':{n:digest(data/n) for n in ['manifest.json','plan.jsonl','preparation.json','samples.jsonl','run.json','summary.json']}})
                    return SimpleNamespace(returncode=0,stderr='')
                with patch('transport_report.build_checker'),patch('transport_report.subprocess.run',side_effect=checker) as checked:
                    r=generate(path,root/'report')['inputs'][0]
                    self.assertEqual(checked.call_count,1)
                self.assertEqual(r['originalMeasurement']['unknownLogicalCommands'],measurement['unknownLogicalCommands'])
                self.assertFalse(r['originalMeasurement']['measurementValid'])
                self.assertFalse(r['originalMeasurement']['loadgenComplete'])
                self.assertEqual(r['attempts'][0]['requestLatencyNanos'],5_000_000)
                self.assertIsNone(r['attempts'][0]['errorType'])
                self.assertEqual(r['unknownCommands'][0]['withinRunResolution'],'RESULT_OBSERVED' if within_run else 'UNRESOLVED')
                self.assertEqual(r['unknownCommands'][0]['subsequentResolution'],'UNRESOLVED')
                # A valid sealed receipt for another run must never resolve/link this UNKNOWN.
                (follow/'ready.json').write_text(json.dumps({'runId':'different-run'}));reseal(follow)
                with patch('transport_report.build_checker'),patch('transport_report.subprocess.run',side_effect=checker):
                    with self.assertRaisesRegex(ValueError,'original UNKNOWN'):
                        generate(path,root/'wrong-run')
                self.assertFalse((root/'wrong-run').exists())
