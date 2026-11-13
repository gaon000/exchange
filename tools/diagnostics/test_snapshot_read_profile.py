"""Focused read profiles preserve fixed state and separate profile evidence."""
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from common import digest, read, save, seal, verify
from snapshot_read_profile import SCOPES, STAGES, collect, expected_counts, input_build, markdown, schedule, selected_files, summarize


class SnapshotReadProfileTests(unittest.TestCase):
    def fixture(self, root):
        n=24
        save(root/'protocol.json',{'version':1,'smoke':True,'commands':n,'order':schedule(True),'allocationSampleRate':65536,
                                  'sourceBundleManifestSHA256':'source','environment':{'GOMAXPROCS':'1'}})
        (root/'statescale').write_bytes(b'not executed');(root/'source.tar.gz').write_bytes(b'not extracted')
        save(root/'build.json',{'productionSourceIdentical':True,'binarySHA256':digest(root/'statescale'),
                               'sourceArchiveSHA256':digest(root/'source.tar.gz'),'go':{'GOVERSION':'go1.27.3'}})
        (root/'fixtures').mkdir();(root/'runs').mkdir()
        for kind in ('active','history'):
            source=root/'fixtures'/kind;source.mkdir();files={}
            for name in selected_files(n):(source/name).write_text(kind+name);files[name]=digest(source/name)
            save(root/'fixtures'/(kind+'.json'),{'Version':1,'Kind':kind,'Commands':n,'Counts':expected_counts(kind,n),'Files':files})
        for i,row in enumerate(schedule(True)):
            case=root/'runs'/f'{i:03d}';case.mkdir();(case/'run').mkdir();(case/'profile').mkdir()
            f=read(root/'fixtures'/(row['kind']+'.json'))
            for name in selected_files(n):(case/'run'/name).write_bytes((root/'fixtures'/row['kind']/name).read_bytes())
            save(case/'case.json',row);save(case/'run/.statescale-owned',row)
            for name in (('cpu.pprof',) if row['profiler']=='cpu' else ('alloc-before.pprof','alloc-after.pprof')):(case/'profile'/name).write_bytes(b'not parsed by this test')
            save(case/'profile/result.json',{'Version':1,'Verified':True,'Phase':'snapshot-open','Profiler':row['profiler'],'Commands':n,
                 'GoVersion':'go1.27.3','GOMAXPROCS':1,'MemProfileRate':65536 if row['profiler']=='alloc' else 524288,
                 'SnapshotSHA256':f['Files'][selected_files(n)[-1]],'WALSHA256':f['Files']['commands.wal'],
                 'Before':{'TotalAlloc':100,'Mallocs':1},'After':{'TotalAlloc':200,'Mallocs':2},
                 'Recovery':{'SnapshotSeq':'24','ReplayedCommands':'0','SnapshotFile':selected_files(n)[-1],'Skipped':[],
                             'Stages':{name:{'count':'1','sumNanos':'1000000','sumOverflow':False,'negativeSamples':'0'} for name in STAGES}}})

    @staticmethod
    def fake_pprof(binary,args,inputs,output,env):
        if output.name=='raw.txt':text='Period: 10000000\n'
        elif '-top' in args:
            unit='ns' if '-sample_index=cpu' in args else 'B'
            text=f'Showing nodes accounting for 20000000{unit}, 100% of 20000000{unit} total\n'
            text+=f'10000000{unit} 50% 50% 20000000{unit} 100% decoder\n'
            text+=f'10000000{unit} 50% 100% 10000000{unit} 50% child\n'
            text+=f'0 0% 100% 20000000{unit} 100% caller\n'
        else:text='retained profile/caller data\n'
        output.write_text(text)

    def test_input_producers_are_verified_before_selecting_current_build(self):
        root=Path('/not-executed')
        for kind,build_path in [('snapshot-tail',root/'build.json'),('canonical-buffer',root/'builds/candidate/build.json'),('recovery-rss',root/'builds/candidate/build.json')]:
            protocol={'smoke':False}
            if kind!='snapshot-tail':protocol['experiment']=kind
            def read_input(path):
                return protocol if path==root/'protocol.json' else {'binarySHA256':'expected','sourceSHA256':{'internal/snapshot/decode.go':'current'}} if path==build_path else self.fail('unexpected producer path')
            with self.subTest(kind=kind),patch('snapshot_read_profile.read',side_effect=read_input),patch('snapshot_read_profile.analyze_tail') as tail,patch('canonical_buffer.analyze') as canonical,patch('recovery_rss.analyze') as rss:
                name,b=input_build(root,False)
                self.assertEqual(name,kind);self.assertEqual(b['binarySHA256'],'expected')
                selected={'snapshot-tail':tail,'canonical-buffer':canonical,'recovery-rss':rss}[kind]
                selected.assert_called_once_with(root)
                self.assertEqual(sum(x.call_count for x in (tail,canonical,rss)),1)
        for protocol in ({'smoke':True,'experiment':'recovery-rss'},{'smoke':False,'experiment':'unknown'}):
            with patch('snapshot_read_profile.read',return_value=protocol),patch('snapshot_read_profile.analyze_tail') as execute:
                with self.assertRaises(ValueError):input_build(root,False)
                execute.assert_not_called()

    def test_two_states_have_twenty_cpu_and_five_alloc_workers(self):
        rows=schedule();self.assertEqual(len(rows),50);self.assertEqual(len({tuple(r.values()) for r in rows}),50)
        for kind in ('active','history'):
            for profiler,count in [('cpu',20),('alloc',5)]:self.assertEqual(sum(r['kind']==kind and r['profiler']==profiler for r in rows),count)
        self.assertTrue(all(r['profiler']=='cpu' for r in rows[:40]));self.assertEqual(len(schedule(True)),4)

    def test_readonly_inputs_regenerate_profiles_without_summing_scopes(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp)/'source';root.mkdir();self.fixture(root);seal(root,True);before=digest(root/'manifest.json')
            out=Path(temp)/'analysis';out.mkdir()
            with patch('snapshot_read_profile.pprof',side_effect=self.fake_pprof):report=summarize(root,out,{})
            self.assertEqual(report['workersVerified'],4);self.assertFalse(report['powerLossTested']);self.assertFalse(report['performanceImprovementClaimed'])
            row=report['rows'][0];self.assertEqual(row['cpuSamples'],2)
            self.assertEqual(row['profiles']['snapshotRead']['perOperation'],20000000)
            self.assertEqual(row['profiles']['snapshotRead']['topCumulative'][0]['flat'],0)
            self.assertEqual(set(row['profiles']),set(SCOPES));self.assertIn('중첩',markdown(report));self.assertIn('미완료·보류',markdown(report))
            verify(root);self.assertEqual(before,digest(root/'manifest.json'))
            out2=Path(temp)/'second';out2.mkdir()
            with patch('snapshot_read_profile.pprof',side_effect=self.fake_pprof):summarize(root,out2,{})
            for name in ('report.json','report.md'):self.assertEqual((out/name).read_bytes(),(out2/name).read_bytes())

    def test_zero_sample_smoke_does_not_claim_cost_or_call_missing_peek(self):
        with tempfile.TemporaryDirectory() as temp:
            root=Path(temp);self.fixture(root);out=root/'analysis';out.mkdir()
            def zero_cpu(binary,args,inputs,output,env):
                if '-sample_index=cpu' in args:
                    self.assertFalse(any(arg.startswith('-peek=') for arg in args))
                    output.write_text('Showing nodes accounting for 0ns, 0% of 0ns total\n')
                else:self.fake_pprof(binary,args,inputs,output,env)
            with patch('snapshot_read_profile.pprof',side_effect=zero_cpu):report=summarize(root,out,{})
            self.assertEqual(report['rows'][0]['cpuSamples'],0)
            self.assertIn('no cost inference',(out/'active-cpu/callers.txt').read_text())

    def test_missing_wrong_path_or_changed_evidence_fails_before_pprof(self):
        for fault in ('schedule','binary','archive','fixture','case','ownership','copy','missing','extra','verified','profiler','sample','runtime','seq','tail','fallback','state','counter','stage'):
            with self.subTest(fault=fault),tempfile.TemporaryDirectory() as temp:
                root=Path(temp);self.fixture(root);case=root/'runs/000'
                if fault in ('binary','archive'):(root/('statescale' if fault=='binary' else 'source.tar.gz')).write_text('changed')
                elif fault=='fixture':(root/'fixtures/active/commands.wal').write_text('changed')
                elif fault=='copy':(case/'run/commands.wal').write_text('changed')
                elif fault=='missing':(case/'profile/cpu.pprof').unlink()
                elif fault=='extra':(case/'profile/unexpected').write_text('changed')
                else:
                    path=root/'protocol.json' if fault=='schedule' else case/'case.json' if fault=='case' else case/'run/.statescale-owned' if fault=='ownership' else case/'profile/result.json'
                    value=read(path)
                    if fault=='schedule':value['order']=[]
                    if fault in ('case','ownership'):value['repeat']=2
                    if fault=='verified':value['Verified']=False
                    if fault=='profiler':value['Profiler']='alloc'
                    if fault=='sample':value['MemProfileRate']=65536
                    if fault=='runtime':value['GOMAXPROCS']=2
                    if fault=='seq':value['Recovery']['SnapshotSeq']='0'
                    if fault=='tail':value['Recovery']['ReplayedCommands']='1'
                    if fault=='fallback':value['Recovery']['Skipped']=['corrupt']
                    if fault=='state':value['SnapshotSHA256']='bad'
                    if fault=='counter':value['After']['TotalAlloc']=0
                    if fault=='stage':value['Recovery']['Stages']['load.decode']['sumOverflow']=True
                    path.unlink();save(path,value)
                with patch('snapshot_read_profile.pprof') as execute:
                    with self.assertRaises((ValueError,FileNotFoundError)):summarize(root,root/'out',{})
                    execute.assert_not_called()


if __name__=='__main__':unittest.main()
