import importlib.util
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location('runner', Path(__file__).with_name('autonomy-runner.py'))
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)

class StorageTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
  self.root=Path(self.tmp.name)
  self.cache=patch.object(r,'ASSET_CACHE',self.root/'cache');self.cache.start();self.addCleanup(self.cache.stop)
  self.jobs=patch.object(r,'ROOT',self.root/'jobs');self.jobs.start();self.addCleanup(self.jobs.stop)
  r.ROOT.mkdir()
  dep=patch.object(r,'DEPENDENCIES',self.root/'dependencies');dep.start();self.addCleanup(dep.stop)
 def test_lossless_sharing_and_accounting(self):
  source=self.root/'source';source.write_bytes(b'ELF fixture'*10000)
  a=r.ROOT/'a';a.mkdir();b=r.ROOT/'b';b.mkdir()
  r.shared_binary(source,a/'agent');r.shared_binary(source,b/'agent')
  self.assertEqual((a/'agent').read_bytes(),source.read_bytes())
  self.assertEqual((a/'agent').stat().st_ino,(b/'agent').stat().st_ino)
  self.assertEqual((a/'agent').stat().st_mode & 0o222,0)
  self.assertLess(r.allocated_storage(),source.stat().st_size*2)
  source.write_bytes(b'new version')
  r.shared_binary(source,b/'helper')
  self.assertEqual((a/'agent').read_bytes(),b'ELF fixture'*10000)
 def test_compaction_preserves_existing_file(self):
  source=r.ROOT/'agent';source.write_bytes(b'unchanged');source.chmod(0o555)
  r.shared_binary(source,source)
  self.assertEqual(source.read_bytes(),b'unchanged')
  r.shared_binary(source,source)
  self.assertEqual(source.read_bytes(),b'unchanged')
 def test_symlink_and_corruption_refused(self):
  source=self.root/'source';source.write_bytes(b'good')
  target=r.ROOT/'agent';r.shared_binary(source,target)
  cache=r.ASSET_CACHE/r.digest_file(source)
  cache.chmod(0o755)
  with self.assertRaises(ValueError):r.shared_binary(source,r.ROOT/'second')
  cache.write_bytes(b'evil');cache.chmod(0o555)
  with self.assertRaises(ValueError):r.shared_binary(source,r.ROOT/'second')
 def test_cache_symlink_refused(self):
  outside=self.root/'outside';outside.mkdir();r.ASSET_CACHE.symlink_to(outside)
  source=self.root/'source';source.write_bytes(b'good')
  with self.assertRaises(ValueError):r.shared_binary(source,r.ROOT/'agent')
 def test_compact_preserves_work_and_skips_live_units(self):
  import uuid
  from types import SimpleNamespace
  jobs=[]
  for _ in range(2):
   job=r.ROOT/str(uuid.uuid4());job.mkdir();(job/'assets').mkdir();(job/'work').mkdir()
   (job/'work'/'evidence').write_text('original research')
   (job/'result.json').write_text('{"state":"done"}')
   binary=job/'assets'/'agent';binary.write_bytes(b'same ELF fixture');binary.chmod(0o555);jobs.append(job)
  live=jobs[0]/'assets'/'agent';inode=live.stat().st_ino
  with patch.object(r,'run',side_effect=lambda args: SimpleNamespace(stdout='active' if jobs[0].name in ' '.join(args) else 'inactive')):
   r.compact_assets()
  self.assertEqual(live.stat().st_ino,inode)
  for job in jobs:self.assertEqual((job/'work'/'evidence').read_text(),'original research')
  with patch.object(r,'run',return_value=SimpleNamespace(stdout='inactive')):r.compact_assets()
  self.assertEqual(live.stat().st_ino,(jobs[1]/'assets'/'agent').stat().st_ino)
 def test_capacity_and_free_space_guards(self):
  with patch.object(r,'allocated_storage',return_value=199*1024**3), patch.object(r.shutil,'disk_usage',return_value=type('Disk',(),{'free':21*1024**3})()):
   self.assertTrue(r.storage_status()['ready'])
  with patch.object(r,'allocated_storage',return_value=200*1024**3):self.assertFalse(r.storage_status()['ready'])
  with patch.object(r.shutil,'disk_usage',return_value=type('Disk',(),{'free':19*1024**3})()):self.assertFalse(r.storage_status()['ready'])

