"""Sealed phase/order graphs; the report is not a second matching engine."""
import hashlib
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

from common import digest, read, save, seal, verify
from transport_report import generate, spec_inputs

RUN = '00000000-0000-4000-8000-000000000001'
CONFIG = {'accounts':[{'accountId':a,'baseLots':'100','quoteAtoms':'100000'} for a in ('alice','bob','carol')]}
BOUNDARIES = ['wal-after-sync','snapshot-before-publish','snapshot-after-publish']


def place(account, name):
    return {'accountId':account,'kind':'PLACE_ORDER','place':{'clientOrderId':name,'symbol':'BTC-USDT','side':'BUY','type':'LIMIT','timeInForce':'GTC','priceTicks':'100','quantityLots':'1'}}


def commands():
    return [place('alice','maker'),place('bob','fill'),place('bob','buy'),
            {'accountId':'alice','kind':'CANCEL_ORDER','cancel':{'cancelRequestId':'cancel','clientOrderId':'maker'}},place('carol','rest'),place('bob','tail')]


def result(c, seq):
    p = c.get('place',c.get('cancel'))
    return {'schemaVersion':1,'runId':RUN,'accountId':c['accountId'],'kind':c['kind'],
            'requestId':p['clientOrderId'] if c['kind']=='PLACE_ORDER' else p['cancelRequestId'],
            'commandSeq':str(seq),'code':'ACCEPTED' if c['kind']=='PLACE_ORDER' else 'CANCELLED','tradeCount':'0'}


def raw(value): return json.dumps(value,separators=(',',':'))


def wal(cs):
    rows=[]
    for i,c in enumerate(cs,1):
        rows.append({'Seq':str(i),'Command':{'RunID':RUN,'AccountID':c['accountId'],'Kind':c['kind'],
                                           'Place':c.get('place',{}),'Cancel':c.get('cancel',{})}})
    return rows


def cp(cs):
    return {'accounts':[{'accountId':a,'availableBaseLots':'100','reservedBaseLots':'0','availableQuoteAtoms':'100000','reservedQuoteAtoms':'0'} for a in ('alice','bob','carol')],
            'orders':[],'book':{'bids':[],'asks':[]},'trades':[],
            'results':[{'command':c,'response':result(c,i)} for i,c in enumerate(cs,1)]}


def queries(cs, step=None):
    return [dict({'account':c['accountId'],'path':'/v1/results/'+('place/' if c['kind']=='PLACE_ORDER' else 'cancel/')+result(c,i)['requestId'],
                  'status':200,'response':raw(result(c,i))},**({'method':'GET','step':step} if step is not None else {})) for i,c in enumerate(cs,1)]


def write_lines(path, rows): path.write_text(''.join(raw(row)+'\n' for row in rows))


def reseal(root, crash=False):
    for name in ('manifest.json','completed.json'): (root/name).unlink(missing_ok=True)
    seal(root,True)
    c=read(root/'completed.json');c['status']='PASSED'
    if crash:c.update(processKillTested=True,powerLossTested=False)
    (root/'completed.json').write_text(raw(c)+'\n')


