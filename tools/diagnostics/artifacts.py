"""Read-only retention inventory and conservative deletion previews for var."""
import argparse
from collections import defaultdict
from datetime import datetime, timezone
import fnmatch
import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys

from common import decode, require, save

CONTAINERS = {'ci', 'demos', 'diagnostics', 'comparisons', 'requests', 'runs',
              'archives', 'cleanup', 'artifact-reports'}
TEXT = {'.md', '.json', '.txt', '.log', '.py', '.sh', '.go', '.mod', '.stdout', '.stderr', '.yml', '.yaml'}
MAX_TEXT = 8 * 2**20
MAX_TOTAL_TEXT = 256 * 2**20
RAW_NAMES = {'genesis.bin', 'commands.wal', 'samples.jsonl', 'plan.jsonl'}
MARKERS = {'protocol.json', 'artifact-manifest.json', 'build.json', 'producer-build.json', 'failed.json'}
DERIVED = {'report.json', 'report.md', 'qualification.json'}
BRACES = re.compile(r'\{([A-Za-z0-9_./-]+(?:,[A-Za-z0-9_./-]+)+)\}')
HASH = re.compile(r'(?<![0-9a-f])[0-9a-f]{64}(?![0-9a-f])')


def token(info):
    """Describe file identity and modification state without following links."""
    return (info.st_dev, info.st_ino, info.st_mode, info.st_size, info.st_mtime_ns, info.st_ctime_ns)


def walk(root):
    """Inventory names and lstat metadata without entering symlinks."""
    result = {}
    if not root.exists():
        return result
    pending = [root]
    while pending:
        path = pending.pop()
        info = path.lstat()
        result[path] = info
        if stat.S_ISDIR(info.st_mode):
            pending.extend(sorted(path.iterdir(), reverse=True))
    return result


def policy_value(value):
    """Validate an explicit retention policy; protection always takes priority."""
    require(isinstance(value, dict) and set(value) == {'version', 'keep', 'temporary'} and value['version'] == 1,
            'invalid retention policy')
    for key in ('keep', 'temporary'):
        require(isinstance(value[key], list), f'policy {key} must be an array')
        for pattern in value[key]:
            require(isinstance(pattern, str) and pattern.startswith('var/') and '..' not in Path(pattern).parts
                    and '\\' not in pattern and not any(x in pattern for x in '\n\r\0'), 'invalid policy path')
    return value


def brace_text(text):
    """Expand bounded path-style brace abbreviations, never shell expressions."""
    extra = []
    for match in BRACES.finditer(text):
        start = match.start()
        while start and text[start-1] not in ' \t\n\r\"\'`<>():':
            start -= 1
        end = match.end()
        while end < len(text) and text[end] not in ' \t\n\r\"\'`<>():':
            end += 1
        word = text[start:end]
        require(len(word) <= 4096 and len(match[1].split(',')) <= 64, 'reference brace expansion too large')
        for item in match[1].split(','):
            extra.append(word.replace(match[0], item, 1))
    return text + '\n' + '\n'.join(extra)


def git_sources(repo):
    """Include tracked and current nonignored sources in the reference scan."""
    raw = subprocess.check_output(['git', '-C', str(repo), 'ls-files', '--cached', '--others',
                                   '--exclude-standard', '-z'], stderr=subprocess.DEVNULL)
    return sorted(set(raw.decode('utf-8').rstrip('\0').split('\0')) - {''})


def bounded_metadata(path):
    """Read one bounded regular metadata file and verify its identity."""
    info = path.lstat()
    require(stat.S_ISREG(info.st_mode), 'not a regular reference file')
    require(info.st_size <= MAX_TEXT, 'reference metadata exceeds 8 MiB')
    fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW)
    with os.fdopen(fd, 'rb') as stream:
        require(token(os.fstat(stream.fileno())) == token(info), 'reference replaced before read')
        data = stream.read(MAX_TEXT+1)
        require(len(data) <= MAX_TEXT and token(os.fstat(stream.fileno())) == token(info), 'reference changed while reading')
    require(token(path.lstat()) == token(info), 'reference path changed')
    return data, info


