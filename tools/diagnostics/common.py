"""Shared bounded I/O and immutable bundle inventory."""
import hashlib
import json
import os
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
MAX_ROWS = 2_000_020


def require(condition, message='invalid experiment evidence'):
    if not condition:
        raise ValueError(message)


def unique(pairs):
    result = {}
    for key, value in pairs:
        require(key not in result, f'duplicate JSON key: {key}')
        result[key] = value
    return result


def decode(value):
    return json.loads(value, object_pairs_hook=unique,
                      parse_constant=lambda x: require(False, f'non-finite JSON: {x}'))


def regular(path):
    path = Path(path)
    require(path.is_file() and not path.is_symlink(), f'not a regular file: {path}')
    return path


def read(path):
    path = regular(path)
    require(path.stat().st_size <= 64 * 2**20, f'JSON too large: {path}')
    return decode(path.read_text())


def lines(path):
    result = []
    with regular(path).open() as source:
        for line in source:
            require(len(line) <= 2**20 and len(result) < MAX_ROWS, 'JSONL limit exceeded')
            result.append(decode(line))
    return result


def digest(path):
    value = hashlib.sha256()
    with regular(path).open('rb') as source:
        for block in iter(lambda: source.read(2**20), b''):
            value.update(block)
    return value.hexdigest()


def save(path, value):
    with Path(path).open('x') as target:
        json.dump(value, target, ensure_ascii=False, indent=2, allow_nan=False)
        target.write('\n')
        target.flush()
        os.fsync(target.fileno())


def child(root, name):
    root = Path(root).resolve()
    rel = Path(name)
    require(isinstance(name, str) and not rel.is_absolute() and name not in ('', '.')
            and '..' not in rel.parts, f'invalid relative path: {name}')
    path = root / rel
    current = path
    while current != root:
        require(not current.is_symlink(), f'symlink in evidence path: {name}')
        current = current.parent
    require(path.resolve().is_relative_to(root.resolve()), 'path escaped bundle')
    return path


def inventory(root):
    files = {}
    for path in sorted(root.rglob('*')):
        require(not path.is_symlink(), f'symlink in bundle: {path}')
        if not path.is_file() or path in (root/'manifest.json', root/'completed.json'):
            continue
        files[str(path.relative_to(root))] = {'bytes': path.stat().st_size, 'sha256': digest(path)}
    return files


def seal(root, completed):
    save(root/'manifest.json', {'version': 1, 'files': inventory(root)})
    if completed:
        save(root/'completed.json', {'version': 1, 'manifestSHA256': digest(root/'manifest.json')})


def verify(root, allow_failed=False):
    root = Path(root).resolve()
    legacy = not (root/'manifest.json').exists() and (root/'artifact-manifest.json').exists()
    manifest = read(root/('artifact-manifest.json' if legacy else 'manifest.json'))
    if not legacy:
        if (root/'completed.json').exists():
            require(read(root/'completed.json')['manifestSHA256'] == digest(root/'manifest.json'),
                    'bundle manifest hash mismatch')
        else:
            require(allow_failed and (root/'failed.json').exists(), 'bundle is incomplete')
    for name, expected in manifest['files'].items():
        path = child(root, name)
        require(path.stat().st_size == expected['bytes'] and digest(path) == expected['sha256'],
                f'artifact hash mismatch: {name}')
    if not legacy:
        require(set(manifest['files']) == set(inventory(root)), 'unrecorded or missing bundle file')
    return {'filesVerified': len(manifest['files']), 'legacyInventory': legacy,
            'completed': legacy or (root/'completed.json').exists()}
