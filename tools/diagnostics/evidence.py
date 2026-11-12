"""Immutable evidence packages, bounded restore and offline report reproduction."""
import argparse
from datetime import datetime, timezone
import hashlib
import gzip
import json
import os
from pathlib import Path, PurePosixPath
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile

from common import child, digest, read, require, save, verify
from artifacts import token, walk

MAX_BYTES = 4 * 2**30
MAX_FILES = 100_000
MIN_FREE = 8 * 2**30
OUTPUT_MARKERS = {'manifest.json', 'artifact-manifest.json', 'protocol.json', 'build.json',
                  'genesis.bin', 'commands.wal', 'package-index.json', 'restore-index.json'}


def relative(name):
    """Accept only canonical portable relative archive paths."""
    require(isinstance(name, str) and 0 < len(name) <= 4096 and '\\' not in name and ':' not in name
            and '\0' not in name, 'invalid package path')
    path = PurePosixPath(name)
    require(not path.is_absolute() and '..' not in path.parts and path.as_posix() == name and name != '.',
            'noncanonical package path')
    return name


def tree(path):
    """Capture a stopped input tree, rejecting links and special files."""
    require(path.exists() and not path.is_symlink(), f'missing or linked input: {path}')
    states = walk(path)
    require(all(stat.S_ISREG(s.st_mode) or stat.S_ISDIR(s.st_mode) for s in states.values()),
            'symlink or special input')
    return {p: token(s) for p, s in states.items()}


def fresh_output(path, inputs=()):
    """Validate a new output before writing into any existing evidence tree."""
    path = Path(path).absolute()
    require('..' not in path.parts and not path.exists() and not path.is_symlink(), 'output already exists or traverses parents')
    require(path.parent.is_dir(), 'output parent must exist')
    for parent in path.parents:
        require(not parent.is_symlink(), 'symlink in output parent')
        require(not any((parent/name).exists() for name in OUTPUT_MARKERS), 'output is inside existing evidence')
    require(not any(path.is_relative_to(p) for p in inputs), 'output is inside an input')
    return path


def capacity(parent, size):
    """Keep a fixed free-space reserve before producing a package or restore."""
    require(size <= MAX_BYTES and shutil.disk_usage(parent).free >= size+MIN_FREE,
            'package/restore budget exceeded (4GiB input, 8GiB free reserve)')


class Dependencies:
    """Collect typed dependencies, never arbitrary paths found in free text."""
    def __init__(self, repo):
        self.repo = Path(repo).resolve()
        self.units, self.states, self.comparisons, self.edges = {}, {}, [], []

    def source(self, value, base=None):
        path = Path(value)
        path = (path if path.is_absolute() else (base or self.repo)/path).absolute()
        require(path.resolve().is_relative_to(self.repo), 'dependency outside source checkout')
        current = path
        while current != current.parent:
            require(not current.is_symlink(), 'linked dependency path')
            current = current.parent
        require(path.exists(), f'missing dependency: {path}')
        return path.resolve()

    def bundle_root(self, value):
        path = self.source(value)
        require(path.is_dir(), 'bundle selector must name a directory')
        for parent in reversed([path, *path.parents]):
            if parent == self.repo or not parent.is_relative_to(self.repo):
                continue
            if (parent/'manifest.json').is_file() or (parent/'artifact-manifest.json').is_file():
                return parent
        raise ValueError('select a sealed collection (whole historical tar-only archives are not automatic inputs)')

    def add(self, path, kind, via=None):
        path = self.source(path)
        name = relative(path.relative_to(self.repo).as_posix())
        if via:
            self.edges.append({'from': via, 'to': name, 'kind': kind})
        if name in self.units:
            require(self.units[name]['kind'] == kind, 'conflicting dependency kinds')
            return name
        require(len(self.units) < 100, 'too many dependency units')
        self.units[name] = {'path': name, 'kind': kind}
        self.states[name] = tree(path)
        if kind == 'bundle':
            protocol = read(path/'protocol.json') if (path/'protocol.json').is_file() else {}
            if protocol.get('resumeFrom'):
                reference = protocol['resumeFrom']
                parent = self.source(reference['path'])
                require(digest(parent/'manifest.json') == reference['manifestSHA256'], 'resume parent hash mismatch')
                self.add(self.bundle_root(parent), 'bundle', name)
            if (path/'producer-build.json').is_file():
                record = read(path/'producer-build.json')
                argv = record['argv']
                require(isinstance(argv, list) and argv.count('-o') == 1, 'unknown producer build path')
                build = self.source(argv[argv.index('-o')+1]).parent
                require(read(build/'build.json') == record, 'producer provenance differs from fixed build')
                self.add(build, 'build', name)
        return name

    def add_spec(self, value):
        from comparison import spec_inputs
        path = self.source(value)
        name = self.add(path, 'file')
        require(name not in self.comparisons, 'duplicate comparison spec')
        self.comparisons.append(name)
        spec, criteria = spec_inputs(path, self.source)
        for group in spec['comparisons']:
            for side in ('baseline', 'candidate'):
                for entry in group[side]:
                    self.add(self.bundle_root(entry['bundle']), 'bundle', name)
        criteria = self.source(criteria)
        criteria_name = self.add(criteria, 'file', name)
        value = read(criteria)
        for key in ('configFile', 'workloadFile'):
            require(isinstance(value.get(key), str), 'criteria must include config/workload dependencies')
            self.add(self.source(value[key], criteria.parent), 'file', criteria_name)

    def unchanged(self):
        """Detect changed bytes/paths/metadata while collecting or archiving."""
        require(all(tree(self.repo/name) == state for name, state in self.states.items()), 'source changed during packaging')


