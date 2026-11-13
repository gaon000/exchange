"""Integer comparison keeps a single production change and verified producers."""
from pathlib import Path
import unittest
from unittest.mock import patch

from integer_json_compare import CHANGES, TITLE, analyze, run
from snapshot_array_compare import input_producer


class IntegerJSONCompareTests(unittest.TestCase):
    def test_run_uses_existing_worker_memory_and_receipt_contract(self):
        paths=[Path('/not-executed')/name for name in ('input','baseline','candidate','output')]
        with patch('integer_json_compare.paired.run',return_value={'workersVerified':60}) as execute:
            self.assertEqual(run(*paths),{'workersVerified':60})
            execute.assert_called_once_with(*paths,False,changes=('internal/domain/integer.go',),input_kind='canonical-buffer',title=TITLE)

    def test_analyze_requires_latest_producer_and_exact_production_change(self):
        root=Path('/not-executed')
        with patch('integer_json_compare.read',return_value={'inputKind':'canonical-buffer'}),patch('integer_json_compare.paired.analyze',return_value={}) as execute:
            analyze(root);execute.assert_called_once_with(root,CHANGES)
        with patch('integer_json_compare.read',return_value={'inputKind':'snapshot-tail'}),patch('integer_json_compare.paired.analyze') as execute:
            with self.assertRaises(ValueError):analyze(root)
            execute.assert_not_called()

    def test_input_kind_and_size_select_only_validated_producer(self):
        root=Path('/not-executed')
        for kind,path in [('snapshot-tail',root/'build.json'),('canonical-buffer',root/'builds/candidate/build.json')]:
            def read_input(p):
                if p==root/'protocol.json':return {'smoke':False}
                self.assertEqual(p,path);return {'sourceSHA256':{'fixture':'expected'}}
            with self.subTest(kind=kind),patch('snapshot_array_compare.read',side_effect=read_input),patch('snapshot_array_compare.analyze_tail') as tail,patch('canonical_buffer.analyze') as canonical:
                self.assertEqual(input_producer(root,False,kind),{'sourceSHA256':{'fixture':'expected'}})
                (tail if kind=='snapshot-tail' else canonical).assert_called_once_with(root)
                self.assertEqual(tail.call_count+canonical.call_count,1)
        for smoke,kind in [(True,'canonical-buffer'),(False,'unsupported')]:
            with patch('snapshot_array_compare.read',return_value={'smoke':smoke}),patch('snapshot_array_compare.analyze_tail') as tail,patch('canonical_buffer.analyze') as canonical:
                with self.assertRaises(ValueError):input_producer(root,False,kind)
                tail.assert_not_called();canonical.assert_not_called()


if __name__=='__main__':unittest.main()
