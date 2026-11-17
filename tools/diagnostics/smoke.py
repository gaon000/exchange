"""CI smoke: portable timer plus native trace/C-IO recovery and semantic tamper rejection."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import shutil
import sys
import tempfile
from common import REPO, digest, read, save, seal
from evidence import create, restore, reproduce
from state_scale import run as state_scale


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
            state_scale(root/'state-scale-smoke', smoke=True)
            args += ['--archive', '--prune-archived']
        cli(*args)
        cli('verify', '--bundle', bundle, '--output', root/'verified.json')
        cli('analyze', '--bundle', bundle, '--output', root/'analysis.json')
        spec = root/'comparison-spec.json'
        selector = {'bundle': 'bundle', 'case': 'on-1' if options.mode == 'native' else 'only-off-1'}
        save(spec, {'version': 1, 'comparisons': [{'name': 'same retained run',
                                                 'baseline': [selector], 'candidate': [selector]}]})
        cli('compare', '--spec', spec, '--output', root/'comparison')
        cli('verify', '--bundle', root/'comparison', '--output', root/'comparison-verified.json')
        comparison = read(root/'comparison/report.json')['comparisons'][0]
        if (comparison['baseline'][0]['status'] != 'verified' or comparison['candidate'][0]['status'] != 'verified'
                or comparison['judgement']['eligibleForReview']
                or 'SHARED_RUNS_BETWEEN_SIDES' not in comparison['judgement']['blockers']
                or not (root/'comparison/report.md').is_file()):
            raise RuntimeError('retained-input comparison or duplicate-run rejection failed')
        if options.mode == 'native':
            transport_spec = root/'transport-spec.json'
            entries = [{'name': 'fresh loadgen', 'kind': 'loadgen', 'bundle': str(bundle), 'case': 'on-1'}]
            if os.environ.get('EXCHANGE_SCENARIO_EVIDENCE_PARENT'):
                fixtures = list(options.parent.glob('random-recovery-TestNativeTransportLossRecoveryAndReplay-*/loss'))
                if len(fixtures) != 1:
                    raise RuntimeError('missing native transport loss fixture')
                entries.append({'name': 'actual response loss', 'kind': 'recoverycheck', 'bundle': str(fixtures[0])})
            save(transport_spec, {'version': 1, 'inputs': entries})
            cli('transport-report', '--spec', transport_spec, '--output', root/'transport-report')
            cli('verify', '--bundle', root/'transport-report', '--output', root/'transport-verified.json')
            transport = read(root/'transport-report/report.json')
            if transport['interpretation']['measurementReclassified'] or transport['interpretation']['unknown404ProvesNonAdmission']:
                raise RuntimeError('transport report changed certainty contract')
            if len(entries) == 2:
                fault = transport['inputs'][1]
                if (fault['counts']['unknownAttempts'] != 3 or fault['counts']['keysWithSubsequentResult'] != 3
                        or any(a['requestLatencyNanos'] is None for a in fault['attempts'])
                        or not any(q['httpStatus'] == 404 and q['resolution'] == 'UNRESOLVED'
                                   for c in fault['unknownCommands'] for q in c['queries'])):
                    raise RuntimeError('native transport report lost failure/404/query evidence')
            cli('transport-report', '--spec', transport_spec, '--output', root/'transport-report', success=False)
            # A resealed plan can still have valid sample aggregates. Verify
            # attribution against Build instead of trusting those plan bytes.
            tampered = root/'plan-tampered'
            data = tampered/'on-1/loadgen'
            shutil.copytree(bundle/'on-1/loadgen', data)
            plan_rows = (data/'plan.jsonl').read_text().splitlines()
            row = json.loads(plan_rows[-1])
            row['phase'] = 'PREPARE'
            plan_rows[-1] = json.dumps(row)
            (data/'plan.jsonl').write_text('\n'.join(plan_rows)+'\n')
            if (data/'completed.json').exists():
                completed = read(data/'completed.json')
                completed['sha256']['plan.jsonl'] = digest(data/'plan.jsonl')
                (data/'completed.json').write_text(json.dumps(completed))
            seal(tampered, True)
            bad_spec = root/'plan-tampered-spec.json'
            save(bad_spec, {'version': 1, 'inputs': [{'name': 'bad plan', 'kind': 'loadgen',
                                                    'bundle': str(tampered), 'case': 'on-1'}]})
            cli('transport-report', '--spec', bad_spec, '--output', root/'plan-must-not-pass', success=False)
            if (root/'plan-must-not-pass').exists():
                raise RuntimeError('transport report accepted a resealed plan with changed attribution')
            print('transport report smoke passed: raw recomputation, failure/query separation, no-reuse', flush=True)
        # Package only this smoke's owned fixtures. Hide originals temporarily:
        # regeneration must use restored bytes, including archived native state.
        create(REPO, [], [spec], root/'package')
        restore(root/'package', root/'restored')
        bundle.rename(root/'original-unavailable')
        spec.rename(root/'spec-unavailable.json')
        try:
            reproduced = reproduce(root/'restored', root/'reproduced')
            if reproduced['status'] != 'PASSED' or len(reproduced['comparisons']) != 1:
                raise RuntimeError('isolated package reproduction failed')
        finally:
            (root/'original-unavailable').rename(bundle)
            (root/'spec-unavailable.json').rename(spec)
        if options.mode == 'native':
            print(json.dumps({'stage': 'host-observation', 'hostLifetime':
                              read(root/'analysis.json')['cases']['on-1']['hostLifetime']}), flush=True)
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
        if options.mode == 'native':
            cli('transport-report', '--spec', transport_spec, '--output', root/'transport-invalid', success=False)
            if (root/'transport-invalid').exists():
                raise RuntimeError('transport report published corrupt raw evidence')
        cli('compare', '--spec', spec, '--output', root/'comparison-invalid')
        comparison = read(root/'comparison-invalid/report.json')['comparisons'][0]
        if (comparison['baseline'][0]['status'] != 'error' or comparison['candidate'][0]['status'] != 'error'
                or comparison['judgement']['eligibleForReview'] or comparison['judgement']['improvementClaimed']):
            raise RuntimeError('comparison accepted semantically corrupted evidence')
    print(f'diagnostic {options.mode} smoke passed: fresh run, isolated package restore/reproduce, no-reuse, semantic tamper')


if __name__ == '__main__':
    main()
