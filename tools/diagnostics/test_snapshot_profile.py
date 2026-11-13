"""Profile scheduling, units and preserving uninstrumented source evidence."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from common import save
from snapshot_profile import parse_top, prepare_output, schedule, summarize


class SnapshotProfileTests(unittest.TestCase):
    def test_low_space_refuses_before_creating_output(self):
        from types import SimpleNamespace
        required = 8*2**30+128*2**20
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp).resolve()
            source = root/'original'
            source.mkdir()
            (source/'evidence').write_text('preserve')
            output = root/'new'
            with patch('snapshot_profile.shutil.disk_usage', return_value=SimpleNamespace(free=required-1)):
                with self.assertRaises(ValueError):prepare_output(output, [source])
            self.assertFalse(output.exists())
            self.assertEqual((source/'evidence').read_text(), 'preserve')
            with patch('snapshot_profile.shutil.disk_usage', return_value=SimpleNamespace(free=required)):
                self.assertEqual(prepare_output(output, [source]), output)
            self.assertTrue(output.is_dir())

    def test_predeclared_counts_and_separate_profilers(self):
        rows = schedule()
        self.assertEqual(len(rows), 190)
        self.assertEqual(len({tuple(r.values()) for r in rows}), 190)
        for kind in ('active', 'history'):
            for phase in ('wal-open', 'snapshot-open', 'create'):
                for profiler in ('cpu', 'alloc'):
                    count = 5 if profiler == 'alloc' else (40 if phase == 'create' else 20)
                    self.assertEqual(sum(r['kind'] == kind and r['phase'] == phase and r['profiler'] == profiler for r in rows), count)
        self.assertEqual(len(schedule(True)), 12)

    def test_fixed_units_and_flat_cumulative_remain_distinct(self):
        text = '''Showing nodes accounting for 20000000ns, 100% of 20000000ns total
      flat  flat%   sum%        cum   cum%
10000000ns 50.00% 50.00% 20000000ns 100% internal.encode
10000000ns 50.00% 100% 10000000ns 50% runtime.alloc
         0     0%   100% 20000000ns 100% internal.caller
'''
        r = parse_top(text)
        self.assertEqual(r['total'], 20000000)
        self.assertEqual(r['functions'][0]['flat'], 10000000)
        self.assertEqual(r['functions'][0]['cumulative'], 20000000)
        self.assertEqual(r['functions'][2]['flat'], 0)
        self.assertEqual(r['functions'][2]['cumulative'], 20000000)
        self.assertEqual(parse_top('Showing nodes accounting for 1048576B, 100% of 1048576B total')['total'], 1048576)
        self.assertEqual(parse_top('no samples')['functions'], [])

    def test_changed_schedule_fails_before_pprof_or_output(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            save(root/'protocol.json', {'version': 1, 'smoke': True, 'order': []})
            save(root/'build.json', {})
            output = root/'new'
            with patch('snapshot_profile.pprof') as execute:
                with self.assertRaises(ValueError):summarize(root, output, {})
                execute.assert_not_called()
            self.assertFalse(output.exists())


if __name__ == '__main__':unittest.main()
