#!/usr/bin/python3
import importlib.util
from pathlib import Path
import unittest
import tempfile
import json
import fcntl
import os
import stat
from types import SimpleNamespace
from unittest.mock import patch

s=importlib.util.spec_from_file_location('inspect_maintenance',Path(__file__).with_name('autonomy-server-maintenance-inspect.py'));I=importlib.util.module_from_spec(s);s.loader.exec_module(I)
class Backend:
 def __init__(self):self.config=['a'*64,'a'*64];self.invocations=['first','first'];self.healthy=True;self.reads=0
 def capture(self,r):return self.config.pop(0)
 def state_sha(self,value):return value
 def invocation(self,r):return self.invocations.pop(0)
 def _health_once(self,r):self.reads+=1;return dict(healthy=self.healthy,observed_at=I.time.time(),response_sha256='b'*64,metrics_sha256='c'*64)
 def properties(self,r,names):return dict(ActiveState='active',SubState='running')
 def install(self,*a):raise AssertionError('inspection mutated service')
 def activate(self,*a):raise AssertionError('inspection restarted service')
 def restore(self,*a):raise AssertionError('inspection overwrote foreign bytes')
class InspectionTests(unittest.TestCase):
 def setUp(self):
  self.request=dict(operation_id='d'*64,authority_sha256='e'*64,expected_state_sha256='f'*64,limits=dict(cpu_quota_percent=50,memory_max_bytes=256<<20,tasks_max=64),generation=1)
 def check(self,backend=None,registry=None,cancelled=lambda:False,owned=lambda:False):
  with patch.object(I.time,'sleep'):
   return I.inspect_backend(backend or Backend(),{},self.request,'1'*64,'2'*64,registry or (lambda:'2'*64),cancelled,owned,'3'*64)
 def test_read_only_bracket_receipt(self):
  r=self.check();I.M.verify_sealed(r);self.assertEqual(r['state'],'external_healthy');self.assertTrue(r['no_mutation']);self.assertFalse(r['owned_candidate']);self.assertEqual(r['current_state_sha256'],r['post_state_sha256']);self.assertEqual(r['invocation_id'],r['post_invocation_id']);self.assertEqual(len(r['observations']),3)
  self.assertTrue(all(r['observed_at']<=o['observed_at']<=r['completed_at'] for o in r['observations']))
 def test_configuration_change_is_not_supersession(self):
  b=Backend();b.config[1]='9'*64;self.assertEqual(self.check(b)['state'],'unavailable')
 def test_restart_during_health_is_not_supersession(self):
  b=Backend();b.invocations[1]='second';self.assertEqual(self.check(b)['state'],'unavailable')
 def test_registry_change_before_or_during_bracket(self):
  for values in (['3'*64],['2'*64,'3'*64]):
   with self.subTest(values=values):self.assertEqual(self.check(registry=lambda:values.pop(0))['state'],'unavailable')
 def test_inactive_unit_not_masked_by_healthy_endpoint(self):
  b=Backend()
  with patch.object(b,'properties',return_value=dict(ActiveState='inactive',SubState='dead')):self.assertEqual(self.check(b)['state'],'unavailable')
 def test_status_during_launch_guard_cannot_terminalize_running_intent(self):
  with tempfile.TemporaryDirectory() as tmp:
   root=Path(tmp);request=dict(self.request,owner_job='11111111-1111-4111-8111-111111111111',owner_task=1,pin_sha256='8'*64)
   raw=I.M.canonical(request);pending=I.response(root,request,raw,'1'*64,'running');I.M.atomic(root/'receipt.json',pending)
   with (root/'launch.lock').open('a') as held:
    fcntl.flock(held,fcntl.LOCK_EX|fcntl.LOCK_NB)
    with patch.object(I,'folder',return_value=root),patch.object(I,'request_at',return_value=(request,raw,{},'2'*64)),patch.object(I.M,'root_bytes',side_effect=lambda p,*args:(p.read_bytes(),0o600)):
     result=I.dispatch(SimpleNamespace(completion_service_active=lambda name:False),'server-maintenance-inspect-status',request['owner_job'],request['operation_id'],'1'*64,1)
    self.assertEqual(result['state'],'running')
    self.assertEqual(json.loads((root/'receipt.json').read_bytes()),pending)

 def test_cached_dependency_tamper_rejected_before_any_import(self):
  spec=importlib.util.spec_from_file_location('inspection_runner_test',Path(__file__).with_name('autonomy-runner.py'));r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
  with tempfile.TemporaryDirectory() as tmp:
   base=Path(tmp);r.SERVER_MAINTENANCE_ROOT=base/'state';r.SERVER_MAINTENANCE_TOOLS=base/'tools';op='a'*64;inspection='b'*64;digest='c'*64
   folder=r.SERVER_MAINTENANCE_ROOT/'operations'/op/'inspections'/inspection;folder.mkdir(parents=True)
   cache=r.SERVER_MAINTENANCE_TOOLS/digest;cache.mkdir(parents=True)
   files={}
   for name in ('autonomy-runner.py','autonomy-server-maintenance.py','autonomy-server-operations.py','autonomy-server-maintenance-inspect.py','entry.py'):
    (cache/name).write_bytes(b'# frozen\n');files[name]=I.M.sha(b'# frozen\n')
   (folder/'executables.json').write_text(json.dumps(dict(digest=digest,files=files)))
   marker=base/'import-executed'
   (cache/'autonomy-server-maintenance.py').write_text("from pathlib import Path\nPath("+repr(str(marker))+").write_text('bad')\n")
   (cache/'autonomy-server-maintenance-inspect.py').write_text("from pathlib import Path\nPath("+repr(str(marker))+").write_text('bad')\n")
   # Even the primary helper's hash can be intact while a companion has drifted.
   files['autonomy-server-maintenance-inspect.py']=I.M.sha((cache/'autonomy-server-maintenance-inspect.py').read_bytes());(folder/'executables.json').write_text(json.dumps(dict(digest=digest,files=files)))
   fake=lambda path:SimpleNamespace(st_uid=0,st_mode=0o400,st_size=path.stat().st_size)
   with patch.object(r,'regular',side_effect=fake),patch.object(Path,'lstat',return_value=SimpleNamespace(st_uid=0,st_mode=stat.S_IFDIR|0o755)):
    with self.assertRaisesRegex(ValueError,'identity changed'):r.server_maintenance_inspect(SimpleNamespace(operation_id=op,inspection_id=inspection,command='server-maintenance-inspect-status',job='11111111-1111-4111-8111-111111111111',generation=1))
   self.assertFalse(marker.exists())

 def test_status_before_first_runner_admission_then_same_id_start(self):
  with tempfile.TemporaryDirectory() as tmp:
   base=Path(tmp);op='a'*64;inspection='b'*64;job='11111111-1111-4111-8111-111111111111';root=base/'inspection';root.mkdir();operation=base/'operation';transactions=base/'transactions';transactions.mkdir()
   request=dict(self.request,schema_version=1,operation_id=op,owner_job=job,owner_task=1,pin_sha256='8'*64,target_id='fixture',service_id='sensor',action='service_resource_limits',registry_sha256='2'*64)
   authority={k:request[k] for k in ('operation_id','target_id','service_id','registry_sha256','expected_state_sha256','pin_sha256','owner_job','owner_task','action')}
   authority.update(request_sha256=I.M.request_digest(request),audit_receipt_sha256={'auditor_a':'3'*64,'auditor_b':'4'*64},candidate_review_sha256='5'*64,backup_receipt_sha256='6'*64,validation_receipt_sha256='7'*64)
   request['authority_sha256']=I.M.sha(I.M.canonical(authority));raw=I.M.canonical(request)
   phase=operation/'phases'/'apply'/job/'1';phase.mkdir(parents=True);(phase/'authority.json').write_bytes(I.M.canonical(authority))
   journal=dict(phase='rollback_conflict',generation=1,request_sha256=I.M.request_digest(request),authority_sha256=request['authority_sha256'],receipt=I.sealed(dict(state='rollback_conflict')))
   (transactions/(op+'.json')).write_bytes(I.M.canonical(journal))
   controller=base/'jobs'/job/'server-maintenance';controller.mkdir(parents=True);(controller/(op+'.inspect-'+inspection+'.json')).write_bytes(raw)
   calls=[];runtime=SimpleNamespace(job_path=lambda j:base/'jobs'/j,completion_service_active=lambda name:False,completion_launch_capacity=lambda:None,run=lambda args,**kw:calls.append(args))
   with patch.object(I,'folder',return_value=root),patch.object(I.M,'operation_root',return_value=operation),patch.object(I.M,'transaction_root',return_value=transactions),patch.object(I.M,'root_bytes',side_effect=lambda path,*args:(path.read_bytes(),0o600)),patch.object(I.M,'controller_bytes',side_effect=lambda path:path.read_bytes()),patch.object(I.M.OBS,'load_registry',return_value=({},'2'*64)),patch.object(I,'freeze',return_value=base/'cache'):
    waiting=I.dispatch(runtime,'server-maintenance-inspect-status',job,op,inspection,1)
    self.assertEqual(waiting['state'],'waiting');self.assertEqual(waiting['request_sha256'],I.M.sha(raw));self.assertFalse((root/'request.json').exists());self.assertEqual(calls,[])
    started=I.dispatch(runtime,'server-maintenance-inspect',job,op,inspection,1)
    self.assertEqual(started['state'],'running');self.assertEqual(len(calls),1);self.assertEqual((root/'request.json').read_bytes(),raw)

 def test_owned_candidate_never_claimed_external(self):
  self.assertEqual(self.check(owned=lambda:True)['state'],'unavailable')
 def test_unhealthy_or_cancelled_never_claimed_external(self):
  b=Backend();b.healthy=False;self.assertEqual(self.check(b)['state'],'unavailable');self.assertEqual(self.check(cancelled=lambda:True)['state'],'unavailable')
if __name__=='__main__':unittest.main()
