"""Paired array comparison schedule, memory contract and evidence retirement."""
from pathlib import Path
import tempfile
import unittest

from common import digest, read, save
from snapshot_array_compare import expected_names, markdown, retire_copy, schedule, validate_receipt, validate_resident


class ArrayCompareTests(unittest.TestCase):
    def test_sixty_workers_five_per_condition_and_balanced_pair_order(self):
        rows=schedule();self.assertEqual(len(rows),60)
        self.assertEqual(len({tuple(r.values()) for r in rows}),60)
        for kind in ('active','history'):
            for tail in (0,1000,5000):
                for side in ('baseline','candidate'):
                    self.assertEqual(sum(r['kind']==kind and r['tail']==tail and r['version']==side and r['commands']==10000 for r in rows),5)
        self.assertEqual(sum(rows[i]['version']=='baseline' for i in range(0,60,2)),15)
        for i in range(0,60,2):
            self.assertEqual({**rows[i], 'version':'pair'}, {**rows[i+1],'version':'pair'})
            self.assertNotEqual(rows[i]['version'],rows[i+1]['version'])
        self.assertEqual(len(schedule(True)),12)

    def scratch(self,root):
        row=schedule(True)[4];case=root/'case';case.mkdir();(case/'run').mkdir();fixtures=root/'fixtures';fixtures.mkdir();f={'Files':{}}
        for name in expected_names(row):
            (fixtures/name).write_text(name);(case/'run'/name).write_text(name);f['Files'][name]=digest(fixtures/name)
        save(case/'run/.tail-owned',row);save(case/'case.json',row)
        for name in ('result.json','probe.stdout','probe.stderr'):(case/name).write_text('saved')
        retire_copy(case,row,f);return case,row,f,fixtures

    def test_receipt_checks_full_and_prefix_snapshot_after_owned_retirement(self):
        with tempfile.TemporaryDirectory() as temp:
            case,row,f,fixtures=self.scratch(Path(temp));validate_receipt(case,row,f,fixtures)
            self.assertFalse((case/'run').exists());self.assertEqual(len(expected_names(row)),5)
            self.assertTrue(all((fixtures/name).exists() for name in f['Files']))

    def test_receipt_rejects_missing_result_extra_copy_or_changed_owner_hash_size(self):
        for fault in ('missing','extra','owner','hash','size','policy'):
            with self.subTest(fault=fault),tempfile.TemporaryDirectory() as temp:
                case,row,f,fixtures=self.scratch(Path(temp));p=case/'verified-files.json';r=read(p);p.unlink()
                if fault=='missing':(case/'result.json').unlink()
                if fault=='extra':(case/'run').mkdir()
                if fault=='owner':r['owner']['repeat']=99
                if fault=='hash':r['files']['commands.wal']['sha256']='changed'
                if fault=='size':r['files']['commands.wal']['bytes']+=1
                if fault=='policy':r['policy']='changed'
                save(p,r)
                with self.assertRaises(ValueError):validate_receipt(case,row,f,fixtures)

    def test_current_rss_and_live_heap_are_required_and_bounded(self):
        for rss in (None,False,0,-1,2*2**30+1,'100'):
            with self.subTest(rss=rss),self.assertRaises(ValueError):
                validate_resident({'RSSAfterGCBytes':rss,'Open':{'AfterGC':{'HeapAlloc':1024}}})
        validate_resident({'RSSAfterGCBytes':1024,'Open':{'AfterGC':{'HeapAlloc':512}}})
        with self.assertRaises(ValueError):
            validate_resident({'RSSAfterGCBytes':1024,'Open':{'AfterGC':{'HeapAlloc':2*2**30+1}}})

    def test_markdown_contains_live_heap_and_peak_rss_limits(self):
        text=markdown({'repeats':5,'workersVerified':60,'rows':[]})
        for expected in ('TotalAlloc','GC 뒤 heap','peak RSS','2GiB','미완료·보류','warm-cache'):
            self.assertIn(expected,text)
