"""Repeat completeness, immutable inputs and cost report failure boundaries."""
import copy
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from common import digest, read, save, seal, verify
from state_scale import analyze, budget, markdown, schedule, validate_observation, values_summary


def stages():
    names = ['recovery.replay', 'recovery.load', 'recovery.restore', 'recovery.confirmSnapshot',
             'capture.copy', 'capture.sort', 'publish.encodeWrite', 'publish.fileSync', 'publish.directorySync']
    return {n: {'count': '1', 'sumNanos': '1000000', 'sumOverflow': False, 'negativeSamples': '0'} for n in names}


class StateScaleTests(unittest.TestCase):
    def fixture(self, root):
        order = schedule([12], 1)
        save(root/'protocol.json', {'version': 1, 'smoke': True, 'sizes': [12], 'repeats': 1,
                                    'order': order, 'runtimeEnvironment': {'GOMAXPROCS': '1'}})
        (root/'statescale').write_bytes(b'not executed')
        (root/'source.tar.gz').write_bytes(b'not extracted')
        save(root/'build.json', {'binarySHA256': digest(root/'statescale'), 'sourceArchiveSHA256': digest(root/'source.tar.gz'), 'go': {'GOVERSION': 'go1.27.3'}})
        (root/'fixtures').mkdir()
        (root/'runs').mkdir()
        for i, row in enumerate(order):
            name = row['kind']+'-12'
            source = root/'fixtures'/name
            if not source.exists():
                source.mkdir()
                files = {}
                for n in ('.lock', 'genesis.bin', 'commands.wal', 'snapshot-00000000000000000012.bin'):
                    (source/n).write_text(n)
                    files[n] = digest(source/n)
                counts = dict(Orders=12, Active=12, Filled=0, Cancelled=0, Trades=0, Results=12)
                if row['kind'] == 'history':counts = dict(Orders=6, Active=0, Filled=3, Cancelled=3, Trades=3, Results=12)
                save(root/'fixtures'/(name+'.json'), {'version': 1, 'kind': row['kind'], 'commands': 12, 'counts': counts, 'files': files})
            f = read(root/'fixtures'/(name+'.json'))
            target = root/'runs'/f'{i:03d}'
            target.mkdir()
            (target/'run').mkdir()
            for filename in f['files']:
                (target/'run'/filename).write_bytes((source/filename).read_bytes())
            save(target/'case.json', row)
            p = {'WallNanos': 2000000, 'Before': {'HeapAlloc': 1024, 'TotalAlloc': 100, 'Mallocs': 1},
                 'After': {'HeapAlloc': 2048, 'TotalAlloc': 200, 'Mallocs': 2, 'NumGC': 1},
                 'AfterGC': {'HeapAlloc': 1800, 'NumGC': 2}, 'PeakRSSBytes': 16*2**20}
            o = {'Version': 1, 'Verified': True, 'Mode': row['mode'], 'Commands': 12,
                 'Publication': 'new' if row['mode'] == 'wal' else 'reconfirm', 'GOMAXPROCS': 1, 'GoVersion': 'go1.27.3',
                 'Recovery': {'SnapshotFile': '' if row['mode'] == 'wal' else 'snapshot.bin',
                              'SnapshotSeq': '0' if row['mode'] == 'wal' else '12',
                              'ReplayedCommands': '12' if row['mode'] == 'wal' else '0', 'Skipped': [], 'Stages': stages()},
                 'SnapshotSHA256': f['files']['snapshot-00000000000000000012.bin'], 'WALSHA256': f['files']['commands.wal'],
                 'Open': copy.deepcopy(p), 'Snapshot': copy.deepcopy(p), 'SnapshotStages': stages()}
            save(target/'result.json', o)
        return order

    def test_full_predeclared_order_and_outliers_are_retained(self):
        rows = schedule([1000, 5000, 10000], 5)
        self.assertEqual(len(rows), 60)
        self.assertEqual(len({tuple(r.values()) for r in rows}), 60)
        for mode in ('wal', 'snapshot'):
            for n in (1000, 5000, 10000):
                for kind in ('active', 'history'):
                    self.assertEqual(sum(r['mode'] == mode and r['commands'] == n and r['kind'] == kind for r in rows), 5)
        s = values_summary([1, 2, 3, 4, 1000])
        self.assertEqual(s, {'count': 5, 'median': 3, 'min': 1, 'max': 1000})
        for values in ([], [float('nan')], [float('inf')]):
            with self.assertRaises(ValueError):values_summary(values)

    def test_report_recomputes_from_readonly_inputs_and_labels_scope(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            self.fixture(root)
            seal(root, True)
            before = digest(root/'manifest.json')
            report = analyze(root)
            self.assertEqual(report['workersVerified'], 4)
            self.assertFalse(report['performanceImprovementClaimed'])
            self.assertEqual(report['rows'][0]['modes']['wal']['snapshotMs']['median'], 2)
            self.assertNotIn('snapshotMs', report['rows'][0]['modes']['snapshot'])
            self.assertIn('누적 high-water', markdown(report))
            self.assertIn('전원 장애', markdown(report))
            verify(root)
            self.assertEqual(before, digest(root/'manifest.json'))

    def test_missing_duplicate_and_mislabelled_cases_fail(self):
        for fault in ('missing', 'order', 'mode', 'counts', 'copy', 'binary'):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                self.fixture(root)
                if fault == 'missing':(root/'runs/000/result.json').unlink()
                if fault == 'order':(root/'runs/000/case.json').write_text((root/'runs/001/case.json').read_text())
                if fault == 'mode':
                    p = root/'runs/000/result.json';data = read(p);data['Publication'] = 'reconfirm';p.write_text(json.dumps(data))
                if fault == 'counts':
                    p = root/'fixtures/history-12.json';data = read(p);data['counts']['Active'] = 1;p.write_text(json.dumps(data))
                if fault == 'copy':(root/'runs/000/run/commands.wal').write_text('changed')
                if fault == 'binary':(root/'statescale').write_text('changed')
                with self.assertRaises((ValueError, FileNotFoundError)):analyze(root)

    def test_failed_recovery_missing_gc_and_changed_runtime_are_not_success(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            order = self.fixture(root)
            o = read(root/'runs/000/result.json');f = read(root/'fixtures/active-12.json')
            for fault in ('verify', 'recovery', 'runtime', 'gc', 'memory'):
                data = copy.deepcopy(o)
                if fault == 'verify':data['Verified'] = False
                if fault == 'recovery':data['Recovery']['ReplayedCommands'] = '0'
                if fault == 'runtime':data['GOMAXPROCS'] = 2
                if fault == 'gc':data['Open']['AfterGC']['NumGC'] = 1
                if fault == 'memory':data['Open']['After']['TotalAlloc'] = 0
                with self.subTest(fault=fault), self.assertRaises(ValueError):
                    validate_observation(data, order[0], f, {'GOMAXPROCS': '1'})

    def test_budget_preserves_free_reserve_and_does_not_delete(self):
        from types import SimpleNamespace
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp);(root/'evidence').write_text('must remain')
            with patch('state_scale.shutil.disk_usage', return_value=SimpleNamespace(free=8*2**30-1)):
                with self.assertRaises(ValueError):budget(root)
            with patch('state_scale.MAX_BYTES', 1):
                with self.assertRaises(ValueError):budget(root)
            self.assertEqual((root/'evidence').read_text(), 'must remain')


if __name__ == '__main__':
    unittest.main()
