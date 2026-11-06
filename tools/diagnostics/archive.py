"""Archive only new, stopped, recovery-verified case files; verify before unlink."""
import hashlib
import json
import os
from pathlib import Path
import tarfile
from common import child, digest, read, save, require


def archive(case, prune=False):
    case = Path(case).resolve()
    if read(case / 'verification.json')['status'] != 'PASSED':
        raise ValueError('recovery must pass before archiving')
    paths = sorted((case / 'run').glob('snapshot-*.bin'))
    paths += [case / (s + '.jsonl') for s in ('before-restart', 'after-restart', 'after-retries')]
    entries = {}
    for p in paths:
        child(case, str(p.relative_to(case)))
        if p.is_symlink() or not p.is_file():
            raise ValueError('expected regular generated artifact')
        entries[str(p.relative_to(case))] = {'bytes': p.stat().st_size, 'sha256': digest(p)}
    target = case / 'state-artifacts.tar.gz'
    with tarfile.open(target, 'x:gz', compresslevel=1) as out:
        for p in paths:
            out.add(p, arcname=str(p.relative_to(case)), recursive=False)
    with target.open('rb') as f:
        os.fsync(f.fileno())
    seen = set()
    with tarfile.open(target, 'r:gz') as inp:
        for member in inp:
            if not member.isfile() or member.name not in entries or member.name in seen:
                raise ValueError('unexpected archive member')
            h = hashlib.sha256()
            with inp.extractfile(member) as f:
                for b in iter(lambda: f.read(1024 * 1024), b''):
                    h.update(b)
            if {'bytes': member.size, 'sha256': h.hexdigest()} != entries[member.name]:
                raise ValueError('archive bytes differ')
            seen.add(member.name)
    if seen != set(entries):
        raise ValueError('incomplete archive')
    result = {'status': 'VERIFIED', 'archive': target.name, 'archiveBytes': target.stat().st_size,
              'archiveSHA256': digest(target), 'originalBytes': sum(e['bytes'] for e in entries.values()), 'files': entries}
    save(case / 'archive-manifest.json', result)
    # Preserve any unexpectedly changed file. Only verified, newly created
    # artifacts from this case are removed after complete archive verification.
    for p in paths:
        if digest(p) != entries[str(p.relative_to(case))]['sha256']:
            raise ValueError('original changed after archive verification')
    if prune:
        for p in paths:
            p.unlink()
    save(case / 'archive-completed.json', {'status': 'PASSED', 'removedFiles': len(paths) if prune else 0, 'archiveSHA256': result['archiveSHA256']})
    return result


def verify_archive(case):
    manifest = read(case / 'archive-manifest.json')
    path = child(case, manifest['archive'])
    require(digest(path) == manifest['archiveSHA256'], 'archive hash mismatch')
    expected = manifest['files']
    require(len(expected) <= 100000 and sum(x['bytes'] for x in expected.values()) <= 16*2**30,
            'archive exceeds diagnostic limits')
    seen = set()
    with tarfile.open(path, 'r:gz') as source:
        for member in source:
            child(case, member.name)
            require(member.isfile() and member.name in expected and member.name not in seen,
                    'unexpected archive member')
            require(member.size == expected[member.name]['bytes'], 'archive size mismatch')
            value = hashlib.sha256()
            with source.extractfile(member) as data:
                for block in iter(lambda: data.read(2**20), b''):
                    value.update(block)
            require(value.hexdigest() == expected[member.name]['sha256'], 'archive member hash mismatch')
            seen.add(member.name)
    require(seen == set(expected), 'missing archive member')
    return {'membersVerified': len(seen), 'archiveSHA256': manifest['archiveSHA256']}