def validate_units(root, units, resolver=None):
    """Recheck inventories, archives, resume chains and fixed build headers."""
    from analysis import verify_archives
    from baseline import validate_build, verify_dependencies
    result = []
    for unit in units:
        path = child(root, unit['path'])
        record = {'path': unit['path'], 'kind': unit['kind']}
        if unit['kind'] == 'bundle':
            record['integrity'] = verify(path, allow_failed=True)
            record['archives'] = verify_archives(path)
            if (path/'protocol.json').is_file():
                record['dependencies'] = verify_dependencies(path, resolver)
        elif unit['kind'] == 'build':
            record['revision'] = validate_build(path)['revision']
        else:
            require(unit['kind'] == 'file', 'unknown unit kind')
            record['sha256'] = digest(path)
        result.append(record)
    return result


def entries_for(dependencies):
    """Hash all selected bytes and metadata once into a flat, exact inventory."""
    entries = {}
    for state in dependencies.states.values():
        for path, captured in state.items():
            name = relative(path.relative_to(dependencies.repo).as_posix())
            info = path.lstat()
            require(token(info) == captured, 'source changed before hashing')
            item = {'kind': 'directory' if stat.S_ISDIR(info.st_mode) else 'file',
                    'mode': stat.S_IMODE(info.st_mode) & 0o777, 'bytes': 0 if path.is_dir() else info.st_size}
            if item['kind'] == 'file':
                item['sha256'] = digest(path)
            require(name not in entries or entries[name] == item, 'overlapping inputs differ')
            entries[name] = item
    require(0 < len(entries) <= MAX_FILES, 'invalid package entry count')
    require(sum(v['bytes'] for v in entries.values()) <= MAX_BYTES, 'package input exceeds 4GiB')
    dependencies.unchanged()
    return dict(sorted(entries.items()))


