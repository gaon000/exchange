"""Snapshot-only production scope and creation-focused read-only reporting."""
import json
from pathlib import Path
import tempfile
import unittest

from common import digest, read, seal, verify
from snapshot_leaf_compare import CHANGES, markdown
import test_wal_decoder_compare
from wal_decoder_compare import analyze, build_pair


class SnapshotLeafComparisonTests(unittest.TestCase):
    def fixture(self, root):
        test_wal_decoder_compare.WALDecoderComparisonTests().fixture(root)
        for side in ('baseline','candidate'):
            path=root/'builds'/side
            b=read(path/'build.json')
            b['sourceSHA256']['internal/wal/codec.go']='same-wal'
            b['sourceSHA256']['internal/snapshot/codec.go']=side
            (path/'build.json').write_text(json.dumps(b))
            (path/'manifest.json').unlink();(path/'completed.json').unlink();seal(path,True)
        for path in (root/'runs').iterdir():
            if read(path/'case.json')['version']=='candidate':
                o=read(path/'result.json');o['Snapshot']['WallNanos']=1000000
                (path/'result.json').write_text(json.dumps(o))

    def test_snapshot_scope_cannot_be_used_for_wal_changes(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root)
            build_pair(root,CHANGES)
            with self.assertRaises(ValueError):build_pair(root)
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);test_wal_decoder_compare.WALDecoderComparisonTests().fixture(root)
            build_pair(root)
            with self.assertRaises(ValueError):build_pair(root,CHANGES)

    def test_creation_report_uses_wal_path_and_preserves_input(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root);seal(root,True)
            before=digest(root/'manifest.json')
            report=analyze(root,CHANGES)
            text=markdown(report)
            self.assertEqual(report['workersVerified'],8)
            self.assertIn('2.000 [2.000~2.000] | 1.000 [1.000~1.000] | -50.00',text)
            self.assertIn('기존 파일 재확인',text)
            self.assertIn('전원 장애',text)
            verify(root);self.assertEqual(before,digest(root/'manifest.json'))


if __name__=='__main__':unittest.main()