class ReviewEvidenceTests(unittest.TestCase):
 def test_separate_snapshot_preserves_builder_and_hashes_review(self):
  import uuid,os,json
  from types import SimpleNamespace
  with tempfile.TemporaryDirectory() as temporary:
   root=Path(temporary);source=str(uuid.uuid4());destination=str(uuid.uuid4())
   for job in (source,destination):(root/job/'work').mkdir(parents=True)
   src=root/source/'work';dst=root/destination/'work'
   (src/'review').mkdir();(src/'review'/'finding.txt').write_text('independent finding')
   (src/'source.c').write_text('reviewer edit');(dst/'source.c').write_text('builder original')
   (src/'autonomy-report.json').write_text('{"approve":false}')
   (src/'external').symlink_to('/nonexistent/private')
   with patch.object(r,'ROOT',root), patch.object(r,'ensure_work',side_effect=lambda p:p/'work'), patch.object(r,'status',return_value={'state':'done'}), patch.object(r.pwd,'getpwnam',return_value=SimpleNamespace(pw_uid=os.getuid(),pw_gid=os.getgid())):
    r.copy_job(destination,source,review=True)
    self.assertEqual((dst/'source.c').read_text(),'builder original')
    evidence=dst/'.lectern-review'/source
    self.assertEqual((evidence/'work/review/finding.txt').read_text(),'independent finding')
    self.assertEqual((evidence/'work/autonomy-report.json').read_text(),'{"approve":false}')
    self.assertTrue((evidence/'work/external').is_symlink())
    manifest=json.loads((evidence/'manifest.json').read_text())
    row=next(row for row in manifest['files'] if row['path']=='review/finding.txt')
    self.assertEqual(row['sha256'],r.digest_file(src/'review/finding.txt'))
    with self.assertRaises(ValueError):r.copy_job(destination,source,review=True)
    second=str(uuid.uuid4());(root/second/'work').mkdir(parents=True)
    (root/second/'work'/'later.txt').write_text('later review')
    r.copy_job(destination,second,review=True)
    self.assertEqual((evidence/'work/review/finding.txt').read_text(),'independent finding')
    self.assertEqual((dst/'.lectern-review'/second/'work/later.txt').read_text(),'later review')
 def test_refuses_review_directory_symlink(self):
  import uuid
  with tempfile.TemporaryDirectory() as temporary:
   root=Path(temporary);source=str(uuid.uuid4());destination=str(uuid.uuid4())
   for job in (source,destination):(root/job/'work').mkdir(parents=True)
   (root/destination/'work/.lectern-review').symlink_to(root/'outside')
   with patch.object(r,'ROOT',root), patch.object(r,'ensure_work',side_effect=lambda p:p/'work'), patch.object(r,'status',return_value={'state':'done'}):
    with self.assertRaises(ValueError):r.copy_job(destination,source,review=True)
   self.assertFalse((root/'outside').exists())