def inspect_payload(archive, entries, destination=None):
    """Stream exact regular tar members; never use extractall or trust links."""
    seen = set()
    # Bound decompression too: tarfile consumes PAX headers before exposing a
    # member, so checking member sizes alone cannot bound hostile gzip input.
    class Limited:
        def __init__(self, source):
            self.source = source
            self.remaining = sum(v['bytes'] for v in entries.values()) + len(entries)*8192 + 10240

        def read(self, size):
            require(size >= 0, 'unbounded archive read')
            data = self.source.read(min(size, self.remaining+1))
            self.remaining -= len(data)
            require(self.remaining >= 0, 'expanded archive exceeds inventory budget')
            return data

    with gzip.open(archive, 'rb') as compressed, tarfile.open(fileobj=Limited(compressed), mode='r|') as source:
        for member in source:
            name = relative(member.name)
            require(name in entries and name not in seen and member.type in (tarfile.REGTYPE, tarfile.AREGTYPE, tarfile.DIRTYPE)
                    and not member.sparse, 'unexpected, duplicate or linked archive member')
            value = entries[name]
            require((member.isdir() and value['kind'] == 'directory' or member.isfile() and value['kind'] == 'file')
                    and member.size == value['bytes'], 'archive type/size mismatch')
            target = child(destination, name) if destination else None
            if value['kind'] == 'directory':
                if target:
                    target.mkdir(parents=True, exist_ok=True)
            else:
                data = source.extractfile(member)
                require(data is not None, 'archive file unavailable')
                out = None
                try:
                    if target:
                        target.parent.mkdir(parents=True, exist_ok=True)
                        out = target.open('xb')
                    h = hashlib.sha256()
                    with data:
                        for block in iter(lambda: data.read(2**20), b''):
                            h.update(block)
                            if out:
                                out.write(block)
                    require(h.hexdigest() == value['sha256'], 'archive member hash mismatch')
                    if out:
                        out.flush()
                        os.fsync(out.fileno())
                finally:
                    if out:
                        out.close()
                if target:
                    target.chmod(value['mode'])
            seen.add(name)
    require(seen == set(entries), 'missing archive member')
    if destination:
        for name in sorted(entries, key=lambda n: len(PurePosixPath(n).parts), reverse=True):
            if entries[name]['kind'] == 'directory':
                child(destination, name).chmod(entries[name]['mode'])
    return len(seen)


def create(repo, bundles, specs, output):
    """Create and verify a package while leaving every original byte in place."""
    dependencies = Dependencies(repo)
    for bundle in bundles:
        dependencies.add(dependencies.bundle_root(bundle), 'bundle')
    for spec in specs:
        dependencies.add_spec(spec)
    require(dependencies.units, 'select a bundle or comparison spec')
    dependencies.unchanged()
    units = sorted(dependencies.units.values(), key=lambda u: u['path'])
    validation = validate_units(dependencies.repo, units, dependencies.source)
    entries = entries_for(dependencies)
    output = fresh_output(output, [dependencies.repo/name for name in dependencies.units])
    require(not any(output.is_relative_to(dependencies.repo/name) for name in dependencies.units), 'output inside input')
    size = sum(v['bytes'] for v in entries.values())
    capacity(output.parent, size+len(entries)*1024)
    output.mkdir(mode=0o700)
    archive = output/'payload.tar.gz'
    try:
        with tarfile.open(archive, 'x:gz', compresslevel=1) as target:
            for name, item in entries.items():
                path = dependencies.repo/name
                header = tarfile.TarInfo(name)
                header.mode, header.size = item['mode'], item['bytes']
                header.type = tarfile.DIRTYPE if item['kind'] == 'directory' else tarfile.REGTYPE
                if item['kind'] == 'directory':
                    target.addfile(header)
                else:
                    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
                    with os.fdopen(fd, 'rb') as data:
                        target.addfile(header, data)
        with archive.open('rb') as data:
            os.fsync(data.fileno())
        inspect_payload(archive, entries)
        dependencies.unchanged()
        index = {'version': 1, 'kind': 'exchange-evidence-package', 'originRepo': str(dependencies.repo),
                 'createdUTC': datetime.now(timezone.utc).isoformat(), 'entries': entries, 'units': units,
                 'comparisons': dependencies.comparisons, 'dependencies': dependencies.edges,
                 'inputValidation': validation, 'payloadBytes': archive.stat().st_size,
                 'payloadSHA256': digest(archive), 'toolSHA256': digest(Path(__file__)), 'powerLossTested': False}
        require(len((json.dumps(index, ensure_ascii=False, indent=2, allow_nan=False)+'\n').encode()) <= 64*2**20,
                'package index exceeds JSON read limit')
        save(output/'package-index.json', index)
        save(output/'completed.json', {'version': 1, 'indexSHA256': digest(output/'package-index.json'),
                                      'payloadSHA256': index['payloadSHA256']})
        return index
    except BaseException as error:
        save(output/'failed.json', {'error': type(error).__name__, 'message': str(error)})
        raise