def normal_fixture(root):
    root.mkdir();cs=commands();order=wal(cs)
    initial=[{'client':i,'command':cs[n],**({'fault':fault} if fault else {})} for i,n,fault in [(0,2,'drop-headers'),(1,2,'truncate-body'),(2,3,''),(3,4,'held-original')]]
    retry=[{'client':i,'command':cs[n]} for i,n in enumerate([2,3,4,5])]
    group={'initial':initial,'retries':retry,'snapshotClient':0}
    plan={'version':2,'seed':17,'actions':[{'kind':'command','command':c} for c in cs[:2]]+[{'kind':'concurrent','concurrent':group},{'kind':'restart'}]}
    save(root/'scenario.json',plan);save(root/'config.json',CONFIG)
    save(root/'summary.json',{'status':'PASSED','scenarioSHA256':digest(root/'scenario.json'),'concurrentGroups':1,'responseLosses':3,'appliedSeq':'6','powerLossTested':False,'processKillTested':False})
    for name in ('final-actual','final-expected'):save(root/(name+'.json'),cp(cs))
    for name in ('wal','live-order'):save(root/('concurrent-002-'+name+'.json'),order)
    for name in ('actual','expected'):save(root/('concurrent-002-snapshot-'+name+'.json'),cp(cs[:3]))
    save(root/'restart-002.json',{'SnapshotSeq':'3','ReplayedCommands':'3','Skipped':[]})
    write_lines(root/'events.jsonl',[{'step':2,'phase':'concurrent-snapshot','snapshotSeq':'3','witnessClient':0,'clientStillInFlight':True}])
    trace=[{'step':2,'method':'GET','account':'carol','path':'/v1/results/place/rest','status':404,'response':'{"state":"UNKNOWN"}'}]+queries(cs,2)
    write_lines(root/'http.jsonl',trace)
    obs=[]
    for phase,rows in [('INITIAL',initial),('RETRY',retry)]:
        for a in rows:
            n=cs.index(a['command'])+1;response=raw(result(a['command'],n));fault=a.get('fault')
            outcome={'class':'BUSINESS','certainty':'COMPLETED','httpStatus':200,'fingerprint':hashlib.sha256(response.encode()).hexdigest()}
            if fault:outcome={'class':'TRANSPORT_FAILURE','certainty':'UNKNOWN','httpStatus':200 if fault=='truncate-body' else 0,'failureStage':'response_read' if fault=='truncate-body' else 'response_headers','errorType':'unexpected_eof' if fault=='truncate-body' else 'eof'}
            obs.append({**a,'phase':phase,'outcome':outcome,'requestLatencyNanos':1000000+a['client'],'upstreamStatus':200,'upstreamResponse':response})
    save(root/'concurrent-002-observations.json',obs);reseal(root)


def crash_fixture(root):
    root.mkdir();cs=commands();cases=[];summaries=[]
    for boundary in BOUNDARIES:
        is_wal=boundary=='wal-after-sync';folder=root/boundary;folder.mkdir()
        attempts=[cs[2],cs[2],cs[3],cs[4]];c={'boundary':boundary,'setup':cs[:2],'attempts':attempts}
        if not is_wal:c['tail']=cs[5]
        cases.append(c);save(folder/'case.json',c)
        final_cs=cs[:5] if is_wal else cs;crash_cs=cs[:3] if is_wal else cs
        final=cp(final_cs);recovered=cp(crash_cs);records=wal(crash_cs)
        for label,value in [('final',final),('recovered',recovered)]:
            for side in ('actual','expected'):save(folder/(label+'-'+side+'.json'),value)
        save(folder/'crash-wal.json',records);save(folder/'final-wal.json',wal(final_cs))
        snap='5' if boundary=='snapshot-after-publish' else '2';tail=str(len(crash_cs)-int(snap))
        save(folder/'recovery.json',{'SnapshotSeq':snap,'ReplayedCommands':tail,'SnapshotFile':'snapshot.bin','Skipped':[]})
        message={'phase':'boundary',**({'records':records[2:3]} if is_wal else {'snapshotSeq':'5'})}
        save(folder/'kill-boundary.json',{'boundary':boundary,'pid':99,'signal':'SIGKILL','initialCallsStillInFlight':4,'message':message,'powerLossTested':False})
        save(folder/'kill-confirmed.json',{'signal':'SIGKILL','waitStatusVerified':True})
        write_lines(folder/'child.stdout.jsonl',[{'phase':'ready','run':RUN,'base':'http://127.0.0.1:1'},{'phase':'wave-entered'}]+([] if is_wal else [{'phase':'wave-complete'}])+[message])
        ack=cs[:2]+([] if is_wal else [cs[5]])
        save(folder/'acknowledged.json',[{'command':v,'status':200,'response':raw(result(v,cs.index(v)+1))} for v in ack])
        save(folder/'initial-observations.json',[{'client':i,'command':v,'outcome':'UNKNOWN','status':0,'error':'private raw error not to repeat','elapsedNs':1000+i} for i,v in enumerate(attempts)])
        save(folder/'followup-results.json',[{'command':v,'originalOutcome':'UNKNOWN','status':200 if v in crash_cs else 404,'response':raw(result(v,cs.index(v)+1)) if v in crash_cs else '{"state":"UNKNOWN"}'} for v in cs[2:5]])
        save(folder/'retry-responses.json',[{'client':i,'command':v,'originalOutcome':'UNKNOWN','status':200,'response':raw(result(v,cs.index(v)+1))} for i,v in enumerate(attempts)])
        save(folder/'recovered-http.json',queries(crash_cs));save(folder/'second-recovery-http.json',queries(final_cs))
        summary={'boundary':boundary,'signal':'SIGKILL','originalUnknown':4,'followup404Unknown':2 if is_wal else 0,'crashCommands':len(crash_cs),'finalCommands':len(final_cs),
                 'snapshotSeq':snap,'tailCommands':tail,'trades':0,'concurrentRetries':4,'secondRecoveryRetries':len(final_cs),'powerLossTested':False}
        summaries.append(summary);save(folder/'summary.json',summary)
    save(root/'scenario.json',{'version':1,'seed':17,'cases':cases});save(root/'summary.json',{'status':'PASSED','cases':summaries,'processKillTested':True,'powerLossTested':False});save(root/'config.json',CONFIG);reseal(root,True)


