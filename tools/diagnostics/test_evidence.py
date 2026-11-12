"""Package dependency closure, isolated restoration and hostile archive rejection."""
import copy
import hashlib
import io
import json
from pathlib import Path
import shutil
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from baseline import BINARIES, order
from common import digest, read, save, seal
from evidence import Dependencies, Restored, check_index, create, fresh_output, inspect_payload, reproduce, restore, tree


class EvidenceTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.repo = self.root/'repo'
        self.repo.mkdir()
        # Unit tests exercise small fixtures independently of host disk capacity.
        self.budget = patch('evidence.capacity')
        self.budget.start()
        self.addCleanup(self.budget.stop)

    def put(self, name, value):
        path = self.repo/name
        path.parent.mkdir(parents=True, exist_ok=True)
        if isinstance(value, dict):
            save(path, value)
        else:
            path.write_text(value)
        return path

    def fixture(self):
        self.put('var/timer/protocol.json', {'cases': [{'name': 'one'}]})
        self.put('var/timer/bin/timer', 'retained bytes; never executed')
        self.put('var/timer/one/run.json', {'durationNanos': 10_000_000, 'count': 1, 'workers': 0,
                                         'requestsPerSecond': 100, 'traceEnabled': False, 'traceBytes': 0})
        self.put('var/timer/one/samples.jsonl', json.dumps({'index': 0, 'scheduledNanos': 0,
                  'waitStartNanos': 0, 'waitEndNanos': 1000, 'workerNanos': -1, 'failure': ''})+'\n')
        seal(self.repo/'var/timer', True)
        self.put('config/criteria.json', {'version': 1, 'configFile': 'config.json', 'workloadFile': 'workload.json',
                                         'minimumRepeats': 3, 'windowNanos': 5_000_000_000, 'p99LimitNanos': 10_000_000})
        self.put('config/config.json', {})
        self.put('config/workload.json', {})
        spec = self.put('config/spec.json', {'version': 1, 'criteria': 'criteria.json', 'comparisons': [
            {'name': 'retained timer', 'baseline': [{'bundle': '../var/timer', 'case': 'one'}],
             'candidate': [{'bundle': '../var/timer', 'case': 'one'}]}]})
        return spec

    def package(self):
        spec = self.fixture()
        package = self.root/'package'
        index = create(self.repo, [], [spec], package)
        return package, index

    def test_removed_original_restores_same_bytes_and_reproduces_reports(self):
        package, index = self.package()
        before = tree(self.repo)
        result = restore(package, self.root/'restored')
        self.assertEqual(result['entries'], len(index['entries']))
        self.assertEqual(tree(self.repo), before)
        self.repo.rename(self.root/'unavailable-original')
        restored = Restored(self.root/'restored')
        self.assertEqual(digest(restored.resolve(self.repo/'config/spec.json')), index['entries']['config/spec.json']['sha256'])
        # Suppress only compilation; real comparison loading/semantic checks,
        # resolver, sealing and Markdown still run on the restored timer data.
        with patch('comparison.go'):
            result = reproduce(self.root/'restored', self.root/'reports')
        self.assertEqual(result['status'], 'PASSED')
        self.assertFalse(result['originalFallbackAllowed'])
        self.assertFalse(result['retainedBinariesExecuted'])
        self.assertEqual(len(result['analyses']), 1)
        report = read(self.root/'reports/comparison-0/report.json')['comparisons'][0]
        self.assertEqual(report['baseline'][0]['status'], 'verified')
        self.assertFalse(report['judgement']['eligibleForReview'])
        self.assertTrue((self.root/'reports/comparison-0/report.md').is_file())
        restored.unchanged()

    def test_parent_chain_and_fixed_producer_are_included_without_execution(self):
        selected = order('reference', 1000, 1000)
        build = self.repo/'var/frozen'
        metadata = 'binary: go1.27.3\n\tbuild\tvcs.revision=abc\n\tbuild\tvcs.modified=false\n'
        hashes = {name: digest(self.put('var/frozen/bin/'+name, name)) for name in BINARIES}
        record = {'revision': 'abc', 'sha256': hashes, 'goBuild': dict.fromkeys(BINARIES, metadata),
                  'argv': ['go', 'build', '-o', str(build/'bin'), './cmd/exchange']}
        self.put('var/frozen/build.json', record)
        protocol = {'preset': 'reference', 'order': selected, 'binarySHA256': hashes, 'runtimeEnvironment': {'GOGC': '100'}}
        completed = [{'case': selected[0][0], 'measurementValid': True, 'exit': 0}]
        parent = None
        for name in ('old', 'middle', 'latest'):
            root = self.repo/'var'/name
            value = {**protocol}
            if parent:
                value['resumeFrom'] = {'path': str(parent), 'manifestSHA256': digest(parent/'manifest.json')}
            self.put(f'var/{name}/protocol.json', value)
            self.put(f'var/{name}/producer-build.json', record)
            if parent is None:
                self.put(f'var/{name}/{selected[0][0]}/loadgen/summary.json', {'measurementValid': True})
            self.put(f'var/{name}/failed.json', {'error': 'host did not meet predeclared quiet gate', 'completedCases': completed})
            seal(root, False)
            parent = root
        package = self.root/'package'
        with patch('baseline.subprocess.check_output', return_value=metadata) as header:
            index = create(self.repo, [parent], [], package)
            self.assertEqual(len(index['units']), 4)
            restore(package, self.root/'restored')
            shutil.rmtree(self.repo)  # Only this test's owned synthetic inputs.
            result = reproduce(self.root/'restored', self.root/'reports')
        self.assertEqual(result['unitsVerified'], 4)
        verification = read(self.root/'reports/verification.json')['units']
        self.assertEqual([v['dependencies']['parentCasesVerified'] for v in verification if v['kind'] == 'bundle'], [1, 1, 0])
        self.assertEqual(header.call_count, 10)
        self.assertTrue(all(call.args[0][1:3] == ['version', '-m'] for call in header.call_args_list))

    def test_missing_dependencies_mutations_links_and_wrong_parent_hash_fail_closed(self):
        self.fixture()
        deps = Dependencies(self.repo)
        deps.add(self.repo/'var/timer', 'bundle')
        self.put('var/timer/unlisted', 'added during packaging')
        with self.assertRaisesRegex(ValueError, 'source changed'):deps.unchanged()
        self.put('var/linked/protocol.json', {'resumeFrom': {'path': str(self.repo/'missing'), 'manifestSHA256': 'a'*64}})
        with self.assertRaisesRegex(ValueError, 'missing dependency'):
            Dependencies(self.repo).add(self.repo/'var/linked', 'bundle')
        (self.repo/'alias').symlink_to(self.repo/'var/timer', target_is_directory=True)
        with self.assertRaisesRegex(ValueError, 'linked dependency'):Dependencies(self.repo).source('alias')
        self.put('var/linked2/protocol.json', {'resumeFrom': {'path': str(self.repo/'var/timer'), 'manifestSHA256': 'a'*64}})
        with self.assertRaisesRegex(ValueError, 'parent hash'):
            Dependencies(self.repo).add(self.repo/'var/linked2', 'bundle')
        # An absolute path with parent components must terminate and normalize.
        self.assertEqual(Dependencies(self.repo).source(self.repo/'../repo/config/spec.json'), self.repo/'config/spec.json')

    def test_no_fallback_existing_output_input_writes_or_restore_mutation(self):
        package, _ = self.package()
        restore(package, self.root/'restored')
        restored = Restored(self.root/'restored')
        original_only = self.put('config/not-packaged.json', {})
        with self.assertRaisesRegex(ValueError, 'fallback forbidden'):restored.resolve(original_only)
        with self.assertRaisesRegex(ValueError, 'outside restored'):restored.resolve(self.root/'outside')
        for target in (self.root/'restored', self.repo/'var/timer/extra', self.root/'restored/extra'):
            with self.assertRaises(ValueError):fresh_output(target)
        with self.assertRaises(ValueError):restore(package, self.root/'restored')
        with self.assertRaises(ValueError):create(self.repo, [self.repo/'var/timer'], [], package)
        restored.resolve('var/timer/one/run.json').write_text('{}')
        with self.assertRaisesRegex(ValueError, 'restored bytes changed'):Restored(self.root/'restored')

    def test_semantic_failure_never_emits_reproduction_completion(self):
        spec = self.fixture()
        bundle = self.repo/'var/timer'
        sample = bundle/'one/samples.jsonl'
        sample.write_text(sample.read_text().replace('"workerNanos": -1', '"workerNanos": 0'))
        (bundle/'manifest.json').unlink()
        (bundle/'completed.json').unlink()
        seal(bundle, True)
        create(self.repo, [], [spec], self.root/'package')
        restore(self.root/'package', self.root/'restored')
        with self.assertRaisesRegex(ValueError, 'unexpected worker'):
            reproduce(self.root/'restored', self.root/'reports')
        self.assertFalse((self.root/'reports/completed.json').exists())
        self.assertTrue((self.root/'reports/failed.json').exists())

    def test_completed_p6_prefix_in_failed_collection_uses_raw_checker(self):
        summary = self.put('var/p6/normal-1/loadgen/summary.json', {'raw': 'fixture'})
        self.put('var/p6/normal-1/loadgen/run.json', {})
        self.put('var/p6/normal-1/completed.json', {'sha256': {'loadgen/summary.json': digest(summary)}})
        self.put('var/p6/failed.json', {'error': 'next quiet gate rejected'})
        seal(self.repo/'var/p6', False)
        create(self.repo, [self.repo/'var/p6'], [], self.root/'package')
        restore(self.root/'package', self.root/'restored')
        def checker(command, **kwargs):
            self.assertTrue(str(command[0]).endswith('/experimentcheck'))
            self.assertTrue(str(command[1]).startswith(str(self.root/'restored/data')))
            save(Path(command[2]), {'recomputed': True})
        with patch('build.build_checker'), patch('evidence.subprocess.run', side_effect=checker):
            result = reproduce(self.root/'restored', self.root/'reports')
        report = read(self.root/'reports'/result['analyses'][0])
        self.assertEqual(report['kind'], 'p6-raw')
        self.assertEqual(report['cases']['normal-1'], {'recomputed': True})
        self.assertFalse(report['performanceQualified'])

    def test_payload_digest_and_inventory_limits(self):
        package, index = self.package()
        with (package/'payload.tar.gz').open('ab') as target:target.write(b'changed')
        with self.assertRaisesRegex(ValueError, 'payload changed'):restore(package, self.root/'bad')
        self.assertFalse((self.root/'bad').exists())
        for fault in ('escape', 'oversize', 'bad-hash', 'link-kind'):
            value = copy.deepcopy(index)
            if fault == 'escape':value['entries']['../escape'] = value['entries'].pop('config/spec.json')
            if fault == 'oversize':value['entries']['config/spec.json']['bytes'] = 5*2**30
            if fault == 'bad-hash':value['entries']['config/spec.json']['sha256'] = 'z'*64
            if fault == 'link-kind':value['entries']['config/spec.json']['kind'] = 'symlink'
            with self.subTest(fault=fault), self.assertRaises(ValueError):check_index(value)

    def test_hostile_tar_members_missing_corrupt_and_expansion_are_rejected(self):
        body = b'preserved'
        entries = {'file': {'kind': 'file', 'mode': 0o600, 'bytes': len(body), 'sha256': hashlib.sha256(body).hexdigest()}}
        for fault in ('traversal', 'absolute', 'duplicate', 'symlink', 'hardlink', 'contiguous', 'missing', 'bytes', 'size'):
            archive = self.root/f'{fault}.tar.gz'
            with tarfile.open(archive, 'w:gz') as target:
                if fault != 'missing':
                    header = tarfile.TarInfo('../escape' if fault == 'traversal' else '/escape' if fault == 'absolute' else 'file')
                    header.size = len(body) + (1 if fault == 'size' else 0)
                    if fault in ('symlink', 'hardlink'):
                        header.type = tarfile.SYMTYPE if fault == 'symlink' else tarfile.LNKTYPE
                        header.linkname, header.size = '../escape', 0
                    if fault == 'contiguous':header.type = tarfile.CONTTYPE
                    contents = b'corrupted' if fault == 'bytes' else body+b'x'
                    target.addfile(header, io.BytesIO(contents))
                    if fault == 'duplicate':target.addfile(header, io.BytesIO(body))
            destination = self.root/f'data-{fault}'
            destination.mkdir()
            with self.subTest(fault=fault), self.assertRaises((ValueError, tarfile.TarError, EOFError)):
                inspect_payload(archive, entries, destination)
        self.assertFalse((self.root/'escape').exists())
        # A gigantic PAX header is consumed internally before member checks.
        archive = self.root/'pax.tar.gz'
        with tarfile.open(archive, 'w:gz') as target:
            header = tarfile.TarInfo('file')
            header.pax_headers = {'comment': 'x'*100_000}
            header.size = len(body)
            target.addfile(header, io.BytesIO(body))
        with self.assertRaisesRegex(ValueError, 'expanded archive'):
            inspect_payload(archive, entries)

    def test_symlink_payload_rejected_and_empty_bundle_selection_rejected(self):
        with self.assertRaises(ValueError):create(self.repo, [], [], self.root/'empty')
        self.fixture()
        (self.repo/'var/timer/link').symlink_to(self.repo/'config/spec.json')
        with self.assertRaisesRegex(ValueError, 'symlink or special'):
            create(self.repo, [self.repo/'var/timer'], [], self.root/'linked')


if __name__ == '__main__':
    unittest.main()