def inspect(repo, policy, policy_path=None):
    """Build a conservative retention graph without changing any input file."""
    repo = Path(repo).resolve()
    policy = policy_value(policy)
    var = repo/'var'
    require(not var.is_symlink(), 'var must not be a symlink')
    require(not var.exists() or var.is_dir(), 'var must be a directory')
    initial = walk(var)
    issues = []
    try:
        sources = git_sources(repo)
    except (OSError, subprocess.SubprocessError, UnicodeError) as error:
        sources = []
        issues.append(f'cannot list Git sources: {type(error).__name__}')
    # A reserved container name can itself be a valid experiment output. Never
    # split a sealed/run root merely because its name normally groups outputs.
    root_markers = MARKERS | RAW_NAMES | {'manifest.json', 'summary.json', 'retention-report.json'}
    splittable = {name for name in CONTAINERS if not any(
        path.parent == var/name and path.name in root_markers for path in initial)}
    roots = []
    for path in sorted(initial):
        rel = path.relative_to(var)
        if len(rel.parts) == 1:
            if rel.name not in splittable or not stat.S_ISDIR(initial[path].st_mode):
                roots.append(path)
        elif len(rel.parts) == 2 and rel.parts[0] in splittable:
            roots.append(path)
    # Container directories themselves carry no experiment data unless empty.
    for name in splittable:
        path = var/name
        if path in initial and stat.S_ISDIR(initial[path].st_mode) and not any(p.parent == path for p in initial):
            roots.append(path)
    roots = sorted(roots)
    units = {str(p.relative_to(repo)): {'path': str(p.relative_to(repo)), 'files': 0, 'logicalBytes': 0,
             'allocatedBytes': 0, 'reasons': [], 'references': [], 'referencedBy': []} for p in roots}
    root_names = {p: str(p.relative_to(repo)) for p in roots}

    def owner(path):
        current = path
        while current != var and current != repo and current != current.parent:
            if current in root_names:
                return root_names[current]
            current = current.parent
        return None

    members = defaultdict(list)
    allocated_inodes = set()
    total_allocated = 0
    for path, info in initial.items():
        key = (info.st_dev, info.st_ino)
        if key not in allocated_inodes:
            total_allocated += info.st_blocks*512
            allocated_inodes.add(key)
        name = owner(path)
        if not name:
            continue
        members[name].append(path)
        item = units[name]
        item['allocatedBytes'] += info.st_blocks*512
        if stat.S_ISREG(info.st_mode):
            item['files'] += 1
            item['logicalBytes'] += info.st_size
        elif not stat.S_ISDIR(info.st_mode):
            item['reasons'].append('symlink-or-special-file')

    texts, raw_texts, checks, hashes = {}, {}, {}, defaultdict(set)
    generated = set()
    read_bytes = 0

    def budgeted(path):
        nonlocal read_bytes
        require(read_bytes + path.lstat().st_size <= MAX_TOTAL_TEXT, 'reference scan exceeds 256 MiB')
        data, info = bounded_metadata(path)
        read_bytes += len(data)
        require(read_bytes <= MAX_TOTAL_TEXT, 'reference scan exceeds 256 MiB')
        return data, info

    if policy_path is not None:
        path = Path(policy_path).absolute()
        try:
            data, info = budgeted(path)
            require(decode(data.decode('utf-8')) == policy, 'policy changed before inventory')
            checks[path] = token(info)
        except (OSError, UnicodeError, ValueError):
            issues.append('policy changed or could not be read; all candidates withheld')

    for name, paths in members.items():
        names = {p.name for p in paths}
        # Only this tool's signature exempts a generated report from reference scanning.
        if name.startswith('var/artifact-reports/') and names == {'retention-report.json', 'plan.json', 'report.md', 'completed.json', Path(name).name}:
            marker = Path(repo/name)/'retention-report.json'
            try:
                data, info = budgeted(marker)
                value = decode(data.decode('utf-8'))
                if value.get('kind') == 'artifact-retention-preview' and value.get('version') == 1:
                    checks[marker] = token(info)
                    recorded = {'retention-report.json': data}
                    for filename in ('plan.json', 'report.md', 'completed.json'):
                        path = marker.parent/filename
                        raw, info = budgeted(path)
                        recorded[filename] = raw
                        checks[path] = token(info)
                    completed = decode(recorded['completed.json'].decode('utf-8'))
                    require(completed == {'version': 1, 'filesSHA256': {
                        filename: hashlib.sha256(raw).hexdigest() for filename, raw in recorded.items()
                        if filename != 'completed.json'}}, 'retention report hashes mismatch')
                    for raw in recorded.values():
                        hashes[hashlib.sha256(raw).hexdigest()].add(name)
                    generated.add(name)
            except (OSError, ValueError, AttributeError):
                issues.append(f'invalid retention report: {name}')
        item = units[name]
        for marker in sorted(names & MARKERS):
            item['reasons'].append('evidence-marker:'+marker)
        if names & RAW_NAMES:
            item['reasons'].append('runtime-or-raw-measurement')
        if name.startswith(('var/requests/', 'var/runs/', 'var/archives/', 'var/cleanup/')):
            item['reasons'].append('requests-runs-archives-or-audit')
        if any(p.name.endswith(('.tar.gz', '.tar.receipt.json')) for p in paths):
            item['reasons'].append('archive-or-receipt')
        if 'summary.json' in names:
            item['reasons'].append('demo-or-run-summary')
        if any(fnmatch.fnmatchcase(name, pattern) for pattern in policy['keep']):
            item['reasons'].append('explicit-keep-policy')

    def read_text(path):
        try:
            data, info = budgeted(path)
            checks[path] = token(info)
            text = data.decode('utf-8')
            raw_texts[path] = text
            texts[path] = brace_text(text)
            unit = owner(path)
            if unit:
                hashes[hashlib.sha256(data).hexdigest()].add(unit)
        except (OSError, UnicodeError, ValueError) as error:
            issues.append(f'reference scan incomplete: {path.relative_to(repo)} ({type(error).__name__})')

    for rel in sources:
        path = repo/rel
        if rel.startswith('var/'):
            name = owner(path)
            if name:
                units[name]['reasons'].append('tracked-or-nonignored-data')
            continue
        if path.suffix in TEXT or path.name in ('AGENTS.md', 'README', 'Makefile'):
            # Policy patterns are retention choices, not historical citations.
            if rel == 'config/artifact-retention.json' or policy_path and path == Path(policy_path).absolute():
                continue
            read_text(path)
    for name, paths in members.items():
        if name in generated:
            continue
        for path in paths:
            if path.suffix in TEXT and stat.S_ISREG(initial[path].st_mode):
                read_text(path)

    edges = set()
    for path, text in texts.items():
        source = str(path.relative_to(repo))
        own = owner(path)
        for name in units:
            # Match concrete repository or absolute workspace paths; preserve suffix boundaries.
            if name != own and name in text and re.search(r'(?<![A-Za-z0-9_.-])'+re.escape(name)+r'(?![A-Za-z0-9_.-])', text):
                edges.add((source, name, 'path'))
        for digest in set(HASH.findall(text)):
            matches = hashes.get(digest, set())
            # A local manifest's own hash does not imply a dependency on all identical copies.
            if own not in matches:
                for name in matches:
                    edges.add((source, name, 'sha256'))

    for source, target, via in sorted(edges):
        units[target]['referencedBy'].append({'source': source, 'via': via})
        source_unit = owner(repo/source)
        if source_unit:
            units[source_unit]['references'].append({'target': target, 'via': via})
        units[target]['reasons'].append('referenced-'+via)

    # Sealed data is indivisible. Only known derived-only manifests can be disposable.
    for name, paths in members.items():
        for path in paths:
            if path.name == 'manifest.json' and path.parent == repo/name and path in texts:
                try:
                    manifest = decode(raw_texts[path])
                    files = manifest['files']
                    require(isinstance(files, dict), 'invalid manifest')
                    if not files or not set(files).issubset(DERIVED):
                        units[name]['reasons'].append('sealed-evidence')
                    else:
                        for rel, expected in files.items():
                            target = path.parent/rel
                            require(target in raw_texts and isinstance(expected, dict), 'missing derived report member')
                            data = raw_texts[target].encode('utf-8')
                            require(expected.get('bytes') == len(data) and expected.get('sha256') == hashlib.sha256(data).hexdigest(),
                                    'derived report manifest mismatch')
                except (OSError, ValueError, KeyError, TypeError):
                    issues.append(f'invalid evidence manifest: {name}')
        for path in paths:
            if path.suffix in {'.log', '.txt', '.stderr'} and re.search(
                    r'(?m)^FAIL(?:\s|$)|^--- FAIL:|minimum free space|^Traceback \(most recent call last\):|^mode=\w+ exit=[1-9]',
                    raw_texts.get(path, '')):
                units[name]['reasons'].append('failure-log')

    newest = {}
    for name, paths in members.items():
        if not name.startswith('var/ci/'):
            continue
        summary = next((p for p in paths if p.name == 'summary.txt' and p.parent == repo/name), None)
        text = texts.get(summary, '')
        if not re.search(r'^mode=(portable|native) exit=0$', text, re.M):
            units[name]['reasons'].append('failed-or-incomplete-ci')
        mode = Path(name).name.split('.')[0]
        stamp = initial[summary].st_mtime_ns if summary else initial[repo/name].st_mtime_ns
        if mode not in newest or (stamp, name) > newest[mode]:
            newest[mode] = (stamp, name)
    for _, name in newest.values():
        units[name]['reasons'].append('latest-ci-mode')

    try:
        require({p: token(s) for p, s in walk(var).items()} == {p: token(s) for p, s in initial.items()},
                'var changed during inventory')
        require(all(token(p.lstat()) == state for p, state in checks.items()), 'reference changed during inventory')
        require(git_sources(repo) == sources, 'Git source inventory changed')
    except (OSError, ValueError, subprocess.SubprocessError):
        issues.append('inputs changed or could not be rechecked; all candidates withheld')
    issues = sorted(set(issues))
    for name, item in units.items():
        item['reasons'] = sorted(set(item['reasons']))
        item['references'] = sorted({(v['target'], v['via']) for v in item['references']})
        item['references'] = [{'target': t, 'via': v} for t, v in item['references']]
        temporary = any(fnmatch.fnmatchcase(name, pattern) for pattern in policy['temporary'])
        item['disposition'] = 'protected' if item['reasons'] else 'candidate' if temporary and not issues else 'review'
        if not item['reasons']:
            item['reasons'] = ['scan-incomplete'] if issues else ['unreferenced-temporary-policy'] if temporary else ['unclassified-retain-for-review']
    disk = shutil.disk_usage(repo)
    return {'version': 1, 'kind': 'artifact-retention-preview', 'readOnly': True, 'repo': str(repo),
            'generatedUTC': datetime.now(timezone.utc).isoformat(),
            'scannerSHA256': hashlib.sha256(Path(__file__).read_bytes()).hexdigest(),
            'policy': policy, 'scanComplete': not issues, 'issues': issues,
            'scope': 'concrete source/metadata paths, bounded path braces and metadata SHA-256; no raw sample or binary content scan',
            'capacity': {'logicalBytes': sum(u['logicalBytes'] for u in units.values()),
                         'allocatedBytesUniqueInodes': total_allocated, 'filesystemFreeBytes': disk.free,
                         'candidateAllocatedBytesUpperBound': sum(u['allocatedBytes'] for u in units.values() if u['disposition'] == 'candidate')},
            'units': sorted(units.values(), key=lambda u: u['path']),
            'edges': [{'source': s, 'target': t, 'via': v} for s, t, v in sorted(edges)]}