def check_index(index):
    """Bound and validate all restore paths before opening the archive."""
    require(index.get('version') == 1 and index.get('kind') == 'exchange-evidence-package', 'unsupported package')
    require(isinstance(index['originRepo'], str) and Path(index['originRepo']).is_absolute(), 'invalid origin')
    entries = index['entries']
    require(isinstance(entries, dict) and 0 < len(entries) <= MAX_FILES, 'invalid entry inventory')
    total = 0
    for name, value in entries.items():
        relative(name)
        require(value['kind'] in ('file', 'directory') and type(value['bytes']) is int and value['bytes'] >= 0
                and type(value['mode']) is int and 0 <= value['mode'] <= 0o777, 'invalid member metadata')
        if value['kind'] == 'file':
            require(isinstance(value['sha256'], str) and len(value['sha256']) == 64
                    and all(c in '0123456789abcdef' for c in value['sha256']), 'invalid member hash')
        else:
            require(value['bytes'] == 0, 'directory payload is not empty')
        total += value['bytes']
    require(total <= MAX_BYTES and 0 < len(index['units']) <= 100, 'package budget exceeded')
    for unit in index['units']:
        relative(unit['path'])
        require(unit['path'] in entries and unit['kind'] in ('file', 'bundle', 'build'), 'invalid unit')
    for path in index['comparisons']:
        require(relative(path) in entries, 'missing comparison spec')
    return total


def restore(package, output):
    """Verify a package and restore exact data into a fresh folder."""
    package = Path(package).resolve()
    require(set(p.name for p in package.iterdir()) == {'package-index.json', 'payload.tar.gz', 'completed.json'},
            'incomplete or unexpected package files')
    index = read(package/'package-index.json')
    total = check_index(index)
    completed = read(package/'completed.json')
    require(completed == {'version': 1, 'indexSHA256': digest(package/'package-index.json'),
                          'payloadSHA256': index['payloadSHA256']}, 'package completion mismatch')
    archive = package/'payload.tar.gz'
    require(archive.stat().st_size == index['payloadBytes'] and digest(archive) == index['payloadSHA256'], 'package payload changed')
    output = fresh_output(output, [package])
    require(not any(output.is_relative_to(Path(index['originRepo'])/u['path']) for u in index['units']), 'restore inside original input')
    capacity(output.parent, total+len(index['entries'])*4096)
    output.mkdir(mode=0o700)
    data = output/'data'
    data.mkdir()
    try:
        count = inspect_payload(archive, index['entries'], data)
        save(output/'restore-index.json', {'version': 1, 'kind': 'exchange-evidence-restore', 'package': index,
                                          'indexSHA256': completed['indexSHA256'], 'filesRestored': count})
        return {'status': 'RESTORED', 'entries': count, 'sourceBytesModified': False}
    except BaseException as error:
        save(output/'failed.json', {'error': type(error).__name__, 'message': str(error)})
        raise


class Restored:
    """Resolve only packaged paths, with no original-checkout fallback."""
    def __init__(self, root):
        self.root = Path(root).resolve()
        record = read(self.root/'restore-index.json')
        require(record.get('version') == 1 and record.get('kind') == 'exchange-evidence-restore', 'invalid restore record')
        self.index = record['package']
        check_index(self.index)
        # Same canonical serialization as common.save; detect altered restore metadata.
        encoded = (json.dumps(self.index, ensure_ascii=False, indent=2, allow_nan=False)+'\n').encode()
        require(hashlib.sha256(encoded).hexdigest() == record['indexSHA256'], 'restore index changed')
        self.data = self.root/'data'
        expected = {self.data/relative(name): value for name, value in self.index['entries'].items()}
        actual = tree(self.data)
        require(set(actual)-{self.data} == set(expected) | {p for name in expected for p in name.parents if p.is_relative_to(self.data) and p != self.data},
                'restored file set changed')
        for path, value in expected.items():
            require(path.is_dir() if value['kind'] == 'directory' else path.is_file(), 'restored type changed')
            require(stat.S_IMODE(path.stat().st_mode) == value['mode'], 'restored permissions changed')
            if value['kind'] == 'file':
                require(path.stat().st_size == value['bytes'] and digest(path) == value['sha256'], 'restored bytes changed')
        self.states = actual

    def resolve(self, value):
        path = Path(os.path.normpath(str(value)))
        if path.is_absolute():
            if path.is_relative_to(self.data):
                name = str(path.relative_to(self.data))
            else:
                require(path.is_relative_to(Path(self.index['originRepo'])), 'path outside restored package')
                name = str(path.relative_to(self.index['originRepo']))
        else:
            name = str(path)
        relative(name)
        require(name in self.index['entries'], 'dependency not packaged; original fallback forbidden')
        return child(self.data, name)

    def unchanged(self):
        require(tree(self.data) == self.states, 'restored inputs changed during reproduction')


