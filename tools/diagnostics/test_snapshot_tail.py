"""Tail completeness, state identity and immutable evidence boundaries."""
from pathlib import Path
import subprocess
import tempfile
import unittest

from common import digest, read, save, seal, verify
from snapshot_tail import analyze, expected_counts, markdown, require_production_head, schedule, settings
from test_state_scale import stages


class SnapshotTailTests(unittest.TestCase):
    def fixture(self, root):
        n,tails,repeats=settings(True)
        order=schedule(True)
        save(root/'protocol.json',{'version':1,'smoke':True,'commands':n,'tails':tails,'repeats':repeats,
                                  'order':order,'runtimeEnvironment':{'GOMAXPROCS':'1'}})
        (root/'statescale').write_bytes(b'not executed')
        (root/'source.tar.gz').write_bytes(b'not extracted')
        save(root/'build.json',{'binarySHA256':digest(root/'statescale'),'sourceArchiveSHA256':digest(root/'source.tar.gz'),'go':{'GOVERSION':'go1.27.3'}})
        (root/'fixtures').mkdir();(root/'runs').mkdir()
        for kind in ('active','history'):
            source=root/'fixtures'/kind;source.mkdir();files={}
            for name in ['.lock','genesis.bin','commands.wal',*[f'snapshot-{n-t:020d}.bin' for t in tails]]:
                (source/name).write_text(kind+name);files[name]=digest(source/name)
            codes=['ACCEPTED','ACCEPTED','ACCEPTED','CANCELLED','CANCELLED','ALREADY_FILLED','ALREADY_CANCELLED','ORDER_NOT_FOUND']
            witnesses=[{'First':{'code':code,'commandSeq':str(seq),'order':{'status':'OPEN'}}} for code,seq in zip(codes,[1,2,*range(n-5,n+1)])]
            save(root/'fixtures'/(kind+'.json'),{'Version':1,'Kind':kind,'Commands':n,'Cuts':sorted(n-t for t in tails),
                                              'Counts':expected_counts(kind,n),'Files':files,'Witnesses':witnesses})
        for i,row in enumerate(order):
            path=root/'runs'/f'{i:03d}';path.mkdir();target=path/'run';target.mkdir()
            f=read(root/'fixtures'/(row['kind']+'.json'));cut=n-row['tail'];selected=f'snapshot-{cut:020d}.bin';final=f'snapshot-{n:020d}.bin'
            for name in {'.lock','genesis.bin','commands.wal',selected,final}:
                (target/name).write_bytes((root/'fixtures'/row['kind']/name).read_bytes())
            save(path/'case.json',row);save(target/'.tail-owned',row)
            o={'Version':1,'Kind':row['kind'],'Commands':n,'Tail':row['tail'],'Verified':True,'GoVersion':'go1.27.3','GOMAXPROCS':1,
               'FirstResponsesVerified':8,'RetriesVerified':2,'OrdersBalancesTradeVerified':True,
               'FinalSnapshotSHA256':f['Files'][final],'WALSHA256':f['Files']['commands.wal'],
               'Recovery':{'SnapshotSeq':str(cut),'SnapshotFile':selected,'ReplayedCommands':str(row['tail']),'Skipped':[],'Stages':stages()},
               'Open':{'WallNanos':2000000,'Before':{'HeapAlloc':1024,'TotalAlloc':100,'Mallocs':1},
                       'After':{'TotalAlloc':200,'Mallocs':2,'NumGC':1},'AfterGC':{'NumGC':2,'HeapAlloc':1800},'PeakRSSBytes':16*2**20}}
            save(path/'result.json',o)

    def test_production_guard_rejects_new_ignored_staged_and_modified_files(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);(root/'internal').mkdir();(root/'tools').mkdir()
            def git(*args):
                return subprocess.run(['git',*args],cwd=root,check=True,capture_output=True)
            git('init','-q')
            (root/'internal/existing.go').write_text('package existing\n')
            (root/'.gitignore').write_text('internal/ignored.go\n')
            git('add','.');git('-c','user.name=Test','-c','user.email=test@example.invalid','commit','-qm','baseline')
            (root/'tools/new.py').write_text('# allowed tool change\n')
            require_production_head(root)
            for name in ('new.go','ignored.go'):
                p=root/'internal'/name;p.write_text('package existing\n')
                with self.subTest(name=name),self.assertRaises(ValueError):require_production_head(root)
                p.unlink()
            p=root/'internal/existing.go';p.write_text('package modified\n')
            with self.assertRaises(subprocess.CalledProcessError):require_production_head(root)
            git('add','internal/existing.go')
            with self.assertRaises(subprocess.CalledProcessError):require_production_head(root)

    def test_full_schedule_has_five_of_each_fixed_condition(self):
        rows=schedule()
        self.assertEqual(len(rows),30)
        self.assertEqual(len({tuple(r.values()) for r in rows}),30)
        for kind in ('active','history'):
            for tail in (0,1000,5000):
                self.assertEqual(sum(r['kind']==kind and r['tail']==tail and r['commands']==10000 for r in rows),5)
        self.assertNotEqual([r['tail'] for r in rows[:6]],[r['tail'] for r in rows[6:12]])

    def test_readonly_report_verifies_all_witnesses_and_labels_limits(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root);seal(root,True);before=digest(root/'manifest.json')
            report=analyze(root)
            self.assertEqual(report['workersVerified'],6)
            self.assertEqual(report['firstResponsesVerified'],48)
            self.assertEqual(report['retriesVerified'],12)
            self.assertFalse(report['powerLossTested'])
            self.assertEqual(report['rows'][0]['metrics']['openMs']['median'],2)
            self.assertIn('전원 장애',markdown(report));self.assertIn('시간 밖',markdown(report))
            verify(root);self.assertEqual(before,digest(root/'manifest.json'))

    def test_wrong_recovery_or_missing_proof_cannot_pass(self):
        faults={'Verified':False,'FirstResponsesVerified':7,'RetriesVerified':1,'OrdersBalancesTradeVerified':False,
                'FinalSnapshotSHA256':'bad','WALSHA256':'bad','GoVersion':'other','GOMAXPROCS':2,'Tail':1}
        recovery={'SnapshotSeq':'0','SnapshotFile':'fallback.bin','ReplayedCommands':'1','Skipped':['fallback']}
        for section,field,value in [*(('',k,v) for k,v in faults.items()),*(('Recovery',k,v) for k,v in recovery.items()),
                                    ('Open','WallNanos',0),('Open','PeakRSSBytes',3*2**30),('AfterGC','NumGC',1),('After','TotalAlloc',0)]:
            with self.subTest(section=section,field=field),tempfile.TemporaryDirectory() as temp:
                root=Path(temp);self.fixture(root);p=root/'runs/000/result.json';o=read(p)
                target=o[section] if section in ('Recovery','Open') else o['Open'][section] if section else o
                target[field]=value;p.unlink();save(p,o)
                with self.assertRaises(ValueError):analyze(root)

    def test_changed_or_missing_evidence_is_rejected(self):
        for fault in ('missing','case','copy','extra','binary','archive','marker','counts','cuts','witness','protocol'):
            with self.subTest(fault=fault),tempfile.TemporaryDirectory() as temp:
                root=Path(temp);self.fixture(root)
                if fault=='missing':(root/'runs/000/result.json').unlink()
                elif fault=='copy':(root/'runs/000/run/commands.wal').write_text('changed')
                elif fault=='extra':(root/'runs/000/run/unexpected').write_text('changed')
                elif fault=='binary':(root/'statescale').write_text('changed')
                elif fault=='archive':(root/'source.tar.gz').write_text('changed')
                else:
                    p=root/('runs/000/case.json' if fault=='case' else 'runs/000/run/.tail-owned' if fault=='marker' else 'protocol.json' if fault=='protocol' else 'fixtures/active.json')
                    value=read(p)
                    if fault in ('case','marker'):value['repeat']=2
                    if fault=='counts':value['Counts']['Active']=0
                    if fault=='cuts':value['Cuts']=[24]
                    if fault=='witness':value['Witnesses'][0]['First']['order']['status']='CANCELLED'
                    if fault=='protocol':value['repeats']=5
                    p.unlink();save(p,value)
                with self.assertRaises((ValueError,FileNotFoundError)):analyze(root)


if __name__=='__main__':unittest.main()
