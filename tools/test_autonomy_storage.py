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

if __name__=='__main__':unittest.main()