def reproduce(root, output):
    """Regenerate verification, analyses and comparisons from restored bytes."""
    from analysis import analyze_bundle, discover
    from comparison import compare
    restored = Restored(root)
    output = fresh_output(output, [restored.data])
    output.mkdir(mode=0o700)
    try:
        units = restored.index['units']
        verification = validate_units(restored.data, units, restored.resolve)
        save(output/'verification.json', {'status': 'PASSED', 'units': verification, 'originalFallbackAllowed': False})
        analyzed = []
        for n, unit in enumerate(units):
            path = restored.resolve(unit['path'])
            if unit['kind'] != 'bundle':
                continue
            cases = discover(path)
            p6 = cases and all((case/'completed.json').is_file()
                              and 'sha256' in read(case/'completed.json') for case in cases)
            if p6:
                # Baseline collections include completed P6 experiments even
                # when their next quiet gate failed. Their schema is distinct
                # from diagnostic suites; recompute using the raw Go checker.
                from build import build_checker
                from comparison import source_hashes
                result = {'kind': 'p6-raw', 'cases': {}, 'sourceSHA256': source_hashes(),
                          'performanceQualified': False, 'powerLossTested': False}
                with tempfile.TemporaryDirectory(prefix='exchange-restored-check-') as temp:
                    scratch = Path(temp)
                    checker = scratch/'experimentcheck'
                    build_checker(checker)
                    for case in cases:
                        for name, sha in read(case/'completed.json')['sha256'].items():
                            require(digest(child(case, name)) == sha, 'P6 completion hash mismatch')
                        raw = scratch/(case.name+'.json')
                        subprocess.run([str(checker), str(case/'loadgen'), str(raw)], check=True,
                                       capture_output=True, timeout=180)
                        result['cases'][case.name] = read(raw)
                name = f'analysis-{n}.json'
                save(output/name, result)
                analyzed.append(name)
            elif (path/'completed.json').exists() and cases:
                name = f'analysis-{n}.json'
                save(output/name, analyze_bundle(path))
                analyzed.append(name)
        comparisons = []
        for n, spec in enumerate(restored.index['comparisons']):
            target = output/f'comparison-{n}'
            report = compare(restored.resolve(spec), target, restored.resolve)
            records = [record for group in report['comparisons'] for side in ('baseline', 'candidate') for record in group[side]]
            require(all(record['status'] == 'verified' for record in records), 'restored comparison contains evidence errors')
            comparisons.append(str(target.relative_to(output)))
        restored.unchanged()
        result = {'status': 'PASSED', 'unitsVerified': len(units), 'analyses': analyzed, 'comparisons': comparisons,
                  'originalFallbackAllowed': False, 'retainedBinariesExecuted': False, 'powerLossTested': False}
        save(output/'completed.json', result)
        return result
    except BaseException as error:
        save(output/'failed.json', {'error': type(error).__name__, 'message': str(error)})
        raise


def main():
    """Expose create/restore/reproduce without deleting or running measurements."""
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    pack = sub.add_parser('create')
    pack.add_argument('--repo', type=Path, default=Path(__file__).resolve().parents[2])
    pack.add_argument('--bundle', type=Path, action='append', default=[])
    pack.add_argument('--comparison', type=Path, action='append', default=[])
    pack.add_argument('--output', type=Path, required=True)
    unpack = sub.add_parser('restore')
    unpack.add_argument('--package', type=Path, required=True)
    unpack.add_argument('--output', type=Path, required=True)
    check = sub.add_parser('reproduce')
    check.add_argument('--restored', type=Path, required=True)
    check.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.command == 'create':
        index = create(args.repo, args.bundle, args.comparison, args.output)
        result = {'status': 'PACKAGED', 'units': len(index['units']), 'entries': len(index['entries']), 'output': str(args.output)}
    elif args.command == 'restore':
        result = restore(args.package, args.output)
    else:
        result = reproduce(args.restored, args.output)
    print(json.dumps(result, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        print(f'evidence: {error}', file=sys.stderr)
        sys.exit(1)
