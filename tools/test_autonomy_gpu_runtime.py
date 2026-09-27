import hashlib,importlib.util,json,os,stat,tempfile,unittest,uuid
from pathlib import Path
from unittest import mock
s=importlib.util.spec_from_file_location('gpu_runtime',Path(__file__).with_name('autonomy-gpu-runtime.py'));H=importlib.util.module_from_spec(s);s.loader.exec_module(H)
class Runner:
 def __init__(self,root):self.ROOT=root/'jobs';self.ROOT.mkdir()
 def job_path(self,job):
  if str(uuid.UUID(job))!=job:raise ValueError('job')
  return self.ROOT/job
 def regular(self,path):
  st=path.lstat()
  if not stat.S_ISREG(st.st_mode) or st.st_nlink!=1:raise ValueError('file')
  return st
 def completion_service_active(self,name):return False
class RuntimeTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.R=Runner(Path(self.tmp.name));self.job=str(uuid.uuid4());self.source=str(uuid.uuid4())
  self.value={'schema_version':1,'owner_job':self.job,'owner_task':1,'role':'builder','experiment_root':'a'*64,'source_job':self.source,'source_snapshot_id':'','source_tree_sha256':'','source_archive_sha256':'b'*64,'admission_sha256':'c'*64,'acceptance_sha256':'d'*64,'target':H.L.TARGET,'runtime_key':'e'*64,'qualification_sha256':'7'*64,'profile':'gpu-screen600','script':'print(1)','argv':[],'trial':0}
  self.raw=json.dumps(self.value,separators=(',',':')).encode();self.key=hashlib.sha256(self.raw).hexdigest();self.run=hashlib.sha256((self.job+':'+self.key).encode()).hexdigest()
  directory=self.R.job_path(self.job)/'gpu-requests';directory.mkdir(parents=True);self.path=directory/(self.run+'.json');self.path.write_bytes(self.raw);self.path.chmod(0o600)
  original=H.L.Store;self.patch=mock.patch.object(H.L,'Store',side_effect=lambda p:original(p,os.getuid()));self.patch.start()
 def tearDown(self):self.patch.stop();self.tmp.cleanup()
 def test_request_exact_owner_identity_and_input_bounds(self):
  self.assertEqual(H.request(self.R,self.job,self.run),(self.value,self.key))
  self.path.write_bytes(self.raw+b' ')
  with self.assertRaises(ValueError):H.request(self.R,self.job,self.run)
 def test_off_before_dispatch_retains_terminal_bound_receipt(self):
  with mock.patch.object(H.subprocess,'run'):
   stop=H.stop(self.R,self.job,self.run)
  self.assertEqual(stop['state'],'stopped');self.assertEqual(stop['request_sha256'],self.key)
  status=H.status(self.R,self.job,self.run)
  self.assertEqual(status['state'],'cancelled');self.assertIs(status['executed'],False);self.assertEqual(status['charged_ms'],0);self.assertTrue(status['cleanup_confirmed'])
  digest=status.pop('receipt_sha256');self.assertEqual(digest,H.L.digest(status))
  with mock.patch.object(H.subprocess,'run') as launch:
   self.assertEqual(H.launch(self.R,self.job,self.run)['state'],'cancelled');launch.assert_not_called()
 def test_off_before_request_publication_blocks_late_launch(self):
  self.path.unlink()
  with mock.patch.object(H.subprocess,'run'):stop=H.stop(self.R,self.job,self.run)
  self.assertEqual(stop['state'],'stopped');self.assertEqual(stop['evidence_scope'],'revoked_missing_request')
  self.path.write_bytes(self.raw);self.path.chmod(0o600)
  with mock.patch.object(H.subprocess,'run') as launch:
   result=H.launch(self.R,self.job,self.run);launch.assert_not_called()
  self.assertEqual(result['state'],'cancelled');self.assertEqual(result['request_sha256'],self.key)
 def test_unqualified_runtime_has_actionable_nonexecution(self):
  with mock.patch.object(H,'qualification',side_effect=H.Unsupported('no measured control proof')),mock.patch.object(H.subprocess,'run') as launch:
   result=H.launch(self.R,self.job,self.run);launch.assert_not_called()
  self.assertEqual(result['state'],'unavailable');self.assertFalse(result['executed']);self.assertTrue(result['cleanup_confirmed']);self.assertIn('proof',result['reason']);self.assertEqual(len(result['receipt_sha256']),64)
 def test_raw_elapsed_retained_and_charge_bounded(self):
  store=H.stage(self.R,self.run);bound={'run_id':self.run};execution={'binding':bound,'state':'timeout','elapsed_ms':600007,'exit_code':-9,'output_sha256':'f'*64}
  class Guest:
   def status(s,b):return {'execution':execution,'execution_receipt_sha256':H.L.digest(execution)}
  class Fence:
   def cancel(s,b,g):return {'state':'stopped'}
  self.assertTrue(H.finish(store,self.job,self.run,self.key,bound,Guest(),Fence()))
  result=store.read('receipt.json');self.assertEqual(result['elapsed_ms'],600007);self.assertEqual(result['charged_ms'],600000)
 def test_foreign_stop_cannot_revoke_sealed_run(self):
  store=H.stage(self.R,self.run);H.seal_request(self.R,self.job,self.run,store)
  with self.assertRaises(ValueError):H.stop(self.R,str(uuid.uuid4()),self.run)
  self.assertIsNone(store.read('cancel.json'))
 def test_terminal_receipt_is_immutable_after_reconciliation(self):
  store=H.stage(self.R,self.run);result=dict(H.common(self.job,self.run,self.key),state='cancelled',executed=False,charged_ms=0,cleanup_confirmed=True)
  store.write('receipt.json',result)
  with mock.patch.object(H,'GuestTransport') as guest:
   self.assertTrue(H.finish(store,self.job,self.run,self.key,{},guest,None));guest.assert_not_called()
  self.assertEqual(store.read('receipt.json'),result)
 def test_stop_does_not_confirm_while_cleanup_unit_remains_active(self):
  store=H.stage(self.R,self.run);H.seal_request(self.R,self.job,self.run,store)
  store.write('receipt.json',dict(H.common(self.job,self.run,self.key),state='cancelled',executed=False,charged_ms=0,cleanup_confirmed=True))
  with mock.patch.object(H.subprocess,'run'),mock.patch.object(self.R,'completion_service_active',side_effect=lambda n:n==H.unit(self.run,True)):
   self.assertEqual(H.stop(self.R,self.job,self.run)['state'],'stopping')
 def test_recorded_dispatch_reconciles_without_relaunch(self):
  store=H.stage(self.R,self.run);H.seal_request(self.R,self.job,self.run,store);bound={'run_id':self.run}
  store.write('binding.json',bound);store.write('dispatch.json',{'binding':bound})
  guest=mock.Mock();fence=mock.Mock()
  with mock.patch.object(H,'finish',return_value=True) as finish,mock.patch.object(H,'qualification') as qualification:
   H.execute(self.R,self.job,self.run,guest,fence)
   qualification.assert_not_called();guest.prepare.assert_not_called();guest.start.assert_not_called();self.assertGreaterEqual(finish.call_count,1)
 def test_retained_dispatch_launch_reconciles_before_current_qualification(self):
  store=H.stage(self.R,self.run);H.seal_request(self.R,self.job,self.run,store)
  bound={'run_id':self.run};store.write('binding.json',bound);store.write('dispatch.json',{'binding':bound})
  with mock.patch.object(H,'qualification',side_effect=H.Unsupported('global helper changed')) as qualification,mock.patch.object(H,'freeze_tools',return_value=store.root),mock.patch.object(H.subprocess,'run') as launch:
   result=H.launch(self.R,self.job,self.run)
   qualification.assert_not_called();launch.assert_called_once();self.assertEqual(result['state'],'preparing')
  guest=mock.Mock();guest.status.return_value={};fence=mock.Mock();fence.cancel.return_value={'state':'stopped'}
  H.execute(self.R,self.job,self.run,guest,fence)
  receipt=store.read('receipt.json');self.assertIsNone(receipt['executed']);self.assertEqual(receipt['charged_ms'],600000);self.assertTrue(receipt['cleanup_confirmed']);fence.cancel.assert_called_once()
 def test_frozen_helpers_reuse_without_global_files(self):
  store=H.stage(self.R,self.run);directory=store.root/'tools';directory.mkdir()
  names=('autonomy-gpu-runtime.py','autonomy-gpu-lease.py','autonomy-gpu-executor.py','autonomy-gpu-telemetry.py','autonomy-gpu-snapshot.py','autonomy-runner.py')
  hashes={}
  for name in names:
   path=directory/name;path.write_bytes(b'frozen');path.chmod(0o444);hashes[name]=hashlib.sha256(b'frozen').hexdigest()
  store.write('tools.json',hashes);self.R.INSTALL='/missing/global/runner'
  self.R.digest_file=lambda p:hashlib.sha256(p.read_bytes()).hexdigest()
  def metadata(path):
   actual=path.lstat();return mock.Mock(st_uid=0,st_mode=actual.st_mode)
  with mock.patch.object(self.R,'regular',side_effect=metadata),mock.patch.object(H,'companion',side_effect=lambda n:Path('/missing/global')/n):
   self.assertEqual(H.freeze_tools(self.R,store),directory)
   (directory/names[0]).chmod(0o644)
   with self.assertRaisesRegex(ValueError,'changed'):H.freeze_tools(self.R,store)
 def test_qualification_identity_is_pinned_before_launch(self):
  directory=self.R.ROOT.parent/'dependencies/gpu';directory.mkdir(parents=True)
  value={'schema_version':1,'qualified':True,'target':H.L.TARGET,'runtime_key':self.value['runtime_key'],'profile':'gpu-screen600','device_bdf':'0000:f4:00.0','kernel_release':'fixture','driver_sha256':'a'*64}
  checksum=H.L.digest(value);path=directory/'qualification.json';path.write_text(json.dumps(dict(value,receipt_sha256=checksum)));path.chmod(0o444)
  metadata=mock.Mock(st_uid=0,st_mode=0o100444,st_size=path.stat().st_size)
  with mock.patch.object(self.R,'regular',return_value=metadata),mock.patch.object(H,'qualified_helpers'):
   with self.assertRaisesRegex(H.Unsupported,'changed after admission'):H.qualification(self.R,self.value)
   selected=dict(self.value,qualification_sha256=checksum)
   self.assertEqual(H.qualification(self.R,selected)['receipt_sha256'],checksum)
 def test_guest_output_is_bound_to_receipt_and_fixed_remote_path(self):
  store=H.stage(self.R,self.run);payload=b'actual GPU experiment output'
  bound={'schema_version':1,'target':H.L.TARGET,'run_id':self.run,'generation':1,'request_sha256':'a'*64,'guest_boot_id':str(uuid.uuid4())}
  execution={'output_bytes':len(payload),'output_sha256':hashlib.sha256(payload).hexdigest()}
  def pull(command,**kwargs):
   self.assertEqual(command[:4],['/usr/sbin/pct','pull','105','/var/lib/lectern-gpu-lease/runs/'+self.run+'/output.log'])
   Path(command[4]).write_bytes(payload)
  with mock.patch.object(H.subprocess,'run',side_effect=pull):H.GuestTransport().output(bound,store,execution)
  self.assertEqual((store.root/'output.log').read_bytes(),payload)
  (store.root/'output.log').chmod(0o600);(store.root/'output.log').write_bytes(b'tamper')
  with self.assertRaisesRegex(ValueError,'retained output differs'):H.GuestTransport().output(bound,store,execution)
 def test_guest_helper_drift_is_not_qualified_execution(self):
  qualified={'helpers':{'executor_sha256':'a'*64,'lease_sha256':'b'*64}}
  guest=mock.Mock();guest.identity.return_value={'target':H.L.TARGET,'helpers':qualified['helpers'],'guest_boot_id':str(uuid.uuid4())}
  self.assertEqual(H.guest_identity(guest,qualified)['target'],H.L.TARGET)
  guest.identity.return_value['helpers']={'executor_sha256':'c'*64,'lease_sha256':'b'*64}
  with self.assertRaisesRegex(H.Unsupported,'guest control helper changed'):H.guest_identity(guest,qualified)
if __name__=='__main__':unittest.main()
