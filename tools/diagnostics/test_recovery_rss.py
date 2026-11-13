"""RSS diagnosis schedule, memory units, controls and rejection boundaries."""
import copy
from unittest.mock import patch
import unittest

from recovery_rss import metrics, production, sample_order, schedule, validate_rss


class RecoveryRSSTests(unittest.TestCase):
    def test_twenty_workers_five_pairs_each_kind_and_counterbalanced_versions(self):
        rows=schedule();self.assertEqual(len(rows),20);self.assertEqual(len({tuple(r.values()) for r in rows}),20)
        for kind in ('active','history'):
            for side in ('baseline','candidate'):self.assertEqual(sum(r['kind']==kind and r['version']==side and r['commands']==10000 and r['tail']==0 for r in rows),5)
            first=[rows[i]['version'] for i in range(0,20,2) if rows[i]['kind']==kind]
            self.assertIn(first.count('baseline'),(2,3))
        self.assertEqual(sum(rows[i]['version']=='baseline' for i in range(0,20,2)),5)
        self.assertEqual(len(schedule(True)),4)

    def test_observations_separate_idle_gc_only_and_force_release(self):
        order=sample_order(False);self.assertEqual(len(order),13)
        self.assertEqual(order[2:7],[('idle',x) for x in (0,100,1000,5000,15000)])
        self.assertEqual(order[7:10],[('gc-only',x) for x in (0,100,1000)])
        self.assertEqual(order[10:],[('force-release',x) for x in (0,100,1000)])
        self.assertEqual(sample_order(True)[6],('idle',20))

    def observation(self):
        samples=[]
        for i,(stage,t) in enumerate(sample_order(True)):
            gc=1+(i>=2)+(i>=7)+(i>=10)
            samples.append({'Stage':stage,'TargetMillis':t,'ElapsedNanos':t*10**6+1000,'CaptureNanos':1000,'Goroutines':5,'RSSBytes':10000000,'PeakRSSBytes':11000000,
                            'Memory':{'HeapAlloc':2000000,'HeapInuse':3000000,'HeapSys':8000000,'HeapIdle':5000000,'HeapReleased':1000000,'HeapObjects':100,'StackInuse':100000,'StackSys':200000,'GCSys':100000,'OtherSys':100000,'Sys':10000000,'NumGC':int(gc)},
                            'VM':{'ResidentBytes':10000000,'PhysicalFootprintBytes':8000000,'ReusableBytes':1000000,'CompressedBytes':0,'InternalBytes':7000000,'ExternalBytes':2000000,'VirtualBytes':100000000}})
        return {'Version':1,'Smoke':True,'Verified':True,'Tail':{},'Samples':samples,'Actions':[{'Kind':'gc-only','DurationNanos':1000},{'Kind':'force-release','DurationNanos':2000}]}

    def validate(self,o):
        with patch('recovery_rss.validate'),patch('recovery_rss.paired.validate_resident'):
            validate_rss(o,schedule(True)[0],{}, {},{'smoke':True,'environment':{}})

    def test_valid_trace_and_negative_faults(self):
        self.validate(self.observation())
        for fault in ('failed','error','missing','order','target','early','zero_capture','rss','footprint','negative','bool','heap_relation','released','stack','gc','control'):
            with self.subTest(fault=fault):
                o=self.observation();s=o['Samples'][2]
                if fault=='failed':o['Verified']=False
                if fault=='error':o['Error']='failed'
                if fault=='missing':o['Samples'].pop()
                if fault=='order':o['Samples'].reverse()
                if fault=='target':s['TargetMillis']=100
                if fault=='early':o['Samples'][3]['ElapsedNanos']=0
                if fault=='zero_capture':s['CaptureNanos']=0
                if fault=='rss':s['RSSBytes']=2*2**30+1
                if fault=='footprint':s['VM']['PhysicalFootprintBytes']=2*2**30+1
                if fault=='negative':s['VM']['CompressedBytes']=-1
                if fault=='bool':s['Memory']['HeapAlloc']=True
                if fault=='heap_relation':s['Memory']['HeapSys']+=1
                if fault=='released':s['Memory']['HeapReleased']=s['Memory']['HeapIdle']+1
                if fault=='stack':s['Memory']['StackInuse']=s['Memory']['StackSys']+1
                if fault=='gc':s['Memory']['NumGC']=1
                if fault=='control':o['Actions'][1]['Kind']='gc-only'
                with self.assertRaises(ValueError):self.validate(o)

    def test_metrics_distinguish_resident_virtual_idle_and_footprint(self):
        s=self.observation()['Samples'][2];m=metrics(s)
        self.assertEqual(m['retainedIdleMiB'],4000000/2**20)
        self.assertEqual(m['unusedInuseMiB'],1000000/2**20)
        self.assertNotEqual(m['rssMiB'],m['PhysicalFootprintMiB'])
        self.assertEqual(m['HeapSysMiB'],8000000/2**20)
        self.assertEqual(m['latenessMs'],0.001)

    def test_production_guard_includes_native_code_but_excludes_test_go(self):
        s={'internal/x.go':'a','internal/x.c':'b','internal/x_test.go':'ignore','cmd/server/main.go':'c','go.mod':'d','.go-version':'e','tools/statescale/rss.go':'probe'}
        self.assertEqual(set(production(s)),set(s)-{'internal/x_test.go','tools/statescale/rss.go'})
