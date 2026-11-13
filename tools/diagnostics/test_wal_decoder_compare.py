"""Fixed-state pair completeness, exact build scope, and immutable recovery checks."""
import copy
from pathlib import Path
import shutil
import tempfile
import unittest

from common import digest, read, save, seal, verify
import test_state_scale
from wal_decoder_compare import analyze, build_pair, markdown, schedule


class WALDecoderComparisonTests(unittest.TestCase):
    def fixture(self, root):
        source=root/'source';source.mkdir()
        test_state_scale.StateScaleTests().fixture(source)
        (root/'builds').mkdir();(root/'fixtures').mkdir();(root/'runs').mkdir()
        for side in ('baseline','candidate'):
            dest=root/'builds'/side;dest.mkdir()
            (dest/'statescale').write_text('not executed '+side)
            (dest/'source.tar.gz').write_text('not extracted')
            save(dest/'build.json',{'label':side,'binarySHA256':digest(dest/'statescale'),'sourceArchiveSHA256':digest(dest/'source.tar.gz'),
                 'go':{'GOVERSION':'go1.27.3'},'sourceSHA256':{'internal/wal/codec.go':side,'internal/runtime/owner.go':'same','tools/statescale/main.go':'same'}})
            seal(dest,True)
        save(root/'protocol.json',{'version':1,'smoke':True,'order':schedule(True),'runtimeEnvironment':{'GOMAXPROCS':'1'},'sourceBundleManifestSHA256':'source-sha'})
        for kind in ('active','history'):
            shutil.copytree(source/'fixtures'/(kind+'-12'),root/'fixtures'/kind)
            f=read(source/'fixtures'/(kind+'-12.json'));save(root/'fixtures'/(kind+'.json'),f)
        for i,row in enumerate(schedule(True)):
            src={'activewal':0,'activesnapshot':1,'historywal':2,'historysnapshot':3}[row['kind']+row['mode']]
            target=root/'runs'/f'{i:03d}';target.mkdir()
            shutil.copytree(source/'runs'/f'{src:03d}'/'run',target/'run')
            save(target/'case.json',row)
            o=read(source/'runs'/f'{src:03d}'/'result.json')
            if row['version']=='candidate':o['Open']['WallNanos']=1000000
            save(target/'result.json',o)

    def test_predeclared_pairs_have_five_per_state_path_version(self):
        rows=schedule()
        self.assertEqual(len(rows),40)
        self.assertEqual(len({tuple(r.values()) for r in rows}),40)
        for kind in ('active','history'):
            for mode in ('wal','snapshot'):
                for side in ('baseline','candidate'):
                    self.assertEqual(sum(r['kind']==kind and r['mode']==mode and r['version']==side for r in rows),5)
        self.assertEqual(rows[0]['version'],'baseline')
        self.assertEqual(rows[8]['version'],'candidate')

    def test_readonly_complete_analysis_and_pairing(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root);seal(root,True)
            before=digest(root/'manifest.json')
            r=analyze(root)
            self.assertEqual(r['workersVerified'],8)
            self.assertEqual(len(r['rows']),4)
            self.assertEqual(r['rows'][0]['medianChangePercent']['openMs'],-50)
            self.assertEqual(r['rows'][0]['pairs'][0]['openMsChangePercent'],-50)
            self.assertIn('전원 장애',markdown(r))
            verify(root);self.assertEqual(before,digest(root/'manifest.json'))

    def test_missing_mislabeled_or_changed_state_is_rejected(self):
        for fault in ('missing','case','state','verified'):
            with self.subTest(fault=fault),tempfile.TemporaryDirectory() as temp:
                root=Path(temp);self.fixture(root)
                p=root/'runs/000'
                if fault=='missing':shutil.rmtree(p)
                if fault=='case':
                    case=read(p/'case.json');case['version']='candidate';(p/'case.json').write_text(__import__('json').dumps(case))
                if fault=='state':(p/'run/commands.wal').write_text('changed')
                if fault=='verified':
                    o=read(p/'result.json');o['Verified']=False;(p/'result.json').write_text(__import__('json').dumps(o))
                with self.assertRaises((ValueError,FileNotFoundError)):analyze(root)

    def test_comparison_cannot_hide_other_production_or_probe_changes(self):
        for name in ('internal/runtime/owner.go','tools/statescale/main.go'):
            with self.subTest(name=name),tempfile.TemporaryDirectory() as temp:
                root=Path(temp);self.fixture(root)
                p=root/'builds/candidate'
                b=read(p/'build.json');b['sourceSHA256'][name]='changed'
                (p/'build.json').write_text(__import__('json').dumps(b))
                (p/'manifest.json').unlink();(p/'completed.json').unlink();seal(p,True)
                with self.assertRaises(ValueError):build_pair(root)


if __name__=='__main__':unittest.main()