class ReportHandoffTests(unittest.TestCase):
 def fixture(self):
  import uuid,os
  from types import SimpleNamespace
  temp=tempfile.TemporaryDirectory();self.addCleanup(temp.cleanup)
  root=Path(temp.name);source=str(uuid.uuid4());destination=str(uuid.uuid4())
  for job in (source,destination):(root/job/'work').mkdir(parents=True)
  for mocked in (patch.object(r,'ROOT',root),patch.object(r,'ensure_work',side_effect=lambda p:p/'work'),patch.object(r,'status',return_value={'state':'done'}),patch.object(r.pwd,'getpwnam',return_value=SimpleNamespace(pw_uid=os.getuid(),pw_gid=os.getgid()))):
   mocked.start();self.addCleanup(mocked.stop)
  return root,source,destination
 def test_exact_prior_report_preserved_without_stale_submission(self):
  import json,hashlib,uuid
  root,source,destination=self.fixture();src=root/source/'work';dst=root/destination/'work'
  original=b'{ "outcome": "incomplete", "summary": "honest stop" }\n'
  (src/'autonomy-report.json').write_bytes(original)
  manifest=hashlib.sha256(original).hexdigest()+'  /work/autonomy-report.json\n'
  (src/'SHA256SUMS').write_text(manifest)
  r.copy_job(destination,source)
  self.assertFalse((dst/'autonomy-report.json').exists())
  with self.assertRaises(FileNotFoundError):r.report(destination)
  saved=dst/'.lectern-reports'/source
  self.assertEqual((saved/'autonomy-report.json').read_bytes(),original)
  receipt=json.loads((saved/'manifest.json').read_text())
  self.assertEqual(receipt['sha256'],hashlib.sha256(original).hexdigest())
  self.assertEqual(receipt['original_path'],'autonomy-report.json')
  self.assertEqual((dst/receipt['preserved_path']).read_bytes(),original)
  self.assertEqual((dst/'SHA256SUMS').read_text(),manifest)
  self.assertEqual((src/'autonomy-report.json').read_bytes(),original)
  later=str(uuid.uuid4());(root/later/'work').mkdir(parents=True)
  (dst/'autonomy-report.json').write_text('{"outcome":"ready_for_review"}')
  r.copy_job(later,destination)
  history=root/later/'work/.lectern-reports'
  self.assertEqual((history/source/'autonomy-report.json').read_bytes(),original)
  self.assertEqual((history/destination/'autonomy-report.json').read_text(),'{"outcome":"ready_for_review"}')
  self.assertFalse((root/later/'work/autonomy-report.json').exists())
 def test_reserved_root_symlink_cannot_write_outside_copy(self):
  root,source,destination=self.fixture();src=root/source/'work';outside=root/'outside';outside.mkdir()
  (src/'autonomy-report.json').write_text('{}')
  (src/'.lectern-reports').symlink_to(outside)
  with self.assertRaises(ValueError):r.copy_job(destination,source)
  self.assertEqual(list(outside.iterdir()),[])
 def test_prior_report_symlink_refused(self):
  root,source,destination=self.fixture();outside=root/'secret';outside.write_text('not evidence')
  (root/source/'work/autonomy-report.json').symlink_to(outside)
  with self.assertRaises(ValueError):r.copy_job(destination,source)
  self.assertEqual(outside.read_text(),'not evidence')
 def test_reserved_source_collision_is_not_overwritten(self):
  root,source,destination=self.fixture();src=root/source/'work'
  (src/'autonomy-report.json').write_text('{}')
  reserved=src/'.lectern-reports'/source;reserved.mkdir(parents=True)
  (reserved/'original.txt').write_text('retained evidence')
  with self.assertRaises(ValueError):r.copy_job(destination,source)
  self.assertEqual((reserved/'original.txt').read_text(),'retained evidence')
 def test_legacy_copy_without_report_still_works(self):
  root,source,destination=self.fixture()
  (root/source/'work/code.txt').write_text('checkpoint')
  r.copy_job(destination,source)
  self.assertEqual((root/destination/'work/code.txt').read_text(),'checkpoint')
  self.assertFalse((root/destination/'work/.lectern-reports').exists())