def markdown(report):
    """Render capacity, preservation reasons and candidates as a review table."""
    def cell(text):
        return str(text).replace('|', '\\|').replace('\n', ' ').replace('\r', ' ').replace('<', '&lt;').replace('>', '&gt;')
    capacity = report['capacity']
    lines = ['# var 보관·정리 미리보기', '',
             f"스캔 완료: {report['scanComplete']} · 실제 삭제: 없음", '',
             f"논리 용량 {capacity['logicalBytes']:,} bytes · inode 중복을 제외한 할당 {capacity['allocatedBytesUniqueInodes']:,} bytes · 파일 시스템 여유 {capacity['filesystemFreeBytes']:,} bytes", '',
             '할당 수치는 APFS clone/공유 블록의 실제 회수 공간을 보장하지 않는다. candidate도 재검토할 삭제 예정 목록이며 자동 삭제하지 않는다.', '',
             '## 삭제 후보', '', '| 경로 | 논리 bytes | 할당 bytes | 사유 |', '| --- | ---: | ---: | --- |']
    for unit in report['units']:
        if unit['disposition'] == 'candidate':
            lines.append(f"| {cell(unit['path'])} | {unit['logicalBytes']} | {unit['allocatedBytes']} | {cell(', '.join(unit['reasons']))} |")
    if not any(u['disposition'] == 'candidate' for u in report['units']):
        lines.append('| 없음 | 0 | 0 | 보호/참조 또는 검토 필요 |')
    lines += ['', '## 전체 항목', '', '| 경로 | 분류 | 논리 bytes | 할당 bytes | 보존/판정 사유 | 참조 수 |', '| --- | --- | ---: | ---: | --- | ---: |']
    for unit in report['units']:
        lines.append(f"| {cell(unit['path'])} | {unit['disposition']} | {unit['logicalBytes']} | {unit['allocatedBytes']} | {cell(', '.join(unit['reasons']))} | {len(unit['referencedBy'])} |")
    lines += ['', '## 참조 관계', '', '| 참조 파일 | 대상 단위 | 방식 |', '| --- | --- | --- |']
    lines += [f"| {cell(e['source'])} | {cell(e['target'])} | {e['via']} |" for e in report['edges']]
    if report['issues']:
        lines += ['', '## 스캔 제한 — 후보 제외', ''] + ['- '+cell(x) for x in report['issues']]
    return '\n'.join(lines)+'\n'


