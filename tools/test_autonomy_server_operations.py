#!/usr/bin/python3
import importlib.util
import json
import os
from pathlib import Path
import socketserver
import stat
import tempfile
import threading
import unittest
from unittest.mock import patch
import uuid
import fcntl
from types import SimpleNamespace
from http.server import BaseHTTPRequestHandler,HTTPServer

SPEC=importlib.util.spec_from_file_location('server_observer',Path(__file__).with_name('autonomy-server-operations.py'))
H=importlib.util.module_from_spec(SPEC);SPEC.loader.exec_module(H)

class ObservationTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name);self.identity=self.root/'service.py';self.identity.write_text('SECRET_TOKEN=do-not-expose')
  self.registry={'schema_version':1,'targets':{'fixture':{'kind':'local_systemd','services':{'worker':{'unit':'fixture.service','identity_files':[{'id':'program','path':str(self.identity),'max_bytes':1000}],'dropin_directory':str(self.root/'dropins')}},'filesystems':{'work':{'path':str(self.root)}},'backups':{'nightly':{'service_unit':'backup.service','timer_unit':'backup.timer'}}}}}
  self.request={'schema_version':1,'request_id':'a'*64,'owner_job':str(uuid.uuid4()),'owner_task':1,'target_id':'fixture','registry_sha256':'b'*64}
 def tearDown(self):self.tmp.cleanup()
 def units(self,memory=12,limit=100):
  fields={'LoadState':'loaded','ActiveState':'active','SubState':'running','MemoryCurrent':memory,'MemoryMax':limit,'TasksMax':20,'CPUQuotaPerSecUSec':2000000,'CPUQuotaPeriodUSec':100000}
  return {'fixture.service':{'state':'available','fields':fields},'backup.service':{'state':'available','fields':{'Result':'success'}},'backup.timer':{'state':'available','fields':{'ActiveState':'active'}}}
 def test_registry_and_request_reject_authority_expansion(self):
  for key,value in [('command','id'),('url','http://localhost/'),('path','/etc/shadow')]:
   request=dict(self.request,**{key:value})
   with self.assertRaises(H.Invalid):H.collect(self.registry,'b'*64,request)
  self.registry['targets']['fixture']['services']['worker']['unit']='fixture.service;touch /tmp/oops'
  with self.assertRaises(H.Invalid):H.validate_registry(self.registry)
 def test_maintenance_catalogue_is_fixed_policy_without_credentials(self):
  target=self.registry['targets']['fixture'];target['services']['worker']['dropin_directory']='/etc/systemd/system/fixture.service.d'
  target['health']={'temperature':{'adapter':'temp_api_v1','port':9101,'temperature_fields':[]}}
  self.registry['backup_profiles']={'offbox':{'repository_file':'/secret/repository','password_file':'/secret/password','offbox_host':'private'}}
  self.registry['maintenance']={'limits':{'target_id':'fixture','service_id':'worker','action':'service_resource_limits','health_id':'temperature','backup_profile':'offbox','stateless':True,'protected':False}}
  H.validate_registry(self.registry)
  with patch.object(H,'R',SimpleNamespace(SERVER_REGISTRY=self.root/'registry')),patch.object(H,'load_registry',return_value=(self.registry,'b'*64)):
   result=H.catalogue()
  policy=result['targets'][0]['maintenance'][0]
  self.assertEqual(policy['min']['memory_max_bytes'],268435456);self.assertEqual(policy['memory_headroom_ratio'],2)
  self.assertNotIn('/secret',json.dumps(result));self.assertNotIn('password_file',json.dumps(result));self.assertNotIn('offbox',json.dumps(result))
  self.registry['maintenance']['limits']['stateless']=False
  with self.assertRaises(H.Invalid):H.validate_registry(self.registry)
  self.registry['maintenance']['limits']['stateless']=True;target['services']['worker']['unit']='lectern.service'
  with self.assertRaises(H.Invalid):H.validate_registry(self.registry)
 def test_exact_registry_bytes_and_ownership(self):
  path=self.root/'registry.json';raw=json.dumps(self.registry,indent=2).encode();path.write_bytes(raw);path.chmod(0o600)
  _,sha=H.load_registry(path,trusted_uid=os.geteuid());self.assertEqual(sha,H.digest(raw))
  path.chmod(0o666)
  with self.assertRaises(H.Invalid):H.load_registry(path,trusted_uid=os.geteuid())
 def test_nofollow_links_hardlinks_fifo_and_parent_links(self):
  link=self.root/'linked';link.symlink_to(self.identity)
  with self.assertRaises(OSError):H.identity_file(link,1000)
  hard=self.root/'hard';os.link(self.identity,hard)
  with self.assertRaises(H.Unavailable):H.identity_file(hard,1000)
  fifo=self.root/'fifo';os.mkfifo(fifo)
  with self.assertRaises(H.Unavailable):H.identity_file(fifo,1000)
  directory=self.root/'dir';directory.mkdir();(directory/'file').write_text('x');(self.root/'alias').symlink_to(directory)
  with self.assertRaises(OSError):H.identity_file(self.root/'alias/file',1000)
 def test_structured_facts_never_return_file_contents_or_false_backup_proof(self):
  with patch.object(H,'observe_units',return_value=self.units()):result=H.collect(self.registry,'b'*64,self.request)
  self.assertNotIn('SECRET_TOKEN',json.dumps(result));self.assertNotIn('do-not-expose',json.dumps(result));self.assertFalse(result['mutation_performed']);self.assertTrue(result['configuration_complete'])
  backup=result['facts']['backups']['nightly'];self.assertFalse(backup['snapshot_verified']);self.assertFalse(backup['restore_verified'])
  fs=result['facts']['filesystems']['work'];actual=os.statvfs(self.root);self.assertEqual(fs['total_bytes'],actual.f_blocks*actual.f_frsize);self.assertIsInstance(fs['mount_id'],int)
  checksum=result.pop('receipt_sha256');self.assertEqual(checksum,H.digest(H.canonical(result)))
 def test_stable_configuration_ignores_activity_but_tracks_limits_and_files(self):
  with patch.object(H,'observe_units',return_value=self.units()):first=H.collect(self.registry,'b'*64,self.request)
  with patch.object(H,'observe_units',return_value=self.units(memory=900)):dynamic=H.collect(self.registry,'b'*64,self.request)
  self.assertEqual(first['configuration_sha256'],dynamic['configuration_sha256']);self.assertNotEqual(first['facts_sha256'],dynamic['facts_sha256'])
  with patch.object(H,'observe_units',return_value=self.units(limit=999)):changed=H.collect(self.registry,'b'*64,self.request)
  self.assertNotEqual(first['configuration_sha256'],changed['configuration_sha256'])
  self.identity.write_text('new content')
  with patch.object(H,'observe_units',return_value=self.units()):changed=H.collect(self.registry,'b'*64,self.request)
  self.assertNotEqual(first['configuration_sha256'],changed['configuration_sha256'])
 def test_missing_metadata_is_explicit_incomplete_not_green(self):
  self.identity.unlink()
  with patch.object(H,'observe_units',side_effect=H.Unavailable('command_timeout')):result=H.collect(self.registry,'b'*64,self.request)
  self.assertFalse(result['configuration_complete']);self.assertEqual(result['facts']['services']['worker']['state'],'unavailable');self.assertEqual(result['facts']['services']['worker']['identity_files']['program']['reason'],'not_found')
 def test_invocation_identity_is_fixed_safe_metadata(self):
  value=H.service_projection({'Id':'fixture.service','InvocationID':'a'*32})
  self.assertEqual(value['fields']['InvocationID'],'a'*32)
  for unsafe in ('','ENV_TOKEN=secret','/proc/1/environ','a'*64):
   value=H.service_projection({'Id':'fixture.service','InvocationID':unsafe})
   self.assertIsNone(value['fields']['InvocationID']);self.assertIn('InvocationID',value['unavailable_fields'])
   self.assertNotIn('secret',json.dumps(value))
 def test_duration_and_projection_exclude_unclassified_strings(self):
  self.assertEqual(H.duration('1w 5d 5h 46min 56.831195s'),1057616831195)
  self.assertEqual(H.duration('2s'),2000000);self.assertIsNone(H.duration('infinity'))
  result=H.service_projection({'LoadState':'loaded','ActiveState':'SECRET','MainPID':'123','CPUQuotaPerSecUSec':'2s'})
  self.assertEqual(result['fields']['ActiveState'],'unrecognized');self.assertNotIn('SECRET',json.dumps(result))
  with patch.object(H,'bounded_command',return_value=b'Id=fixture.service\nEnvironment=SECRET\n'):
   with self.assertRaises(H.Unavailable):H.observe_units(self.registry['targets']['fixture'])
 def test_command_timeout_and_output_are_bounded(self):
  with self.assertRaises(H.Unavailable) as timed:H.bounded_command(['/usr/bin/python3','-c','import time;time.sleep(30)'],seconds=.05)
  self.assertEqual(timed.exception.code,'command_timeout')
  with self.assertRaises(H.Unavailable) as large:H.bounded_command(['/usr/bin/python3','-c','print("x"*65536)'],limit=1024)
  self.assertEqual(large.exception.code,'command_output_limit')
 def test_typed_http_omits_unknown_fields_and_refuses_redirect(self):
  class Handler(BaseHTTPRequestHandler):
   redirect=False
   def do_GET(self):
    if self.redirect:self.send_response(302);self.send_header('Location','http://example.invalid/secret');self.end_headers();return
    self.send_response(200);self.send_header('Content-Type','application/json');self.end_headers();self.wfile.write(json.dumps({'_cpu_percent':10,'_load':2,'_cores':32,'sensor':40,'_top':'SECRET','credentials':'SECRET'}).encode())
   def log_message(self,*args):pass
  server=HTTPServer(('127.0.0.1',0),Handler);thread=threading.Thread(target=server.serve_forever,daemon=True);thread.start()
  try:
   config={'adapter':'temp_api_v1','port':server.server_port,'temperature_fields':['sensor']};value=H.temp_health(config);self.assertNotIn('SECRET',json.dumps(value));self.assertEqual(value['values']['sensor'],40)
   Handler.redirect=True
   with self.assertRaises(H.Unavailable) as error:H.temp_health(config)
   self.assertEqual(error.exception.code,'health_http_status')
  finally:server.shutdown();server.server_close();thread.join()

class LifecycleTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name);self.jobs=self.root/'jobs';self.jobs.mkdir();self.job=str(uuid.uuid4());(self.jobs/self.job).mkdir();self.observation='a'*64;self.calls=[]
  def regular(path):
   info=path.lstat()
   if not stat.S_ISREG(info.st_mode) or info.st_nlink!=1:raise ValueError('not regular')
   return info
  def write(path,value):
   temporary=path.with_suffix('.tmp');temporary.write_text(json.dumps(value));temporary.chmod(0o600);os.replace(temporary,path)
  H.R=SimpleNamespace(job_path=lambda job:self.jobs/str(uuid.UUID(job)),SERVER_OBSERVATIONS_ROOT=self.root/'state',regular=regular,completion_write=write,completion_service_active=lambda unit:False,run=lambda *args,**kwargs:self.calls.append(args),completion_launch_capacity=lambda:None)
 def tearDown(self):self.tmp.cleanup()
 def request(self):
  value={'schema_version':1,'request_id':self.observation,'owner_job':self.job,'owner_task':1,'target_id':'fixture','registry_sha256':'b'*64}
  directory=self.jobs/self.job/'server-observations'/self.observation;directory.mkdir(parents=True,exist_ok=True);path=directory/'request.json';path.write_text(json.dumps(value));path.chmod(0o600);return value
 def test_stop_before_request_prevents_late_start(self):
  self.assertEqual(H.observation_stop(self.job,self.observation)['state'],'stopped');self.request()
  value=H.observation_start(self.job,self.observation);self.assertEqual(value['state'],'cancelled');self.assertEqual(self.calls,[])
  self.assertEqual(H.observation_status(self.job,self.observation),value)
 def test_status_during_launch_does_not_manufacture_timeout(self):
  self.request();stage,request,raw=H.read_request(self.job,self.observation)
  H.write_state(stage/'receipt.json',dict(H.base_receipt(request,raw),state='observing',launched_at=H.stamp()))
  with (H.job_state(self.job)/'launch.lock').open('a') as guard:
   fcntl.flock(guard,fcntl.LOCK_EX);value=H.observation_status(self.job,self.observation);self.assertEqual(value['state'],'observing');self.assertNotIn('receipt_sha256',value)
  value=H.observation_status(self.job,self.observation);self.assertEqual(value['state'],'unavailable');self.assertEqual(self.calls,[])
  self.assertEqual(H.observation_status(self.job,self.observation),value)
 def test_terminal_receipt_tamper_is_rejected(self):
  self.request();stage,request,raw=H.read_request(self.job,self.observation);value=H.terminal_failure(stage,request,raw,'unavailable','fixture')
  value['reason']='tampered';H.write_state(stage/'receipt.json',value)
  with self.assertRaises(H.Invalid):H.read_receipt(stage)
 def test_frozen_request_survives_controller_file_deletion(self):
  self.request();stage,request,raw=H.read_request(self.job,self.observation)
  (self.jobs/self.job/'server-observations'/self.observation/'request.json').unlink()
  self.assertEqual(H.read_request(self.job,self.observation)[2],raw);self.assertEqual(H.observation_status(self.job,self.observation)['state'],'waiting')
 def test_job_cancel_is_a_tombstone_for_unpublished_ids(self):
  with patch.object(H,'bounded_command',return_value=b''):self.assertEqual(H.observation_stop(self.job)['state'],'stopped')
  self.request();self.assertEqual(H.observation_start(self.job,self.observation)['state'],'cancelled');self.assertEqual(self.calls,[])

if __name__=='__main__':unittest.main()