class AuthTests(unittest.TestCase):
 def fixture(self, age=0, expiry=7200):
  import base64,datetime,json,time
  now=time.time()
  claims=base64.urlsafe_b64encode(json.dumps({'exp':now+expiry}).encode()).decode().rstrip('=')
  return {'auth_mode':'chatgpt','last_refresh':datetime.datetime.fromtimestamp(now-age,datetime.timezone.utc).isoformat(),
          'tokens':{'access_token':'header.'+claims+'.signature','refresh_token':'private-refresh'}}
 def test_freshness_boundaries(self):
  self.assertTrue(r.codex_auth_fresh(self.fixture()))
  for auth in [self.fixture(age=7*86400), self.fixture(age=-60), self.fixture(expiry=1200), {},
               dict(self.fixture(),auth_mode='apikey'),dict(self.fixture(),last_refresh='bad')]:
   self.assertFalse(r.codex_auth_fresh(auth))
 def test_snapshot_refreshes_host_only_and_strips_rotation_credential(self):
  import json,os
  from types import SimpleNamespace
  with tempfile.TemporaryDirectory() as temporary:
   root=Path(temporary);auth=root/'auth.json';auth.write_text(json.dumps(self.fixture(age=8*86400)))
   def refresh():auth.write_text(json.dumps(self.fixture()))
   real_stat=os.fstat
   def trusted(fd):
    s=real_stat(fd);return SimpleNamespace(st_mode=s.st_mode,st_nlink=s.st_nlink,st_uid=0)
   with patch.object(r,'AUTH',{'codex':auth}),patch.object(r,'AUTH_LOCK',root/'lock'),patch.object(r.os,'fstat',side_effect=trusted),patch.object(r,'refresh_codex_auth',side_effect=refresh) as call:
    snapshot=json.loads(r.codex_auth_snapshot());self.assertEqual(call.call_count,1)
    self.assertEqual(snapshot['tokens']['refresh_token'],'')
    self.assertEqual(json.loads(auth.read_text())['tokens']['refresh_token'],'private-refresh')
    r.codex_auth_snapshot();self.assertEqual(call.call_count,1)
 def test_refresh_without_persistence_fails_closed(self):
  import json,os
  from types import SimpleNamespace
  with tempfile.TemporaryDirectory() as temporary:
   root=Path(temporary);auth=root/'auth.json';auth.write_text(json.dumps(self.fixture(age=8*86400)))
   original=auth.read_bytes();real_stat=os.fstat
   def trusted(fd):
    s=real_stat(fd);return SimpleNamespace(st_mode=s.st_mode,st_nlink=s.st_nlink,st_uid=0)
   with patch.object(r,'AUTH',{'codex':auth}),patch.object(r,'AUTH_LOCK',root/'lock'),patch.object(r.os,'fstat',side_effect=trusted),patch.object(r,'refresh_codex_auth'):
    with self.assertRaisesRegex(RuntimeError,'did not persist'):r.codex_auth_snapshot()
   self.assertEqual(auth.read_bytes(),original)
 def test_real_protocol_success_and_sanitized_failure(self):
  import subprocess,sys
  real_popen=subprocess.Popen
  for fails in (False,True):
   script="""import sys,json
for line in sys.stdin:
 req=json.loads(line)
 if req.get('id')==1:
  print(json.dumps({'id':1,'result':{}}),flush=True)
 if req.get('id')==2:
  assert req['method']=='account/read' and req['params']['refreshToken'] is True
  print(json.dumps({'id':2,FIELD:VALUE}),flush=True)
""".replace('FIELD',repr('error' if fails else 'result')).replace('VALUE',repr({'message':'PRIVATE-TOKEN'} if fails else {'account':{'type':'chatgpt'}}))
   def launch(command,**kwargs):
    self.assertEqual(command[:3],['/usr/sbin/runuser','-u','admin'])
    return real_popen([sys.executable,'-c',script],**kwargs)
   with patch.object(r.subprocess,'Popen',side_effect=launch):
    if fails:
     with self.assertRaises(RuntimeError) as error:r.refresh_codex_auth()
     self.assertNotIn('PRIVATE-TOKEN',str(error.exception))
    else:r.refresh_codex_auth()