def publish(repo, policy, output, policy_path=None):
    """Write a new preview bundle outside evidence roots, never overwrite input."""
    repo = Path(repo).resolve()
    output = Path(output).absolute()
    require('..' not in output.parts, 'output must not contain parent traversal')
    require(not output.exists() and not output.is_symlink(), 'output already exists')
    for parent in output.parents:
        require(not parent.is_symlink(), 'symlink in output parent')
    if output.is_relative_to(repo):
        require(output.parent == repo/'var/artifact-reports', 'var reports must be direct children of var/artifact-reports')
    report = inspect(repo, policy, policy_path)
    if output.parent == repo/'var/artifact-reports':
        (repo/'var').mkdir(exist_ok=True)
        output.parent.mkdir(exist_ok=True)
    require(output.parent.is_dir(), 'output parent must exist')
    output.mkdir()
    save(output/'retention-report.json', report)
    save(output/'plan.json', {'version': 1, 'readOnly': True, 'scanComplete': report['scanComplete'],
                            'reportSHA256': hashlib.sha256((output/'retention-report.json').read_bytes()).hexdigest(),
                            'candidates': [u for u in report['units'] if u['disposition'] == 'candidate'],
                            'requiresFreshReview': True, 'issues': report['issues']})
    (output/'report.md').write_text(markdown(report))
    save(output/'completed.json', {'version': 1, 'filesSHA256': {
        name: hashlib.sha256((output/name).read_bytes()).hexdigest()
        for name in ('retention-report.json', 'plan.json', 'report.md')}})
    return report


def main():
    """Print an inventory or publish a new JSON/Markdown deletion preview."""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--repo', type=Path, default=Path(__file__).resolve().parents[2])
    parser.add_argument('--policy', type=Path, help='default: config/artifact-retention.json under repo')
    parser.add_argument('--output', type=Path, help='new report directory; without it, print JSON only')
    args = parser.parse_args()
    policy_path = args.policy or args.repo/'config/artifact-retention.json'
    data, _ = bounded_metadata(policy_path)
    policy = policy_value(decode(data.decode('utf-8')))
    report = publish(args.repo, policy, args.output, policy_path) if args.output else inspect(args.repo, policy, policy_path)
    print(json.dumps({'output': str(args.output), 'scanComplete': report['scanComplete'],
                      'candidates': sum(u['disposition'] == 'candidate' for u in report['units'])}
                     if args.output else report, ensure_ascii=False, indent=2))


if __name__ == '__main__':
    try:
        main()
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        print(f'artifacts: {error}', file=sys.stderr)
        sys.exit(1)
