"""Predeclared quiet and resource gates; only owned descendants are counted."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
import signal
import subprocess
from unittest.mock import patch
from baseline import BINARIES, freeze, order, owned_processes, quiet_stats, resume_state, run, stop, validate_build, verify_dependencies, within_budget
from common import digest, read, save, seal, verify


class BaselineTests(unittest.TestCase):
    def test_resume_preserves_prefix_and_rejects_changed_rates_or_partial_case(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()/'old'
            root.mkdir()
            selected = order('reference', 10000, 1000)
            hashes, env = {'exchange': 'test'}, {'GOMAXPROCS': '11'}
            save(root/'protocol.json', {'preset': 'reference', 'order': selected, 'binarySHA256': hashes, 'runtimeEnvironment': env})
            completed = [{'case': x[0], 'measurementValid': True, 'exit': 0} for x in selected[:2]]
            save(root/'failed.json', {'error': 'host did not meet predeclared quiet gate', 'completedCases': completed})
            for name, _, _ in selected[:2]:
                (root/name/'loadgen').mkdir(parents=True)
                save(root/name/'loadgen/summary.json', {'measurementValid': True})
            seal(root, False)
            results, paths = resume_state(root, selected, hashes, env)
            self.assertEqual(results, completed)
            self.assertEqual(paths, {x[0]: root/x[0] for x in selected[:2]})
            with self.assertRaisesRegex(ValueError, 'conditions'):resume_state(root, order('reference', 20000, 1000), hashes, env)
            child = Path(temp)/'resumed'
            child.mkdir()
            save(child/'protocol.json', {**read(root/'protocol.json'), 'resumeFrom': {'path': str(root), 'manifestSHA256': digest(root/'manifest.json')}})
            self.assertEqual(verify_dependencies(child)['parentCasesVerified'], 2)
            save(child/'failed.json', {'error': 'host did not meet predeclared quiet gate', 'completedCases': completed})
            seal(child, False)
            self.assertEqual(resume_state(child, selected, hashes, env)[1], paths)
            # A workload that actually started cannot be silently substituted.
            (root/selected[2][0]).mkdir()
            save(root/selected[2][0]/'partial.json', {})
            (root/'manifest.json').unlink()
            seal(root, False)
            with self.assertRaisesRegex(ValueError, 'partially executed'):resume_state(root, selected, hashes, env)
            with self.assertRaisesRegex(ValueError, 'manifest changed'):verify_dependencies(child)

    def test_reference_rate_overrides_match_fixed_plan_bounds(self):
        cases = order('reference', 10000, 1000)
        self.assertEqual([rate for _, kind, rate in cases if kind == 'normal'], [10000]*3)
        self.assertEqual([rate for _, kind, rate in cases if kind == 'snapshot'], [1000]*3)
        self.assertEqual(order('reference', 80000, 1000)[0][2]*25, 2000000)
        self.assertEqual(order('reference', snapshot_rate=1000)[0][2], 29000)
        self.assertEqual([c[0] for c in order('reference', 10000, 1000, 'normal')], ['normal-1', 'normal-2', 'normal-3'])
        self.assertEqual(len(order('reference', 10000, 1000, 'snapshot')), 3)
        for rate in (0, -1000, 999, 1500, 81000, True, 1000.0):
            with self.subTest(rate=rate), self.assertRaises(ValueError):order('reference', rate, 1000)
        with self.assertRaisesRegex(ValueError, 'reference preset'):order('smoke', 1000, 1000)
        with tempfile.TemporaryDirectory() as temp, patch('baseline.validate_build') as build:
            output = Path(temp)/'output'
            with self.assertRaises(ValueError):run(output, Path(temp), 'reference', 1500, 1000)
            build.assert_not_called()
            self.assertFalse(output.exists())

    def test_reference_never_starts_workload_when_quiet_gate_fails(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            frozen = root/'build'
            (frozen/'bin').mkdir(parents=True)
            for name in BINARIES:(frozen/'bin'/name).write_text('not executed')
            from contextlib import nullcontext
            with patch('baseline.platform.system', return_value='Darwin'), patch('baseline.platform.machine', return_value='arm64'), \
                 patch('baseline.validate_build', return_value={'revision': 'fixture', 'sha256': {}}), \
                 patch('baseline.subprocess.run') as probe, patch('baseline.subprocess.Popen') as workload, \
                 patch('baseline.power_and_thermal'), patch('baseline.Samplers', return_value=nullcontext()), \
                 patch('baseline.quiet_stats', return_value={'accepted': False}) as quiet, patch('baseline.time.sleep'):
                probe.return_value.returncode = 0
                probe.return_value.stdout = '{}'
                with self.assertRaisesRegex(ValueError, 'quiet gate'):
                    run(root/'output', frozen, 'reference')
            self.assertEqual(quiet.call_count, 3)
            workload.assert_not_called()
            self.assertTrue(json.loads((root/'output/protocol.json').read_text())['quietGate']['enforced'])
            self.assertFalse(verify(root/'output', allow_failed=True)['completed'])

    def test_dirty_source_rejected_before_build_directory_creation(self):
        with tempfile.TemporaryDirectory() as temp:
            output = Path(temp)/'build'
            with patch('baseline.subprocess.check_output', return_value=' M tracked-file'):
                with self.assertRaisesRegex(ValueError, 'commit source'):freeze(output)
            self.assertFalse(output.exists())

    def test_failed_collection_preserves_evidence_and_signal_handlers(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            frozen = root/'build'
            (frozen/'bin').mkdir(parents=True)
            for name in BINARIES:(frozen/'bin'/name).write_text('not executed')
            before = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
            with patch('baseline.platform.system', return_value='Darwin'), patch('baseline.platform.machine', return_value='arm64'), \
                 patch('baseline.validate_build', return_value={'revision': 'fixture', 'sha256': {}}), \
                 patch('baseline.subprocess.run') as probe, patch('baseline.power_and_thermal', side_effect=ValueError('power gate failed')):
                probe.return_value.returncode = 0
                probe.return_value.stdout = '{}'
                with self.assertRaisesRegex(ValueError, 'power gate'):
                    run(root/'output', frozen, 'smoke')
            self.assertFalse(verify(root/'output', allow_failed=True)['completed'])
            self.assertTrue((root/'output/failed.json').is_file())
            self.assertEqual(before, {sig: signal.getsignal(sig) for sig in before})

    def test_forced_owned_child_termination_is_not_silent_success(self):
        from unittest.mock import Mock
        target = Mock()
        target.poll.return_value = None
        target.wait.side_effect = [subprocess.TimeoutExpired('owned', 30), 0]
        with self.assertRaisesRegex(RuntimeError, 'forced termination'):stop(target)
        target.terminate.assert_called_once()
        target.kill.assert_called_once()

    def test_owned_tree_excludes_unrelated_processes(self):
        rows = '10 1 5 experiment\n20 10 7 server\n30 20 8 helper\n99 1 99999 unrelated\n'
        result = owned_processes(rows, 10)
        self.assertEqual({r['pid'] for r in result}, {10, 20, 30})
        self.assertEqual(sum(r['rssBytes'] for r in result), 20*1024)
        self.assertTrue(within_budget(8*2**30, 4*2**30, result))
        self.assertFalse(within_budget(8*2**30-1, 0, result))
        self.assertFalse(within_budget(9*2**30, 4*2**30+1, result))
        self.assertFalse(within_budget(9*2**30, 0, [{'rssBytes': 2*2**30+1}]))
        self.assertFalse(within_budget(9*2**30, 0, [{'rssBytes': 2*2**30}, {'rssBytes': 2*2**30}]))

    def test_quiet_gate_rejects_peak_and_incomplete_observation(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp)/'host.jsonl'
            def write(idle):
                values = ['cpu', 'us sy id', '10 10 80']+[f'1 1 {x}' for x in idle]
                path.write_text(''.join(json.dumps({'line': v})+'\n' for v in values))
            write([95]*10)
            self.assertTrue(quiet_stats(path, 10)['accepted'])
            self.assertFalse(quiet_stats(path, 11)['accepted'])
            write([95]*9+[60])
            self.assertFalse(quiet_stats(path, 10)['accepted'])
            write([80]*10)
            self.assertFalse(quiet_stats(path, 10)['accepted'])

    def test_frozen_build_rejects_mutation_or_dirty_metadata(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root/'bin').mkdir()
            metadata = 'binary: go1.27.3\n\tbuild\tvcs.revision=abc\n\tbuild\tvcs.modified=false\n'
            hashes = {}
            for name in BINARIES:
                (root/'bin'/name).write_bytes(name.encode())
                hashes[name] = hashlib.sha256(name.encode()).hexdigest()
            save(root/'build.json', {'revision': 'abc', 'sha256': hashes,
                                     'goBuild': {name: metadata for name in BINARIES}})
            with patch('baseline.subprocess.check_output', return_value=metadata):
                self.assertEqual(validate_build(root)['revision'], 'abc')
            with patch('baseline.subprocess.check_output', return_value=metadata.replace('modified=false', 'modified=true')):
                with self.assertRaisesRegex(ValueError, 'metadata mismatch'):validate_build(root)
            (root/'bin/exchange').write_bytes(b'changed')
            with self.assertRaisesRegex(ValueError, 'binary changed'):validate_build(root)

    def test_reference_order_keeps_three_independent_cases_per_condition(self):
        cases = order('reference')
        self.assertEqual(len(cases), 6)
        self.assertEqual(len({name for name, _, _ in cases}), 6)
        self.assertEqual([rate for _, kind, rate in cases if kind == 'normal'], [29000]*3)
        self.assertEqual([rate for _, kind, rate in cases if kind == 'snapshot'], [3000]*3)
        self.assertEqual(len(order('smoke')), 2)


if __name__ == '__main__':
    unittest.main()
