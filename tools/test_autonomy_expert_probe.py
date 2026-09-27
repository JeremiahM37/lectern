"""Bounded trusted probe request, lifecycle and immutable source regression checks."""
import base64,fcntl,hashlib,importlib.util,json,os,tempfile,time,unittest,uuid
from pathlib import Path
from types import SimpleNamespace
from unittest.mock import patch

def module(name,path):
 spec=importlib.util.spec_from_file_location(name,path);out=importlib.util.module_from_spec(spec);spec.loader.exec_module(out);return out
R=module('probe_runner',Path(__file__).with_name('autonomy-runner.py'))
H=module('probe_helper',Path(__file__).with_name('autonomy-expert-probe.py'))

class ProbeTests(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory();self.addCleanup(self.temp.cleanup);self.root=Path(self.temp.name)
  self.owner=str(uuid.uuid4());self.source=str(uuid.uuid4());(self.root/self.owner).mkdir();(self.root/self.source).mkdir()
  self.req=dict(schema_version=1,owner_job=self.owner,owner_task=2,role='auditor_a',audit_revision=1,progress_key='a'*64,root_task_id=1,source_job=self.source,source_task_id=1,source_archive_sha256='b'*64,root_acceptance_sha256='c'*64,source_acceptance_sha256='d'*64,profile='ordinary180',script='print(42)',fixtures=[],argv=[],runtime=dict(python_bundle_key='',python_input_key='',browser_key='',python_test_key=''))
  self.patcher=patch.object(R,'ROOT',self.root);self.patcher.start();self.addCleanup(self.patcher.stop)
  H.R=SimpleNamespace(**R.__dict__)
 def encoded(self):
  raw=json.dumps(self.req).encode();key=H.sha(raw);probe=H.sha((self.req['progress_key']+':2:'+key).encode());return raw,key,probe
 def test_exact_request_bytes_and_owner_binding(self):
  raw,key,probe=self.encoded();self.assertEqual(H.validate_request(self.owner,probe,raw)[1],key)
  with self.assertRaises(ValueError):H.validate_request(self.owner,probe,raw+b' ')
  with self.assertRaises(ValueError):H.validate_request(self.source,probe,raw)
 def test_fixture_escape_duplicate_collision(self):
  for names in [['../bad'],['/host'],['main.py'],['a','a/b'],['same','same']]:
   self.req['fixtures']=[{'path':n,'content':base64.b64encode(b'x').decode()} for n in names]
   raw,_,probe=self.encoded()
   with self.subTest(names=names),self.assertRaises(ValueError):H.validate_request(self.owner,probe,raw)
 def test_profile_and_runtime_conflicts(self):
  self.req['profile']='extended600';raw,_,probe=self.encoded()
  with self.assertRaises(ValueError):H.validate_request(self.owner,probe,raw)
  self.req['profile']='ordinary180';self.req['runtime']['browser_key']='e'*64;raw,_,probe=self.encoded()
  with self.assertRaises(ValueError):H.validate_request(self.owner,probe,raw)
 def test_socket_free_mounts_and_normal_site(self):
  cmd=H.sandbox_command(self.req)
  self.assertIn('--unshare-all',cmd);self.assertIn('--clearenv',cmd);self.assertIn('--remount-ro',cmd)
  self.assertNotIn('-S',cmd);self.assertFalse(any('bridge' in arg or '.codex' in arg for arg in cmd))
 def test_cancelled_reservation_cannot_launch(self):
  raw,key,probe=self.encoded();stage=self.root/'stage';stage.mkdir();(stage/'cancel.json').write_text('{}')
  records=[]
  with patch.object(H,'request_for',return_value=(stage,self.req,key)),patch.object(H,'receipt',return_value=None),patch.object(H,'write',side_effect=lambda p,v:records.append(v)),patch.object(H.R,'completion_service_active',side_effect=AssertionError('must not launch')):
   receipt=H.start(self.owner,probe)
  self.assertEqual(receipt['state'],'cancelled');self.assertFalse(receipt['executed']);self.assertEqual(receipt['charged_ms'],0)
 def test_cancelled_running_probe_cannot_be_relabelled_unexecuted(self):
  raw,key,probe=self.encoded();stage=self.root/'stage';stage.mkdir();(stage/'cancel.json').write_text('{}')
  running=dict(H.base_receipt(self.owner,probe,self.req,key),state='running',executed=True,started_at=H.now())
  with patch.object(H,'request_for',return_value=(stage,self.req,key)),patch.object(H,'receipt',return_value=running),patch.object(H.R,'completion_service_active',return_value=True),patch.object(H,'write',side_effect=AssertionError('must not erase running accounting')):
   value=H.start(self.owner,probe)
  self.assertTrue(value['executed']);self.assertEqual(value['state'],'running')
 def test_frozen_launcher_survives_installed_runner_change(self):
  stage=self.root/'stage';stage.mkdir();source=self.root/'installed.py';source.write_text('print("version one")');source.chmod(0o400)
  with patch.object(H.R,'__file__',str(source)):
   entry=H.freeze_launcher(stage);before=(stage/'runner.py').read_bytes();launcher=entry.read_bytes()
   source.chmod(0o600);source.write_text('print("version two")');source.chmod(0o400)
   self.assertEqual(H.freeze_launcher(stage),entry)
  self.assertEqual((stage/'runner.py').read_bytes(),before);self.assertEqual(entry.read_bytes(),launcher)
 def test_status_never_starts(self):
  raw,key,probe=self.encoded();stage=self.root/'stage';stage.mkdir()
  with patch.object(H,'request_for',return_value=(stage,self.req,key)),patch.object(H,'receipt',return_value=None),patch.object(H.R,'completion_service_active',return_value=False),patch.object(H.R,'run',side_effect=AssertionError('read status must not launch')):
   self.assertEqual(H.status(self.owner,probe)['state'],'waiting')
 def test_status_does_not_overwrite_receipt_finalized_during_unit_query(self):
  raw,key,probe=self.encoded();stage=self.root/'stage';stage.mkdir()
  running=dict(H.base_receipt(self.owner,probe,self.req,key),state='running',executed=True,started_at=H.now())
  terminal=dict(running,state='exited',exit_code=0,receipt_sha256='e'*64)
  with patch.object(H,'request_for',return_value=(stage,self.req,key)),patch.object(H,'receipt',side_effect=[running,terminal]),patch.object(H.R,'completion_service_active',return_value=False),patch.object(H,'write',side_effect=AssertionError('must not replace finalized receipt')):
   self.assertEqual(H.status(self.owner,probe),terminal)
 def test_nonexecution_and_uncertain_execution_distinct(self):
  raw,key,probe=self.encoded();value=H.base_receipt(self.owner,probe,self.req,key);self.assertFalse(value['executed'])
  value.update(executed=True,started_at=H.now())
  with patch.object(H,'write'):
   result=H.interrupted(self.root,value,'lost supervisor')
  self.assertTrue(result['executed']);self.assertEqual(result['charged_ms'],180000)
 def test_stop_before_request_published_persists_tombstone(self):
  _,_,probe=self.encoded()
  with patch.object(H,'write',side_effect=lambda p,v:p.write_text(json.dumps(v))),patch.object(H.R,'completion_service_active',return_value=False):
   result=H.stop(self.owner,probe)
  self.assertEqual(result['state'],'stopped')
  stage=self.root/self.owner/'expert-probes'/probe
  self.assertTrue((stage/'cancel.json').is_file())
  raw,key,_=self.encoded();requests=self.root/self.owner/'expert-probe-requests';requests.mkdir(mode=0o700)
  path=requests/(probe+'.json');path.write_bytes(raw);path.chmod(0o600)
  with patch.object(H.R,'archive_identity',return_value={'sha256':'b'*64}),patch.object(H,'write',side_effect=lambda p,v:p.write_text(json.dumps(v))),patch.object(H.R,'completion_service_active',side_effect=AssertionError('late publication must not launch')):
   result=H.start(self.owner,probe)
  self.assertEqual(result['state'],'cancelled');self.assertFalse(result['executed'])
 def test_stop_does_not_confirm_while_launcher_owns_guard(self):
  _,_,probe=self.encoded();owner,stage=H.paths(self.owner,probe);H.private(owner/'expert-probes');H.private(stage)
  with (stage/'guard').open('a') as guard,patch.object(H,'write',side_effect=lambda p,v:p.write_text(json.dumps(v))):
   fcntl.flock(guard,fcntl.LOCK_EX)
   self.assertEqual(H.stop(self.owner,probe)['state'],'stopping')
   self.assertTrue((stage/'cancel.json').exists())
  with patch.object(H,'write',side_effect=lambda p,v:p.write_text(json.dumps(v))),patch.object(H.R,'completion_service_active',side_effect=RuntimeError('query failed')):
   with self.assertRaises(RuntimeError):H.stop(self.owner,probe)
 def test_terminal_receipt_digest_is_verified(self):
  stage=self.root/'stage';stage.mkdir();value={'state':'exited','executed':True,'exit_code':0}
  with patch.object(H,'write',side_effect=lambda p,v:p.write_text(json.dumps(v))):H.seal_receipt(stage,value)
  self.assertEqual(H.receipt(stage)['exit_code'],0)
  path=stage/'receipt.json';corrupt=json.loads(path.read_text());corrupt['exit_code']=1;path.write_text(json.dumps(corrupt))
  with self.assertRaises(ValueError):H.receipt(stage)
 def test_monotonic_worker_usage_excludes_preparation(self):
  p=self.root/self.owner;(p/'job.json').write_text('{"runtime_seconds":10}')
  self.assertEqual(R.usage_elapsed(self.owner,True)['elapsed_milliseconds'],0)
  with patch.object(R,'completion_write',side_effect=lambda p,v:p.write_text(json.dumps(v))):
   R.usage_begin(self.owner);time.sleep(.02);self.assertGreaterEqual(R.usage_elapsed(self.owner,True)['elapsed_milliseconds'],15)
   R.usage_finish(self.owner);first=R.usage_elapsed(self.owner,False);time.sleep(.02);self.assertEqual(R.usage_elapsed(self.owner,False),first)
 def test_unknown_worker_end_consumes_reserved_limit(self):
  p=self.root/self.owner;(p/'job.json').write_text('{"runtime_seconds":7}');(p/'worker-usage.json').write_text('{"boot_id":"old","started_monotonic_ns":0}')
  self.assertEqual(R.usage_elapsed(self.owner,False),{'elapsed_milliseconds':7000,'usage_accounting':'reserved_limit_unknown_end'})


class ArchiveWorkTests(unittest.TestCase):
 def setUp(self):
  fixtures=module('completion_fixtures',Path(__file__).with_name('test_autonomy_completion.py'))
  self.f=fixtures.CompletionTests('test_prepare_uses_archived_source_and_retains_old_report');self.f.setUp();self.addCleanup(self.f.doCleanups);self.r=fixtures.RUNNER
 def test_archive_work_exact_source_report_handoff_and_retry(self):
  f=self.f;r=self.r
  (f.jobs/f.source/'work/code.py').write_text('mutable drift')
  receipt=r.copy_archive_work(f.destination,f.source);work=f.jobs/f.destination/'work'
  self.assertEqual((work/'code.py').read_bytes(),b'assert True\n')
  self.assertFalse((work/'autonomy-report.json').exists())
  self.assertEqual((work/'.lectern-reports'/f.source/'autonomy-report.json').read_bytes(),b'{"old":true}\r\n')
  self.assertEqual(receipt['source_archive_sha256'],r.archive_identity(f.source)['sha256'])
  self.assertEqual(r.copy_archive_work(f.destination,f.source),receipt)
  with self.assertRaises(FileNotFoundError):r.report(f.destination)
 def test_copy_generation_revokes_late_launch_and_preserves_new_authority(self):
  f=self.f;r=self.r
  with patch.object(r,'completion_service_active',return_value=False),patch.object(r,'run') as launch:
   self.assertEqual(r.completion_stop(f.destination,generation=1),{'state':'stopped'})
   denied=r.completion_copy_status(f.destination,f.source,'archive-work',generation=1)
   self.assertTrue(denied['revoked']);self.assertEqual(launch.call_count,0)
   allowed=r.completion_copy_status(f.destination,f.source,'archive-work',generation=2)
   self.assertEqual(allowed['copy_generation'],2);self.assertEqual(allowed['state'],'copying');self.assertEqual(launch.call_count,1)
   late=r.completion_copy_status(f.destination,f.source,'archive-work',generation=1)
   self.assertTrue(late['revoked']);self.assertEqual(launch.call_count,1)
   stage=f.jobs/f.destination/'completion'
   self.assertEqual(r.completion_json(stage/'archive-copy-authority.json')['generation'],2)
 def test_archive_resume_retains_exact_malformed_report(self):
  f=self.f;r=self.r
  malformed=b'{"broken": truncated\r\n';(f.jobs/f.source/'work/autonomy-report.json').write_bytes(malformed);(f.jobs/f.source/'artifact.tar.gz').chmod(0o600);f.archive(f.source)
  receipt=r.copy_archive_work(f.destination,f.source,preserve_report=True)
  self.assertEqual((f.jobs/f.destination/'work/autonomy-report.json').read_bytes(),malformed)
  self.assertEqual(receipt['source_archive_sha256'],r.archive_identity(f.source)['sha256'])
  self.assertEqual(r.completion_copy_receipt(f.root,f.source,'archive-resume').name,'copy-report-resume.json')
 def test_archive_work_refuses_unreserved_existing_work(self):
  work=self.f.jobs/self.f.destination/'work';(work/'keep').write_text('retained')
  with self.assertRaises(ValueError):self.r.copy_archive_work(self.f.destination,self.f.source)
  self.assertEqual((work/'keep').read_text(),'retained')

if __name__=='__main__':unittest.main()