def spec(root, entries):
    path=root/'spec.json';save(path,{'version':1,'inputs':entries});return path


class ConcurrentReportTests(unittest.TestCase):
    def test_both_adapters_reproduce_separate_unknown_query_retry_and_do_not_write_sources(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp).resolve();normal_fixture(root/'normal');crash_fixture(root/'crash')
            entries=[{'name':'parallel','kind':'concurrent','bundle':'normal'}]+[{'name':c,'kind':'concurrent-crash','bundle':'crash','case':c} for c in BOUNDARIES]
            path=spec(root,entries);before={str(p):digest(p) for p in root.rglob('*') if p.is_file()}
            first=generate(path,root/'report');second=generate(path,root/'again')
            self.assertEqual(first,second);self.assertEqual((root/'report/report.md').read_bytes(),(root/'again/report.md').read_bytes())
            self.assertTrue(verify(root/'report')['completed'])
            self.assertEqual(before,{p:digest(p) for p in before})
            normal,crash,*_=first['inputs'];self.assertEqual(normal['counts']['unknownAttempts'],3)
            self.assertEqual(crash['counts']['unknownAttempts'],4)
            self.assertTrue(all(r['measurementCertainty']=='UNKNOWN' for source in first['inputs'] for r in source['attempts']))
            missing=next(r for r in crash['unknownCommands'] if r['requestId']=='cancel')
            self.assertEqual(missing['firstFollowupResolution'],'UNRESOLVED');self.assertEqual(missing['retryResolution'],'RESULT_OBSERVED')
            self.assertTrue(any(q['httpStatus']==404 and q['resolution']=='UNRESOLVED' for q in missing['queries']))
            self.assertTrue(all(r['failureStage'] is None and r['errorType'] is None for r in crash['attempts']))
            text=(root/'report/report.md').read_text();self.assertIn('RECOVERY_TAIL',text);self.assertIn('AFTER_RETRY',text)
            self.assertNotIn('private raw error',raw(first));self.assertNotIn('private raw error',text)
            with self.assertRaises(ValueError):generate(path,root/'report')

    def test_resealed_concurrent_semantic_tampering_fails_before_output(self):
        edits=[('concurrent-002-wal.json',lambda v:v[2].update(Seq='2')),
               ('concurrent-002-observations.json',lambda v:v[0]['outcome'].update(certainty='COMPLETED')),
               ('concurrent-002-observations.json',lambda v:v[0].update(client=1)),
               ('concurrent-002-observations.json',lambda v:v[0].update(requestLatencyNanos=-1)),
               ('concurrent-002-snapshot-actual.json',lambda v:v['accounts'][0].update(availableBaseLots='0')),
               ('restart-002.json',lambda v:v.update(ReplayedCommands='2'))]
        for name,edit in edits:
            with self.subTest(name=name),tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp).resolve();normal_fixture(root/'source');path=spec(root,[{'name':'one','kind':'concurrent','bundle':'source'}])
                target=root/'source'/name;v=read(target);edit(v);target.write_text(raw(v))
                if name == 'concurrent-002-wal.json':
                    (root/'source/concurrent-002-live-order.json').write_text(raw(v))
                if name == 'concurrent-002-snapshot-actual.json':
                    (root/'source/concurrent-002-snapshot-expected.json').write_text(raw(v))
                reseal(root/'source')
                with self.assertRaises((ValueError,KeyError)):generate(path,root/'invalid')
                self.assertFalse((root/'invalid').exists())

    def test_resealed_crash_identity_order_kill_and_phase_tampering(self):
        edits=[('kill-confirmed.json',lambda v:v.update(signal='SIGTERM')),
               ('initial-observations.json',lambda v:v[0].update(outcome='COMPLETED')),
               ('followup-results.json',lambda v:v[1].update(originalOutcome='COMPLETED')),
               ('retry-responses.json',lambda v:v[0].update(response=v[2]['response'])),
               ('crash-wal.json',lambda v:v[2]['Command'].update(AccountID='alice')),
               ('recovery.json',lambda v:v.update(SnapshotSeq='5')),
               ('acknowledged.json',lambda v:v.pop())]
        for name,edit in edits:
            with self.subTest(name=name),tempfile.TemporaryDirectory() as tmp:
                root=Path(tmp).resolve();crash_fixture(root/'source');path=spec(root,[{'name':'one','kind':'concurrent-crash','bundle':'source','case':'wal-after-sync'}])
                target=root/'source/wal-after-sync'/name;v=read(target);edit(v);target.write_text(raw(v));reseal(root/'source',True)
                with self.assertRaises((ValueError,KeyError)):generate(path,root/'invalid')
                self.assertFalse((root/'invalid').exists())

    def test_input_kind_case_followups_and_output_protection(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp).resolve();normal_fixture(root/'normal')
            for entry in [{'name':'bad','kind':'concurrent','bundle':'normal','case':'x'},
                          {'name':'bad','kind':'concurrent-crash','bundle':'normal'},
                          {'name':'bad','kind':'concurrent-crash','bundle':'normal','case':'../x'},
                          {'name':'bad','kind':'concurrent-crash','bundle':'normal','case':'wal-after-sync','followups':[]}]:
                path=root/'spec.json';path.unlink(missing_ok=True);save(path,{'version':1,'inputs':[entry]})
                with self.assertRaises(ValueError):spec_inputs(path)
            path.unlink();path=spec(root,[{'name':'ok','kind':'concurrent','bundle':'normal'}])
            with self.assertRaises(ValueError):generate(path,root/'normal/inside')
            link=root/'link';link.symlink_to(root/'normal',target_is_directory=True)
            with self.assertRaises(ValueError):generate(path,link/'inside')

    def test_recorded_schedule_and_retry_order_match_persisted_inputs(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp).resolve();normal_fixture(root/'normal');crash_fixture(root/'crash');cs=commands()
            plan=read(root/'normal/scenario.json')
            plan['actions'][2]['concurrent']['recorded']={'order':cs[2:],'snapshotAfter':'1'}
            (root/'normal/scenario.json').write_text(raw(plan))
            summary=read(root/'normal/summary.json');summary['scenarioSHA256']=digest(root/'normal/scenario.json');(root/'normal/summary.json').write_text(raw(summary));reseal(root/'normal')
            path=spec(root,[{'name':'normal','kind':'concurrent','bundle':'normal'}])
            generate(path,root/'valid')
            plan['actions'][2]['concurrent']['recorded']['order'][0:2]=list(reversed(cs[2:4]))
            (root/'normal/scenario.json').write_text(raw(plan));summary['scenarioSHA256']=digest(root/'normal/scenario.json');(root/'normal/summary.json').write_text(raw(summary));reseal(root/'normal')
            with self.assertRaisesRegex(ValueError,'recorded concurrent'):generate(path,root/'invalid')
            p=read(root/'crash/scenario.json');p['cases'][0]['recorded']={'beforeKill':[cs[2]],'afterRetry':cs[3:5]}
            (root/'crash/scenario.json').write_text(raw(p));(root/'crash/wal-after-sync/case.json').write_text(raw(p['cases'][0]));reseal(root/'crash',True)
            path.unlink();path=spec(root,[{'name':'crash','kind':'concurrent-crash','bundle':'crash','case':'wal-after-sync'}])
            generate(path,root/'valid-crash')
            p['cases'][0]['recorded']['afterRetry'].reverse()
            (root/'crash/scenario.json').write_text(raw(p));(root/'crash/wal-after-sync/case.json').write_text(raw(p['cases'][0]));reseal(root/'crash',True)
            with self.assertRaisesRegex(ValueError,'recorded retry'):generate(path,root/'invalid-crash')

    def test_output_failure_does_not_complete(self):
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp).resolve();normal_fixture(root/'normal');path=spec(root,[{'name':'ok','kind':'concurrent','bundle':'normal'}])
            with patch('transport_report.markdown',side_effect=OSError('expected failure')):
                with self.assertRaises(OSError):generate(path,root/'failed')
            self.assertTrue((root/'failed/failed.json').exists());self.assertFalse((root/'failed/completed.json').exists())
