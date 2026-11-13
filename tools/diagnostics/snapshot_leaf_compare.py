"""Compare snapshot leaf serialization using frozen probes and identical state."""
import argparse
import json
from pathlib import Path
import sys

from common import save, seal, verify
from wal_decoder_compare import analyze, freeze, new_output, run

CHANGES = ('internal/snapshot/codec.go',)


def markdown(report):
    cell = lambda x: f"{x['median']:.3f} [{x['min']:.3f}~{x['max']:.3f}]"
    text = ['# 스냅샷 항목 직렬화 비교', '', f"각 버전 {report['repeats']}회 중앙값 [최소~최대], {report['workersVerified']} worker 상태 일치.", '',
            '| 상태 | 이전 새 생성 ms | 이후 새 생성 ms | 변화 % | 이전 생성 할당 MiB | 이후 생성 할당 MiB | 변화 % |',
            '| --- | --- | --- | ---: | --- | --- | ---: |']
    for row in report['rows']:
        if row['mode'] != 'wal':continue
        a,b = row['versions']['baseline'],row['versions']['candidate']
        change = lambda name: (b[name]['median']/a[name]['median']-1)*100
        text.append(f"| {row['kind']} | {cell(a['snapshotMs'])} | {cell(b['snapshotMs'])} | {change('snapshotMs'):.2f} | {cell(a['snapshotAllocMiB'])} | {cell(b['snapshotAllocMiB'])} | {change('snapshotAllocMiB'):.2f} |")
    text += ['', '| 상태 | 복구 | 이전 Open ms | 이후 Open ms | 이전 할당 MiB | 이후 할당 MiB |',
             '| --- | --- | --- | --- | --- | --- |']
    for row in report['rows']:
        a,b = row['versions']['baseline'],row['versions']['candidate']
        text.append(f"| {row['kind']} | {row['mode']} | {cell(a['openMs'])} | {cell(b['openMs'])} | {cell(a['openAllocMiB'])} | {cell(b['openAllocMiB'])} |")
    text += ['', '새 snapshot 생성은 WAL 복구 후 첫 publication이다. snapshot 복구의 기존 파일 재확인과 구분한다.',
             '준비/사본/해시/강제 GC/검증은 wall 시간 밖이며 자연 GC·I/O 대기는 포함한다. CPU/할당 profile은 켜지 않는다.',
             '같은 입력·새 worker·warm-cache·비격리 호스트, 느린 표본 포함. HTTP p99/최대 처리량·실제 전원 장애 검증과 구분한다.',
             '실제 OS·전원 장애 검증은 미완료·보류다.', '']
    return '\n'.join(text)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    f = sub.add_parser('freeze')
    f.add_argument('--label', choices=('baseline','candidate'), required=True)
    f.add_argument('--output', type=Path, required=True)
    r = sub.add_parser('run')
    for name in ('bundle','baseline','candidate','output'):r.add_argument('--'+name, type=Path, required=True)
    r.add_argument('--smoke', action='store_true')
    a = sub.add_parser('analyze')
    a.add_argument('--bundle', type=Path, required=True)
    a.add_argument('--output', type=Path, required=True)
    args = parser.parse_args()
    if args.command == 'freeze':
        freeze(args.output, args.label);print('frozen');return
    if args.command == 'run':
        report = run(args.bundle,args.baseline,args.candidate,args.output,args.smoke,CHANGES,markdown)
    else:
        root = args.bundle.resolve();verify(root)
        report = analyze(root,CHANGES)
        out = new_output(args.output,[root])
        save(out/'report.json',report);(out/'report.md').write_text(markdown(report));seal(out,True)
    print(json.dumps({'status':'PASSED','workersVerified':report['workersVerified']}))


if __name__ == '__main__':
    try:main()
    except (Exception,KeyboardInterrupt) as error:
        print(f'snapshot-leaf-compare: {error}',file=sys.stderr);sys.exit(1)
