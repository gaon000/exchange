"""Buffer experiment boundaries and successful scratch retirement."""
import copy
from pathlib import Path
import tempfile
import unittest

from common import digest, read, save
from read_buffer import BUFFERS, TARGET, TEST_TARGET, test_variant as bounded_test_variant, expected_names, retire_copy, schedule, validate_io, variant


class ReadBufferTests(unittest.TestCase):
    def test_complete_rotating_schedule_has_five_of_24_conditions(self):
        rows=schedule();self.assertEqual(len(rows),120);self.assertEqual(len({tuple(r.values()) for r in rows}),120)
        for kind in ('active','history'):
            for tail in (0,1000,5000):
                for kib in BUFFERS:self.assertEqual(sum(r['kind']==kind and r['tail']==tail and r['bufferKiB']==kib and r['commands']==10000 for r in rows),5)
        self.assertNotEqual([r['bufferKiB'] for r in rows[:4]],[r['bufferKiB'] for r in rows[4:8]])
        self.assertEqual(len(schedule(True)),24)

    def test_overlay_changes_only_decoder_argument_and_refuses_drift(self):
        source='checksumBuffer = bufferBytes\n'+TARGET+'\nwriterBuffer = bufferBytes\n'
        for kib in BUFFERS:
            out=variant(source,kib)
            self.assertEqual(out.replace(f'bufio.NewReaderSize(r, {kib*1024})',TARGET),source)
            self.assertIn('checksumBuffer = bufferBytes',out);self.assertIn('writerBuffer = bufferBytes',out)
        for source,kib in [('',32),(TARGET+TARGET,32),(TARGET,16),(TARGET,512)]:
            with self.assertRaises(ValueError):variant(source,kib)

    def test_test_overlay_preserves_write_bound_and_fixed_value_limit(self):
        source='if len(b) > bufferBytes { return 0, writeError }\n'+TEST_TARGET+'\nmaxValueBytes = 16 << 10\n'
        for kib in BUFFERS:
            out=bounded_test_variant(source,kib)
            self.assertIn('if len(b) > bufferBytes { return 0, writeError }',out)
            self.assertIn('maxValueBytes = 16 << 10',out)
            self.assertEqual(out.replace(TEST_TARGET.replace('> bufferBytes','> '+str(kib*1024)),TEST_TARGET),source)
        with self.assertRaises(ValueError):bounded_test_variant(TEST_TARGET+TEST_TARGET,64)

    def io(self,kib):
        payload=700001
        phases={}
        for name,n,q in [('header',20,20),('checksum',700021,32768),('digest',32,32),('decode',payload,kib*1024)]:
            hist={}
            if n//q:hist[str(q)]=n//q
            if n%q:hist[str(n%q)]=n%q and 1
            phases[name]={'Calls':sum(hist.values()),'RequestedBytes':n,'ReadBytes':n,'MaxRequest':max(map(int,hist)),'Sizes':hist}
        hist={}
        for v in phases.values():
            for n,count in v['Sizes'].items():hist[n]=hist.get(n,0)+count
        row={'kind':'active','commands':10000,'tail':1000,'bufferKiB':kib,'repeat':1};name='snapshot-00000000000000009000.bin';f={'Files':{name:'sha'}}
        o={'Version':1,'Verified':True,'BufferBytes':kib*1024,'SnapshotSeq':'9000','SnapshotFile':name,'SnapshotSHA256':'sha','PayloadBytes':payload,
           'FileReadAt':phases,'DirectPread':copy.deepcopy(phases),'PreadCalls':{'Calls':sum(hist.values()),'RequestedBytes':1400074,'ReadBytes':1400074,'MaxRequest':max(map(int,hist)),'Sizes':hist},'Interrupted':0,'ShortReads':0}
        return o,row,f

    def test_io_counts_fixed_checksum_and_changed_decoder_are_verified(self):
        for kib,total in [(32,46),(64,35),(128,30),(256,27)]:
            o,row,f=self.io(kib);validate_io(o,row,f,700001)
            self.assertEqual(o['PreadCalls']['Calls'],total)
            self.assertEqual(o['FileReadAt']['checksum']['Calls'],22)
        for fault in ('buffer','seq','snapshot','payload','verify','transport','decode','checksum','pread','bytes','histogram','retries'):
            with self.subTest(fault=fault):
                o,row,f=self.io(64)
                if fault=='buffer':o['BufferBytes']=32768
                if fault=='seq':o['SnapshotSeq']='5000'
                if fault=='snapshot':o['SnapshotSHA256']='changed'
                if fault=='payload':o['PayloadBytes']=1
                if fault=='verify':o['Verified']=False
                if fault=='transport':o['DirectPread']['decode']['Calls']+=1
                if fault in ('decode','checksum'):
                    for key in ('FileReadAt','DirectPread'):o[key][fault]['Calls']+=1
                if fault=='pread':o['PreadCalls']['Calls']+=1
                if fault=='bytes':o['PreadCalls']['ReadBytes']-=1
                if fault=='histogram':o['PreadCalls']['Sizes']['1']=1
                if fault=='retries':o['Interrupted']=-1
                with self.assertRaises(ValueError):validate_io(o,row,f,700001)

    def scratch(self,root):
        source=root/'original';source.mkdir();case=root/'case';case.mkdir();(case/'run').mkdir()
        row={'kind':'active','commands':10000,'tail':1000,'bufferKiB':64,'repeat':1};f={'Files':{}}
        for name in expected_names(row):
            (source/name).write_text(name);f['Files'][name]=digest(source/name);(case/'run'/name).write_bytes((source/name).read_bytes())
        save(case/'run/.tail-owned',row)
        return source,case,row,f

    def test_success_retains_receipt_and_original_before_removing_only_scratch(self):
        with tempfile.TemporaryDirectory() as temp:
            source,case,row,f=self.scratch(Path(temp));retire_copy(case,row,f)
            self.assertFalse((case/'run').exists());receipt=read(case/'verified-files.json');self.assertEqual(receipt['owner'],row)
            for name,meta in receipt['files'].items():self.assertEqual(meta,{'sha256':digest(source/name),'bytes':(source/name).stat().st_size})

    def test_changed_unowned_extra_or_symlink_copy_is_preserved(self):
        for fault in ('hash','owner','extra','symlink'):
            with self.subTest(fault=fault),tempfile.TemporaryDirectory() as temp:
                source,case,row,f=self.scratch(Path(temp));run=case/'run'
                if fault=='hash':(run/'commands.wal').write_text('changed')
                if fault=='owner':p=run/'.tail-owned';p.unlink();save(p,{**row,'repeat':2})
                if fault=='extra':(run/'unrelated').write_text('preserve')
                if fault=='symlink':p=run/'commands.wal';p.unlink();p.symlink_to(source/'commands.wal')
                with self.assertRaises(ValueError):retire_copy(case,row,f)
                self.assertTrue(run.exists());self.assertTrue((run/'.tail-owned').exists());self.assertFalse((case/'verified-files.json').exists())
                for name,sha in f['Files'].items():self.assertEqual(digest(source/name),sha)


if __name__=='__main__':unittest.main()
