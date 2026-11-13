"""Latest profile identity, baseline isolation and immutable preparation receipt."""
import copy
from pathlib import Path
import tempfile
import unittest

from canonical_buffer import markdown, paired, production, profile_order, retire_profile, validate_profile, verify_prepared
from common import digest,save


class CanonicalBufferTests(unittest.TestCase):
    def test_latest_allocation_profile_uses_five_fresh_workers_per_kind(self):
        rows=profile_order(False);self.assertEqual(len(rows),10)
        self.assertEqual(len({tuple(r.values()) for r in rows}),10)
        for kind in ('active','history'):
            self.assertEqual(sum(r['kind']==kind and r['commands']==10000 and r['tail']==0 and r['profiler']=='alloc' and r['version']=='baseline' for r in rows),5)
        self.assertEqual([r['kind'] for r in rows[:4]],['active','history','history','active'])
        self.assertEqual(len(profile_order(True)),2);self.assertEqual(len(paired.schedule()),60)

    def test_production_comparison_includes_new_files_and_module_but_not_tests(self):
        s={'internal/snapshot/decode.go':'a','internal/new.go':'b','cmd/exchange/main.go':'c','go.mod':'d','.go-version':'e','internal/test_test.go':'ignored','tools/statescale/profile.go':'probe'}
        self.assertEqual(set(production(s)),set(s)-{'internal/test_test.go','tools/statescale/profile.go'})

    def observation(self):
        row=profile_order(False)[0];name='snapshot-00000000000000010000.bin'
        o={'Version':1,'Verified':True,'Phase':'snapshot-open','Profiler':'alloc','Commands':10000,'GoVersion':'go1.27.3','GOMAXPROCS':11,'MemProfileRate':65536,
           'Recovery':{'SnapshotSeq':'10000','ReplayedCommands':'0','SnapshotFile':name,'Skipped':[]},'SnapshotSHA256':'snapshot','WALSHA256':'wal','Before':{'TotalAlloc':1},'After':{'TotalAlloc':2}}
        return o,row,{'Files':{name:'snapshot','commands.wal':'wal'}},{'go':{'GOVERSION':'go1.27.3'}},{'GOMAXPROCS':'11'}

    def test_profile_requires_exact_path_state_sampling_and_runtime(self):
        args=self.observation();validate_profile(*args)
        for key,value in [('Version',2),('Verified',False),('Phase','wal-open'),('Profiler','cpu'),('Commands',24),('GoVersion','other'),('GOMAXPROCS',1),('MemProfileRate',524288),('SnapshotSHA256','changed'),('WALSHA256','changed')]:
            changed=list(copy.deepcopy(args));changed[0][key]=value
            with self.subTest(key=key),self.assertRaises(ValueError):validate_profile(*changed)
        for key,value in [('SnapshotSeq','9000'),('ReplayedCommands','1000'),('SnapshotFile','changed'),('Skipped',['fallback'])]:
            changed=list(copy.deepcopy(args));changed[0]['Recovery'][key]=value
            with self.subTest(key=key),self.assertRaises(ValueError):validate_profile(*changed)

    def test_prepared_receipt_rejects_changed_baseline_profile_or_fixture(self):
        for name in ('build','profile','fixture'):
            with tempfile.TemporaryDirectory() as temp:
                root=Path(temp)
                for f in ('build','profile','fixture'):(root/f).write_text(f)
                save(root/'prepared.json',{'version':1,'files':{f:digest(root/f) for f in ('build','profile','fixture')}})
                verify_prepared(root);(root/name).write_text('changed')
                with self.assertRaises(ValueError):verify_prepared(root)

    def test_profile_retirement_refuses_changed_owner_and_preserves_files(self):
        with tempfile.TemporaryDirectory() as temp:
            case=Path(temp);run=case/'run';run.mkdir();row=profile_order(True)[0]
            save(run/'.statescale-owned',{**row,'repeat':99})
            (run/'unexpected').write_text('preserve')
            with self.assertRaises(ValueError):retire_profile(case,row,{'Files':{}})
            self.assertTrue((run/'unexpected').exists());self.assertTrue((run/'.statescale-owned').exists())
            self.assertFalse((run/'.tail-owned').exists())

    def test_shared_memory_table_retains_current_rss_and_budget(self):
        text=markdown({'repeats':5,'workersVerified':60,'rows':[]})
        for s in ('정규형 비교 버퍼','GC 뒤 RSS','peak RSS','2GiB','미완료·보류'):self.assertIn(s,text)