class DependencyTests(unittest.TestCase):
 def test_exact_inputs_and_symlink_rejection(self):
  with tempfile.TemporaryDirectory() as tmp:
   work=Path(tmp);self.assertIsNone(r.go_dependency_key(work))
   (work/'go.mod').write_text('module fixture\n');(work/'go.sum').write_text('checksum\n')
   first=r.go_dependency_key(work)
   self.assertEqual(first,r.go_dependency_key(work))
   (work/'go.sum').write_text('different\n')
   self.assertNotEqual(first,r.go_dependency_key(work))
   (work/'go.sum').unlink();(work/'go.sum').symlink_to(work/'go.mod')
   with self.assertRaises(ValueError):r.go_dependency_key(work)
 def test_bundle_missing_does_not_grant_network_or_host_cache(self):
  with tempfile.TemporaryDirectory() as tmp, patch.object(r,'DEPENDENCIES',Path(tmp)/'dependencies'):
   work=Path(tmp)/'work';work.mkdir();(work/'go.mod').write_text('module fixture\n');(work/'go.sum').write_text('')
   self.assertIsNone(r.go_dependency_bundle(work))


class DependencyRecoveryTests(unittest.TestCase):
 def test_interrupted_format_is_not_published_and_next_attempt_recovers(self):
  with tempfile.TemporaryDirectory() as tmp:
   stage=Path(tmp)
   with patch.object(r,'run',side_effect=RuntimeError('interrupted mkfs')):
    with self.assertRaises(RuntimeError):r.dependency_volume(stage)
   self.assertFalse((stage/'work.ext4').exists())
   old=list(stage.glob('.initializing-*'));self.assertEqual(len(old),1)
   with patch.object(r,'run') as format_disk:
    image=r.dependency_volume(stage)
    self.assertNotEqual(format_disk.call_args.args[0][-1],str(old[0]))
   self.assertEqual(image.stat().st_size,2*1024**3)
   with patch.object(r,'run') as format_disk:
    self.assertEqual(r.dependency_volume(stage),image);format_disk.assert_not_called()

 def test_missing_sum_is_a_distinct_recoverable_input(self):
  with tempfile.TemporaryDirectory() as tmp:
   work=Path(tmp);self.assertIsNone(r.go_dependency_key(work))
   (work/'go.mod').write_text('module example.org/new\n')
   missing=r.go_dependency_key(work);self.assertEqual(len(missing),64)
   (work/'go.sum').write_bytes(b'<MISSING>')
   self.assertNotEqual(missing,r.go_dependency_key(work))
   (work/'go.sum').write_bytes(b'')
   self.assertNotEqual(missing,r.go_dependency_key(work))
 def test_existing_bundle_does_not_start_a_provisioner(self):
  import uuid
  with tempfile.TemporaryDirectory() as tmp:
   job=Path(tmp)/str(uuid.uuid4());job.mkdir();work=job/'work';work.mkdir()
   (work/'go.mod').write_text('module fixture\n')
   with patch.object(r,'job_path',return_value=job), patch.object(r,'ensure_work',return_value=work), patch.object(r,'status',return_value={'state':'failed'}), patch.object(r,'go_dependency_bundle',return_value=Path('/verified')), patch.object(r,'run') as launch:
    result=r.dependency_status(job.name)
    self.assertEqual(result['state'],'verified');launch.assert_not_called()
 def test_live_worker_never_has_inputs_provisioned_underneath_it(self):
  with patch.object(r,'job_path',return_value=Path('/unused')), patch.object(r,'status',return_value={'state':'running'}), patch.object(r,'ensure_work') as work:
   with self.assertRaises(ValueError):r.dependency_status('ignored')
   work.assert_not_called()


