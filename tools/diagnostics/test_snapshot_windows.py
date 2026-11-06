"""Cohort enclosure regressions for final-window response tails."""
import unittest
import tempfile
import json
from pathlib import Path
from snapshot_windows import enclosing, host_rows


class SnapshotWindowTests(unittest.TestCase):
    def test_host_drops_since_boot_average_and_preserves_utc(self):
        with tempfile.TemporaryDirectory() as temp:
            path = Path(temp)/'host.jsonl'
            path.write_text('\n'.join(json.dumps({'utc': str(i), 'line': line})
                            for i, line in enumerate(('KB/t tps MB/s us sy id', '1 2 3 10 5 85', '2 3 4 20 10 70')))+'\n')
            self.assertEqual(host_rows(path), [{'utc': '2', 'busyPercent': 30, 'displayedDiskMBps': 4}])

    def test_includes_next_observation_and_response_tail(self):
        times = [0, .5, 1, 1.5, 2, 2.5]
        self.assertEqual(enclosing(times, .1, 1.9), (0, 4))
        # Scheduled window ends at 2s, but its final response ends at 2.1s.
        self.assertEqual(enclosing(times, .1, 2.1), (0, 5))
        self.assertEqual(enclosing(times, .5, 2), (1, 4))

    def test_rejects_unobserved_cohort_boundaries(self):
        for start, end in ((-1, 1), (0, 3), (2, 1)):
            with self.subTest(start=start, end=end), self.assertRaises(ValueError):
                enclosing([0, 1, 2], start, end)
