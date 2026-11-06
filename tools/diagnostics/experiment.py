#!/usr/bin/env python3
"""Run, verify and analyze reproducible local diagnostics without retained var scripts."""
import argparse
import json
import sys
from pathlib import Path
from common import read, require, save, verify


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
    compare = commands.add_parser('compare', help='reverify retained inputs and write JSON/Markdown comparison')
    compare.add_argument('--spec', type=Path, required=True)
    compare.add_argument('--output', type=Path, required=True, help='new report directory outside every input bundle')
    baseline = commands.add_parser('baseline', help='owned host observations and sequential P6 reference measurements')
    baseline.add_argument('--build-dir', type=Path, required=True, help='clean frozen build directory with build.json and bin/')
    baseline.add_argument('--output', type=Path, required=True)
    baseline.add_argument('--preset', choices=['smoke', 'reference'], default='smoke')
    baseline.add_argument('--normal-rate', type=int, help='reference only: 1000..80000, step 1000; default 29000')
    baseline.add_argument('--snapshot-rate', type=int, help='reference only: 1000..80000, step 1000; default 3000')
    baseline.add_argument('--resume-from', type=Path, help='preserve completed prefix after a quiet-gate-only interruption')
    baseline.add_argument('--condition', choices=['both', 'normal', 'snapshot'], default='both', help='reference scenarios to measure')
    baseline.add_argument('--quiet-attempts', type=int, default=3, help='1..60 observations before stopping; thresholds unchanged')
    frozen = commands.add_parser('baseline-build', help='freeze clean producer binaries before recording a baseline')
    frozen.add_argument('--output', type=Path, required=True)
    for name in ['verify', 'analyze']:
        item = commands.add_parser(name)
        item.add_argument('--bundle', type=Path, required=True)
        item.add_argument('--output', type=Path, required=True, help='new JSON outside the input bundle')
        if name == 'verify':
            item.add_argument('--allow-failed', action='store_true', help='check sealed partial data, never mark completed')
    options = parser.parse_args()
    if options.command == 'baseline-build':
        from baseline import freeze
        print(json.dumps({'buildDirectory': str(freeze(options.output)), 'status': 'frozen'}))
    elif options.command == 'baseline':
        from baseline import run as run_baseline
        print(json.dumps({'bundle': str(run_baseline(options.output, options.build_dir, options.preset,
                                                    options.normal_rate, options.snapshot_rate, options.resume_from,
                                                    options.condition, options.quiet_attempts)), 'status': 'collected'}))
    elif options.command == 'compare':
        from comparison import compare as compare_report
        result = compare_report(options.spec, options.output)
        print(json.dumps({'reportDirectory': str(options.output.resolve()), 'status': 'reported',
                          'eligibleComparisons': sum(g['judgement']['eligibleForReview'] for g in result['comparisons'])}))
    elif options.command == 'run':
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
            if (root/'protocol.json').is_file() and read(root/'protocol.json').get('resumeFrom'):
                from baseline import verify_dependencies
                result['baselineDependencies'] = verify_dependencies(root)
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
