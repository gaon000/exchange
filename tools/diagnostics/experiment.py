#!/usr/bin/env python3
"""Run, verify and analyze reproducible local diagnostics without retained var scripts."""
import argparse
import json
import sys
from pathlib import Path
from common import require, save, verify


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    commands = parser.add_subparsers(dest='command', required=True)
    run = commands.add_parser('run', help='new smoke or explicit ten-minute suite')
    run.add_argument('--suite', choices=['soak', 'trace', 'io', 'timer'], required=True)
    run.add_argument('--preset', choices=['smoke', 'ten-minute'], default='smoke')
    run.add_argument('--output', type=Path, required=True)
    run.add_argument('--repeats', type=int, choices=range(1, 4))
    run.add_argument('--cooldown', type=int, choices=range(0, 601), metavar='SECONDS',
                     help='between cases: 0..600 (smoke 0, ten-minute 30)')
    run.add_argument('--archive', action='store_true', help='verify compressed state before optional pruning')
    run.add_argument('--prune-archived', action='store_true', help='remove only verified new state files')
    for name in ['verify', 'analyze']:
        item = commands.add_parser(name)
        item.add_argument('--bundle', type=Path, required=True)
        item.add_argument('--output', type=Path, required=True, help='new JSON outside the input bundle')
        if name == 'verify':
            item.add_argument('--allow-failed', action='store_true', help='check sealed partial data, never mark completed')
    options = parser.parse_args()
    if options.command == 'run':
        from runner import run_suite
        print(json.dumps({'bundle': str(run_suite(options)), 'status': 'completed'}))
    else:
        root, output = options.bundle.resolve(), options.output.absolute()
        require(not output.resolve().is_relative_to(root), 'report must be outside the input bundle')
        require(not output.exists() and not output.is_symlink(), 'report output already exists')
        if options.command == 'verify':
            result = verify(root, allow_failed=options.allow_failed)
            from analysis import verify_archives
            result['archives'] = verify_archives(root)
        else:
            from analysis import analyze_bundle
            result = analyze_bundle(root)
        save(output, result)
        print(json.dumps({'report': str(output), 'status': 'verified'}))


if __name__ == '__main__':
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        print(f'diagnostics: {error}', file=sys.stderr)
        sys.exit(1)
