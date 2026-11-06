"""CI smoke: portable timer plus native trace/C-IO recovery and semantic tamper rejection."""
import argparse
import json
from pathlib import Path
import subprocess
import shutil
import sys
import tempfile
from common import REPO, digest, read, save, seal


def cli(*args, success=True):
    result = subprocess.run([sys.executable, str(REPO/'tools/diagnostics/experiment.py'), *map(str, args)],
                            cwd=REPO, text=True, capture_output=True, timeout=180)
    if (result.returncode == 0) != success:
        raise RuntimeError(f'diagnostic smoke: {result.returncode}\n{result.stdout}\n{result.stderr}')
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--mode', choices=['portable', 'native'], required=True)
    parser.add_argument('--parent', type=Path, required=True)
    options = parser.parse_args()
    options.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix='diagnostic-smoke-', dir=options.parent) as temp:
        root = Path(temp)
        suite = 'io' if options.mode == 'native' else 'timer'
        bundle = root/'bundle'
        args = ['run', '--suite', suite, '--output', bundle]
        if options.mode == 'native':
            args += ['--archive', '--prune-archived']
        cli(*args)
        cli('verify', '--bundle', bundle, '--output', root/'verified.json')
        cli('analyze', '--bundle', bundle, '--output', root/'analysis.json')
        if options.mode == 'native':
            # The standalone raw checker accepts any directory name, not only
            # the suite's conventional "loadgen" child.
            renamed = root/'renamed-input'
            shutil.copytree(bundle/'on-1/loadgen', renamed)
            checked = subprocess.run([str(bundle/'bin/experimentcheck'), str(renamed),
                                      str(root/'renamed-analysis.json')],
                                     capture_output=True, text=True, timeout=60)
            if checked.returncode != 0:
                raise RuntimeError(f'renamed raw input failed: {checked.stderr}')
        before = digest(bundle/'manifest.json')
        cli(*args, success=False)
        if digest(bundle/'manifest.json') != before:
            raise RuntimeError('reused output changed sealed bundle')
        # Rehash both seals after changing meaning: ordinary hash verification
        # succeeds, while analysis must still reject the contradictory evidence.
        if options.mode == 'native':
            case = bundle/'on-1/loadgen'
            summary = read(case/'summary.json')
            summary['measurement']['started'] = 0
            (case/'summary.json').write_text(json.dumps(summary))
            if (case/'completed.json').exists():
                completed = read(case/'completed.json')
                completed['sha256']['summary.json'] = digest(case/'summary.json')
                (case/'completed.json').write_text(json.dumps(completed))
        else:
            case = bundle/'only-off-1'
            rows = (case/'samples.jsonl').read_text().splitlines()
            row = json.loads(rows[0])
            row['workerNanos'] = 0  # Timer-only mode cannot have a worker.
            rows[0] = json.dumps(row)
            (case/'samples.jsonl').write_text('\n'.join(rows)+'\n')
        (bundle/'manifest.json').unlink()
        (bundle/'completed.json').unlink()
        seal(bundle, True)
        cli('verify', '--bundle', bundle, '--output', root/'rehashed.json')
        cli('analyze', '--bundle', bundle, '--output', root/'must-not-pass.json', success=False)
        if (root/'must-not-pass.json').exists():
            raise RuntimeError('failed analysis emitted a successful report')
    print(f'diagnostic {options.mode} smoke passed: fresh run, verify, analyze, no-reuse, semantic tamper')


if __name__ == '__main__':
    main()
