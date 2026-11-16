"""Compare Int JSON decoding on sealed latest fixed-state recovery fixtures."""
import argparse
import json
from pathlib import Path
import sys

from common import read, require, save, seal, verify
import snapshot_array_compare as paired
from wal_decoder_compare import freeze, new_output

CHANGES=('internal/domain/integer.go',)
TITLE='정수 JSON 디코딩 비교'


def analyze(root):
    """Validate both frozen builds and every recovery/memory observation."""
    require(read(root/'protocol.json').get('inputKind')=='canonical-buffer','integer comparison producer differs')
    return paired.analyze(root,CHANGES)


def run(bundle,baseline,candidate,output,smoke=False):
    """Run separate unprofiled workers; retain immutable inputs and receipts."""
    return paired.run(bundle,baseline,candidate,output,smoke,changes=CHANGES,input_kind='canonical-buffer',title=TITLE)


def main():
    p=argparse.ArgumentParser(description=__doc__);sub=p.add_subparsers(dest='command',required=True)
    f=sub.add_parser('freeze');f.add_argument('--label',choices=('baseline','candidate'),required=True);f.add_argument('--output',type=Path,required=True)
    r=sub.add_parser('run')
    for name in ('bundle','baseline','candidate','output'):r.add_argument('--'+name,type=Path,required=True)
    r.add_argument('--smoke',action='store_true')
    a=sub.add_parser('analyze');a.add_argument('--bundle',type=Path,required=True);a.add_argument('--output',type=Path,required=True)
    args=p.parse_args()
    if args.command=='freeze':freeze(args.output,args.label);print('frozen');return
    if args.command=='run':report=run(args.bundle,args.baseline,args.candidate,args.output,args.smoke)
    else:
        root=args.bundle.resolve();verify(root);report=analyze(root);out=new_output(args.output,[root])
        save(out/'report.json',report);(out/'report.md').write_text(paired.markdown(report,TITLE));seal(out,True)
    print(json.dumps({'status':'PASSED','workersVerified':report['workersVerified']}))


if __name__=='__main__':
    try:main()
    except (Exception,KeyboardInterrupt) as error:print(f'integer-json-compare: {error}',file=sys.stderr);sys.exit(1)
