"""Portable integrity, recovery verifier and source binding regression checks."""
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch
from analysis import timer
from archive import archive, verify_archive
from build import prepare_overlay
from common import child, decode, digest, read, save, seal, verify
from metrics import process
from recovery import capture, check_supply
from trace_reader import parse


class DiagnosticTests(unittest.TestCase):
    def test_seal_rejects_mutation_extra_file_and_path_escape(self):
        for mutation in ['bytes', 'extra', 'path']:
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                (root/'data').write_text('original')
                seal(root, True)
                self.assertEqual(verify(root)['filesVerified'], 1)
                if mutation == 'bytes':
                    (root/'data').write_text('changed')
                elif mutation == 'extra':
                    (root/'extra').write_text('unrecorded')
                else:
                    data = read(root/'manifest.json')
                    data['files']['../escape'] = data['files'].pop('data')
                    (root/'manifest.json').write_text(json.dumps(data))
                    (root/'completed.json').write_text(json.dumps({'manifestSHA256': digest(root/'manifest.json')}))
                with self.assertRaises(ValueError):
                    verify(root)

    def test_duplicate_keys_symlinks_and_incomplete_are_not_success(self):
        with self.assertRaises(ValueError):
            decode('{"n":1,"n":2}')
        with self.assertRaises(ValueError):
            decode('{"n":NaN}')
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            (root/'link').symlink_to(root.parent)
            with self.assertRaises(ValueError):
                child(root, 'link/elsewhere')
            (root/'link').unlink()
            save(root/'failed.json', {'error': 'interrupted'})
            seal(root, False)
            with self.assertRaises(ValueError):
                verify(root)
            self.assertFalse(verify(root, allow_failed=True)['completed'])

    def test_rehashed_timer_semantic_mutation_is_rejected(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            save(root/'run.json', {'durationNanos': 10000000, 'count': 1, 'workers': 0,
                                  'requestsPerSecond': 100, 'traceEnabled': False, 'traceBytes': 0})
            row = {'index': 0, 'scheduledNanos': 0, 'waitStartNanos': -1,
                   'waitEndNanos': 1, 'workerNanos': -1}
            (root/'samples.jsonl').write_text(json.dumps(row)+'\n')
            self.assertEqual(timer(root)['scheduler']['count'], 1)
            row['failure'] = 'scheduler'  # Correct bytes, contradictory decision.
            (root/'samples.jsonl').write_text(json.dumps(row)+'\n')
            seal(root, True)
            verify(root)
            with self.assertRaises(ValueError):
                timer(root)

    def test_archive_verifies_before_opt_in_pruning_and_detects_damage(self):
        for prune in [False, True]:
            with self.subTest(prune=prune), tempfile.TemporaryDirectory() as temp:
                case = Path(temp)
                (case/'run').mkdir()
                save(case/'verification.json', {'status': 'PASSED'})
                for label in ['before-restart', 'after-restart', 'after-retries']:
                    (case/(label+'.jsonl')).write_text('retained state\n')
                (case/'run/snapshot-1.bin').write_bytes(b'opaque test snapshot')
                archive(case, prune=prune)
                self.assertEqual(verify_archive(case)['membersVerified'], 4)
                self.assertEqual((case/'run/snapshot-1.bin').exists(), not prune)
                with (case/'state-artifacts.tar.gz').open('r+b') as file:
                    file.write(b'bad')
                with self.assertRaises(ValueError):
                    verify_archive(case)

    def test_overlay_bound_to_source_and_modes_do_not_change_repository(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            mapping = read(prepare_overlay(root, 'trace'))['Replace']
            text = (root/'overlay/exchange-main.go').read_text()
            self.assertNotIn('WriteIORecords', text)
            self.assertNotIn('internal/durable', text)
            self.assertEqual(len(mapping), 4)
        with tempfile.TemporaryDirectory() as temp:
            with patch('build.digest', return_value='modified'):
                with self.assertRaisesRegex(ValueError, 'overlay source changed'):
                    prepare_overlay(Path(temp), 'io')

    def test_archive_rejects_parent_symlink_before_writing_or_pruning(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            case, outside = root/'case', root/'outside'
            case.mkdir()
            outside.mkdir()
            source = outside/'snapshot-1.bin'
            source.write_bytes(b'must survive')
            (case/'run').symlink_to(outside, target_is_directory=True)
            save(case/'verification.json', {'status': 'PASSED'})
            with self.assertRaisesRegex(ValueError, 'symlink'):
                archive(case, prune=True)
            self.assertEqual(source.read_bytes(), b'must survive')
            self.assertFalse((case/'state-artifacts.tar.gz').exists())

    def test_unavailable_os_metrics_are_not_zero_cpu(self):
        value = {'osAvailable': False, 'gcPauses': [], 'goGcCycles': '0',
                 'goGcCpuSeconds': 0, 'peakRssBytes': '0', 'goLiveHeapBytes': '0'}
        result = process({'process': value}, {'process': value})
        self.assertFalse(result['osAvailable'])
        self.assertIsNone(result['cpuMs'])
        self.assertIsNone(result['peakRssMiB'])

    def test_invalid_trace_is_rejected_and_reader_child_finishes(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp)/'bad.trace'
            path.write_bytes(b'not a Go trace')
            with self.assertRaises(ValueError):
                parse(path)

    def test_supply_and_original_result_verifier_reject_corruption(self):
        config = {'accounts': [{'baseLots': '10', 'quoteAtoms': '100'}]}
        values = [{'BTC': {'availableLots': '10', 'reservedLots': '0'},
                   'USDT': {'availableAtoms': '100', 'reservedAtoms': '0'}}]
        check_supply(values, config)
        values[0]['BTC']['availableLots'] = '11'
        with self.assertRaises(ValueError):
            check_supply(values, config)
        class CorruptClient:
            run_id = 'run'
            def request(self, account, url):
                return b'{}', {'runId': 'run', 'asOfSeq': '2'}
        with tempfile.TemporaryDirectory() as temp:
            with self.assertRaisesRegex(ValueError, 'state changed'):
                capture(CorruptClient(), Path(temp), 'bad', {}, 1, {'accounts': [{'accountId': 'alice'}]})


if __name__ == '__main__':
    unittest.main()