class ArtifactTests(unittest.TestCase):
 def setUp(self):
  import uuid
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
  self.root=Path(self.tmp.name); self.job=str(uuid.uuid4());self.p=self.root/self.job;self.p.mkdir();self.work=self.p/'work';self.work.mkdir()
  for name,value in [('ROOT',self.root),('ARTIFACT_LOCK',self.root/'global.lock')]:
   patcher=patch.object(r,name,value);patcher.start();self.addCleanup(patcher.stop)
  for name,value in [('status',{'state':'done','exit_code':0}),('ensure_work',self.work),('storage_status',{'ready':True,'free_bytes':100*1024**3,'allocated_bytes':0})]:
   patcher=patch.object(r,name,return_value=value);patcher.start();self.addCleanup(patcher.stop)
  patcher=patch.object(r.os,'chown');patcher.start();self.addCleanup(patcher.stop)
 def test_lossless_atomic_export_and_idempotence(self):
  import tarfile,hashlib
  data=b'original evidence\x00'*10000;(self.work/'evidence').write_bytes(data)
  (self.work/'link').symlink_to('/outside/never-follow')
  self.assertEqual(r.snapshot_execute(self.job),0)
  target=self.p/'artifact.tar.gz'
  original=target.read_bytes()
  with tarfile.open(target) as archive:
   self.assertEqual(archive.extractfile('work/evidence').read(),data)
   self.assertEqual(archive.getmember('work/link').linkname,'/outside/never-follow')
  import json
  receipt=json.loads((self.p/'artifact-state/receipt.json').read_text())
  self.assertEqual(receipt['sha256'],hashlib.sha256(original).hexdigest())
  self.assertFalse((self.p/'artifact-state/archive.tmp').exists())
  (self.work/'evidence').write_bytes(b'later host change')
  r.snapshot_execute(self.job)
  self.assertEqual(target.read_bytes(),original)
  self.assertEqual(r.snapshot(self.job),{'state':'ready'})
 def test_failure_retains_workspace_and_never_publishes_partial(self):
  import json
  (self.work/'evidence').write_text('keep me')
  with patch.object(r.tarfile,'open',side_effect=OSError('simulated disk failure')):
   with self.assertRaises(OSError):r.snapshot_execute(self.job)
  self.assertFalse((self.p/'artifact.tar.gz').exists())
  self.assertEqual((self.work/'evidence').read_text(),'keep me')
  receipt=json.loads((self.p/'artifact-state/receipt.json').read_text())
  self.assertEqual(receipt['state'],'waiting')
  self.assertGreater(receipt['retry_at'],r.time.time())
  r.snapshot_execute(self.job)
  self.assertTrue((self.p/'artifact.tar.gz').is_file())
 def test_restart_reconciles_unit_and_backs_off_interruption(self):
  from types import SimpleNamespace
  stage=r.snapshot_stage(self.job);r.dependency_receipt(stage,{'state':'exporting'})
  with patch.object(r.subprocess,'run',return_value=SimpleNamespace(stdout='active')):
   self.assertEqual(r.snapshot(self.job)['state'],'exporting')
  with patch.object(r.subprocess,'run',return_value=SimpleNamespace(stdout='inactive')),patch.object(r,'run') as start:
   self.assertEqual(r.snapshot(self.job)['state'],'waiting')
   start.assert_not_called()
  r.dependency_receipt(stage,{'state':'waiting','retry_at':0})
  with patch.object(r.subprocess,'run',return_value=SimpleNamespace(stdout='inactive')),patch.object(r,'storage_status',return_value={'ready':True,'free_bytes':100*1024**3,'allocated_bytes':0}),patch.object(r.shutil,'disk_usage',return_value=SimpleNamespace(free=100*1024**3)),patch.object(r,'allocated_storage',return_value=0),patch.object(r,'run') as start:
   self.assertEqual(r.snapshot(self.job)['state'],'exporting')
   cmd=start.call_args.args[0]
   self.assertIn('--property=KillMode=control-group',cmd)
   self.assertIn('--property=RuntimeMaxSec=600',cmd)
   self.assertEqual(cmd[-3:],['_snapshot','--job',self.job])

if __name__ == "__main__": unittest.main()
