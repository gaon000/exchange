"""Retention safety across references, sealed data, races and report feedback."""
import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import artifacts
from common import save, seal


class ArtifactTests(unittest.TestCase):
    def setUp(self):
        self.scratch = tempfile.TemporaryDirectory()
        self.addCleanup(self.scratch.cleanup)
        self.repo = (Path(self.scratch.name)/'repo').resolve()
        self.repo.mkdir()
        subprocess.run(['git', 'init', '-q', str(self.repo)], check=True, capture_output=True)
        self.put('.gitignore', '/var/\n')
        self.policy = {'version': 1, 'keep': [], 'temporary': ['var/*']}

    def put(self, path, value):
        path = self.repo/path
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(value if isinstance(value, str) else json.dumps(value))
        return path

    def report(self, policy=None):
        return artifacts.inspect(self.repo, self.policy if policy is None else policy)

    def units(self, report=None):
        return {u['path']: u for u in (report or self.report())['units']}

    def test_paths_braces_hashes_and_dependency_cycles(self):
        first = self.put('var/derived-first/report.json', {'metric': 123})
        self.put('var/ref-a.txt', 'var/ref-b.txt')
        self.put('var/ref-b.txt', 'var/ref-a.txt')
        self.put('var/path-one.json', {})
        self.put('var/path-two.json', {})
        self.put('var/unreferenced.log', 'temporary output')
        self.put('docs/evidence.md', f'`var/path-{{one,two}}.json`\nrawReportSHA256: {hashlib.sha256(first.read_bytes()).hexdigest()}')
        report = self.report()
        self.assertTrue(report['scanComplete'], report['issues'])
        units = self.units(report)
        for path in ('var/path-one.json', 'var/path-two.json', 'var/derived-first', 'var/ref-a.txt', 'var/ref-b.txt'):
            self.assertEqual(units[path]['disposition'], 'protected')
        self.assertEqual(units['var/unreferenced.log']['disposition'], 'candidate')
        self.assertIn({'source': 'docs/evidence.md', 'via': 'sha256'}, units['var/derived-first']['referencedBy'])
        self.assertTrue(units['var/ref-a.txt']['references'])

    def test_fixed_build_resume_raw_data_archives_failures_and_requests(self):
        self.put('var/current/protocol.json', {'resumeFrom': {'path': str(self.repo/'var/parent')}})
        self.put('var/parent/protocol.json', {'resumeFrom': {'path': 'var/grandparent'}})
        self.put('var/grandparent/note.txt', 'var/frozen/bin/exchange')
        self.put('var/frozen/build.json', {'revision': 'frozen'})
        self.put('var/live/run/genesis.bin', 'fixture')
        self.put('var/interrupted/failed.json', {'error': 'cancelled'})
        self.put('var/archives/unique.tar.gz', 'archive bytes')
        self.put('var/requests/order.json', {'kind': 'PLACE_ORDER'})
        self.put('var/runs/manual/state.txt', 'live')
        self.put('var/ci-failure.log', 'diagnostics: minimum free space is 8GiB')
        units = self.units()
        self.assertTrue(units)
        self.assertTrue(all(u['disposition'] == 'protected' for u in units.values()), units)
        self.assertTrue(any(e['target'] == 'var/grandparent' for e in self.report()['edges']))

    def test_sealed_roots_are_indivisible_except_known_derived_reports(self):
        self.put('var/raw/data.txt', 'measurement')
        self.put('var/raw/run/manifest.json', {'schemaVersion': 1, 'genesisHash': 'storage manifest, not a diagnostic inventory'})
        self.put('var/derived/report.json', {'result': 1})
        self.put('var/derived/report.md', '# report')
        seal(self.repo/'var/raw', True)
        seal(self.repo/'var/derived', True)
        units = self.units()
        self.assertEqual(units['var/raw']['disposition'], 'protected')
        self.assertEqual(units['var/derived']['disposition'], 'candidate')
        self.assertNotIn('var/raw/data.txt', units)
        self.put('var/derived/report.json', {'tampered': True})
        report = self.report()
        self.assertFalse(report['scanComplete'])
        self.assertFalse(any(u['disposition'] == 'candidate' for u in report['units']))

    def test_policy_file_is_checked_before_and_after_inventory(self):
        target = self.put('var/temp.log', 'temporary')
        policy_file = self.put('config/artifact-retention.json', self.policy)
        report = artifacts.inspect(self.repo, self.policy, policy_file)
        self.assertTrue(report['scanComplete'], report['issues'])
        self.assertEqual(self.units(report)['var/temp.log']['disposition'], 'candidate')
        self.put('config/artifact-retention.json', {'version': 1, 'keep': [], 'temporary': []})
        report = artifacts.inspect(self.repo, self.policy, policy_file)
        self.assertFalse(report['scanComplete'])
        self.assertTrue(any('policy changed' in issue for issue in report['issues']))
        self.put('config/artifact-retention.json', self.policy)
        real = artifacts.bounded_metadata
        def change_policy(path):
            result = real(path)
            if path == target:
                policy_file.write_text('{}')
            return result
        with patch.object(artifacts, 'bounded_metadata', side_effect=change_policy):
            report = artifacts.inspect(self.repo, self.policy, policy_file)
        self.assertFalse(report['scanComplete'])
        self.assertFalse(any(u['disposition'] == 'candidate' for u in report['units']))

    def test_reserved_container_that_is_an_experiment_stays_indivisible(self):
        for name in ('artifact-reports', 'diagnostics', 'comparisons', 'ci'):
            with self.subTest(container=name):
                self.put(f'var/{name}/protocol.json', {'version': 1})
                self.put(f'var/{name}/unreferenced.csv', 'measurement data')
                report = self.report()
                self.assertTrue(report['scanComplete'], report['issues'])
                units = self.units(report)
                self.assertEqual(units[f'var/{name}']['disposition'], 'protected')
                self.assertFalse(any(path.startswith(f'var/{name}/') for path in units))

    def test_newest_ci_and_failed_ci_survive_broad_temporary_policy(self):
        for name, status, stamp in [('portable.old', 0, 10), ('portable.new', 0, 20), ('portable.failed', 1, 15), ('native.new', 0, 20)]:
            p = self.put(f'var/ci/{name}/summary.txt', f'mode={name.split(".")[0]} exit={status}\n')
            os.utime(p, ns=(stamp, stamp))
        units = self.units()
        self.assertEqual(units['var/ci/portable.old']['disposition'], 'candidate')
        for name in ('portable.new', 'portable.failed', 'native.new'):
            self.assertEqual(units['var/ci/'+name]['disposition'], 'protected')

    def test_unknown_is_review_and_keep_overrides_temporary(self):
        p = self.put('var/old-large.bin', 'x'*10000)
        os.utime(p, (1, 1))
        policy = {'version': 1, 'keep': [], 'temporary': []}
        self.assertEqual(self.units(self.report(policy))['var/old-large.bin']['disposition'], 'review')
        policy['temporary'] = ['var/*']
        policy['keep'] = ['var/old-*']
        self.assertEqual(self.units(self.report(policy))['var/old-large.bin']['disposition'], 'protected')
        for value in ({}, {'version': 2, 'keep': [], 'temporary': []}, {'version': 1, 'keep': [], 'temporary': ['var/../outside']}):
            with self.assertRaises(ValueError):
                artifacts.policy_value(value)

    def test_symlinks_are_not_followed_and_source_failure_withholds_candidates(self):
        outside = Path(self.scratch.name)/'external.txt'
        outside.write_text('secret outside the workspace')
        self.put('var/temp.log', 'temporary')
        (self.repo/'var/link.txt').symlink_to(outside)
        units = self.units()
        self.assertEqual(units['var/link.txt']['disposition'], 'protected')
        self.assertEqual(units['var/link.txt']['files'], 0)
        (self.repo/'reference.md').symlink_to(outside)
        report = self.report()
        self.assertFalse(report['scanComplete'])
        self.assertFalse(any(u['disposition'] == 'candidate' for u in report['units']))
        self.assertNotIn('secret outside', json.dumps(report))

    def test_unreadable_oversized_or_concurrently_changed_input_withholds_candidates(self):
        self.put('var/temp.log', 'temporary')
        source = self.put('docs/reference.md', 'evidence')
        real = artifacts.bounded_metadata
        def denied(path):
            if path == source:
                raise PermissionError('fixture')
            return real(path)
        with patch.object(artifacts, 'bounded_metadata', side_effect=denied):
            report = self.report()
        self.assertFalse(report['scanComplete'])
        self.assertFalse(any(u['disposition'] == 'candidate' for u in report['units']))
        source.write_text('x'*(artifacts.MAX_TEXT+1))
        self.assertFalse(self.report()['scanComplete'])
        source.write_text('evidence')
        actual_walk = artifacts.walk
        count = 0
        def changed(root):
            nonlocal count
            count += 1
            if count == 2:
                self.put('var/new-during-scan.log', 'new data')
            return actual_walk(root)
        with patch.object(artifacts, 'walk', side_effect=changed):
            report = self.report()
        self.assertFalse(report['scanComplete'])
        self.assertFalse(any(u['disposition'] == 'candidate' for u in report['units']))

    def test_reporting_is_read_only_and_does_not_create_reference_feedback(self):
        original = self.put('var/unreferenced.log', 'temporary')
        before = original.stat()
        output = self.repo/'var/artifact-reports/first'
        report = artifacts.publish(self.repo, self.policy, output)
        self.assertTrue(report['scanComplete'], report['issues'])
        self.assertEqual(original.stat().st_mtime_ns, before.st_mtime_ns)
        self.assertEqual(original.read_text(), 'temporary')
        plan = json.loads((output/'plan.json').read_text())
        self.assertEqual([u['path'] for u in plan['candidates']], ['var/unreferenced.log'])
        self.assertEqual(plan['reportSHA256'], hashlib.sha256((output/'retention-report.json').read_bytes()).hexdigest())
        units = self.units()
        self.assertEqual(units['var/unreferenced.log']['disposition'], 'candidate')
        self.assertEqual(units['var/artifact-reports/first']['disposition'], 'candidate')
        self.put('docs/retained-preview.md', hashlib.sha256((output/'retention-report.json').read_bytes()).hexdigest())
        self.assertEqual(self.units()['var/artifact-reports/first']['disposition'], 'protected')
        with self.assertRaises(ValueError):
            artifacts.publish(self.repo, self.policy, output)
        for path in (self.repo/'var/unreferenced.log/nested', self.repo/'docs/report'):
            with self.assertRaises(ValueError):
                artifacts.publish(self.repo, self.policy, path)
        (self.repo.parent/'outside').mkdir()
        (self.repo/'var/evidence').mkdir()
        traversal = self.repo.parent/'outside'/'..'/self.repo.name/'var/evidence/new'
        with self.assertRaises(ValueError):
            artifacts.publish(self.repo, self.policy, traversal)
        self.put('config/artifact-retention.json', self.policy)
        command = [sys.executable, str(Path(artifacts.__file__).resolve()), '--repo', str(self.repo)]
        result = subprocess.run(command+['--output', str(self.repo/'var/artifact-reports/cli')], capture_output=True, text=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertTrue(json.loads(result.stdout)['scanComplete'])
        refused = subprocess.run(command+['--delete'], capture_output=True, text=True)
        self.assertNotEqual(refused.returncode, 0)
        self.assertEqual(original.read_text(), 'temporary')
        (output/'plan.json').write_text('{}')
        report = self.report()
        self.assertFalse(report['scanComplete'])
        self.assertFalse(any(u['disposition'] == 'candidate' for u in report['units']))

    def test_forged_report_with_extra_metadata_is_scanned(self):
        self.put('var/unreferenced.log', 'temporary')
        out = self.repo/'var/artifact-reports/forged'
        self.put('var/artifact-reports/forged/retention-report.json', {'version': 1, 'kind': 'artifact-retention-preview', 'target': 'var/unreferenced.log'})
        self.put('var/artifact-reports/forged/protocol.json', {'path': 'var/unreferenced.log'})
        units = self.units()
        self.assertEqual(units['var/unreferenced.log']['disposition'], 'protected')
        self.assertEqual(units[str(out.relative_to(self.repo))]['disposition'], 'protected')

    def test_capacity_inode_accounting_and_markdown(self):
        first = self.put('var/one.txt', 'contents')
        os.link(first, self.repo/'var/two.txt')
        report = self.report()
        self.assertEqual(report['capacity']['logicalBytes'], 16)
        self.assertEqual(report['capacity']['allocatedBytesUniqueInodes'], sum(s.st_blocks*512 for p,s in artifacts.walk(self.repo/'var').items() if p.name != 'two.txt'))
        text = artifacts.markdown(report)
        self.assertIn('삭제 후보', text)
        self.assertIn('var/one.txt', text)
        self.assertIn('실제 삭제: 없음', text)


if __name__ == '__main__':
    unittest.main()
