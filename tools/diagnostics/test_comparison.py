"""Comparison validity, independent repeats and fail-closed report publication."""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from comparison import Evidence, compare, compare_group, markdown, spec_inputs
from common import read, save, seal, verify


def record(identity, value=10):
    return {'input': {'bundle': 'retained'}, 'identity': identity, 'format': 'p6-experiment',
            'criterionPass': True, 'series': 'series-one', 'status': 'verified', 'measurementValid': True, 'reasons': [],
            'conditions': {'rate': 100, 'environment': {'hardware': {'cpu': 'fixture'}, 'storage': {'type': 'apfs'}}},
            'build': {'binary': 'one'}, 'metrics': {'requestP99Ms': value}, 'windows': []}


def group(a=None, b=None):
    return compare_group('test', a or [record('a'+str(i), 10+i) for i in range(3)],
                         b or [record('b'+str(i), 5+i) for i in range(3)], 3,
                         {'baseline': True, 'candidate': True})


class FakeEvidence:
    def __init__(self, scratch, criteria):
        pass

    def judge_side(self, entries):
        return {'groups': [{'qualified': True}]}

    def load(self, entry, judged):
        value = record(entry['bundle'])
        value['input'] = entry
        return value


class ComparisonTests(unittest.TestCase):
    def test_independent_qualified_repeats_and_descriptive_delta(self):
        result = group()
        self.assertTrue(result['judgement']['eligibleForReview'])
        self.assertFalse(result['judgement']['improvementClaimed'])
        metric = result['metrics']['requestP99Ms']
        self.assertEqual(metric['baseline'], {'observedRuns': 3, 'median': 11, 'min': 10, 'max': 12})
        self.assertEqual(metric['delta'], -5)
        self.assertAlmostEqual(metric['percentChange'], -500/11)
        self.assertIn('11 [10~12; 3]', markdown({'comparisons': [result]}))

    def test_invalid_missing_and_error_observations_are_not_zero_or_dropped(self):
        a = [record('a0', 10), record('a1', 100), record('a2', 1000)]
        a[1]['measurementValid'] = False
        result = group(a=a)
        self.assertIn('baseline:INVALID_MEASUREMENT', result['judgement']['blockers'])
        self.assertEqual(result['metrics']['requestP99Ms']['baseline']['median'], 100)
        a[0]['metrics'].clear()
        a[2]['status'] = 'error'
        a[2]['reasons'] = ['EVIDENCE: bad data']
        result = group(a=a)
        self.assertEqual(len(result['baseline']), 3)
        self.assertEqual(result['metrics']['requestP99Ms']['baseline']['observedRuns'], 1)
        self.assertIsNone(result['metrics']['serverPeakRssMiB']['delta'])
        self.assertIn('EVIDENCE: bad data', markdown({'comparisons': [result]}))
        for r in a:r.pop('conditions', None)
        b = [copy.deepcopy(r) for r in a]
        unknown = group(a, b)
        self.assertFalse(unknown['conditions']['observedFieldsMatch'])
        self.assertIn('| 조건 자료 | 알 수 없음 | 알 수 없음 |', markdown({'comparisons': [unknown]}))

    def test_duplicates_condition_changes_mixed_builds_and_diagnostics_block(self):
        for fault in ('duplicate', 'shared', 'conditions', 'mixed', 'mixed-series', 'environment', 'diagnostic'):
            with self.subTest(fault=fault):
                a, b = [record('a'+str(i)) for i in range(3)], [record('b'+str(i)) for i in range(3)]
                if fault == 'duplicate':a[1]['identity'] = a[0]['identity']
                if fault == 'shared':b[0]['identity'] = a[0]['identity']
                if fault == 'conditions':
                    for r in b:r['conditions']['rate'] = 200
                if fault == 'mixed':a[1]['build']['binary'] = 'other'
                if fault == 'mixed-series':a[1]['series'] = 'another-series'
                if fault == 'environment':
                    for r in a:r['conditions']['environment']['hardware'] = None
                if fault == 'diagnostic':
                    for r in a:r['format'] = 'timer-diagnostic'
                result = group(a, b)
                self.assertFalse(result['judgement']['eligibleForReview'])
                self.assertTrue(result['judgement']['blockers'])
        result = group(a=[record('a')])
        self.assertIn('baseline:INSUFFICIENT_REPEATS', result['judgement']['blockers'])
        zero = group(a=[record('zero', 0)])
        self.assertIsNone(zero['metrics']['requestP99Ms']['percentChange'])

    def test_markdown_escapes_cells_and_title(self):
        result = group()
        result['name'] = '<tag>|line\nnext'
        result['baseline'][0]['reasons'] = ['bad|cell\n<script>']
        text = markdown({'comparisons': [result]})
        self.assertIn('&lt;tag&gt;&#124;line<br>next', text)
        self.assertIn('bad&#124;cell<br>&lt;script&gt;', text)
        self.assertNotIn('<script>', text)

    def test_spec_validation_and_relative_paths(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            spec = root/'spec.json'
            payload = {'version': 1, 'comparisons': [{'name': 'one', 'baseline': [{'bundle': 'input', 'case': 'ok'}],
                                                     'candidate': [{'bundle': 'other', 'case': 'ok'}]}]}
            save(spec, payload)
            data, _ = spec_inputs(spec)
            self.assertEqual(data['comparisons'][0]['baseline'][0]['bundle'], str((root/'input').resolve()))
            for case in ('../elsewhere', '..', ''):
                payload['comparisons'][0]['baseline'][0]['case'] = case
                spec.write_text(json.dumps(payload))
                with self.assertRaises(ValueError):spec_inputs(spec)

    def test_output_integrity_reuse_symlink_and_partial_write(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root/'input').mkdir()
            (root/'input/raw').write_text('must stay')
            spec = root/'spec.json'
            save(spec, {'version': 1, 'comparisons': [{'name': 'one', 'baseline': [{'bundle': 'input'}],
                                                     'candidate': [{'bundle': 'other'}]}]})
            out = root/'report'
            with patch('comparison.Evidence', FakeEvidence):
                report = compare(spec, out)
            self.assertEqual(verify(out)['filesVerified'], 2)
            self.assertEqual(read(out/'report.json'), report)
            self.assertEqual((out/'report.md').read_text(), markdown(report))
            self.assertEqual((root/'input/raw').read_text(), 'must stay')
            for target in (out, root/'input/forbidden'):
                with self.assertRaises(ValueError):compare(spec, target)
            alias = root/'alias'
            alias.symlink_to(root/'input', target_is_directory=True)
            with self.assertRaises(ValueError):compare(spec, alias/'forbidden')
            broken = root/'broken'
            broken.symlink_to(root/'not-created', target_is_directory=True)
            with self.assertRaises(ValueError):compare(spec, broken)
            self.assertFalse((root/'not-created').exists())
            partial = root/'partial'
            with patch('comparison.Evidence', FakeEvidence), patch('comparison.markdown', side_effect=OSError('write error')):
                with self.assertRaises(OSError):compare(spec, partial)
            self.assertTrue((partial/'failed.json').is_file())
            self.assertFalse((partial/'completed.json').exists())

    def test_missing_or_partial_bundle_is_retained_as_evidence_error(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            adapter = object.__new__(Evidence)
            adapter.cache, adapter.inventories = {}, {}
            value = adapter.load({'bundle': str(root/'missing'), 'case': 'one'}, {})
            self.assertEqual(value['status'], 'error')
            self.assertTrue(value['reasons'][0].startswith('EVIDENCE:'))
            save(root/'failed.json', {'error': 'interrupted'})
            seal(root, False)
            value = adapter.load({'bundle': str(root), 'case': 'one'}, {})
            self.assertEqual(value['status'], 'error')
            self.assertFalse(value['measurementValid'])
            self.assertIn('incomplete', value['reasons'][0])


if __name__ == '__main__':
    unittest.main()
