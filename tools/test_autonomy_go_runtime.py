"""Immutable Go test runtime snapshots and exact-selection regressions; no network."""
import importlib.util,json,os,tempfile,unittest
from pathlib import Path
from unittest.mock import patch
spec=importlib.util.spec_from_file_location('go_runner',Path(__file__).with_name('autonomy-runner.py'));r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
class GoRuntimeTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup);self.root=Path(self.tmp.name)
  self.work=self.root/'work';self.work.mkdir();(self.work/'go.mod').write_text('module example.test/fixture\n\ngo 1.23\n')
  self.key=r.go_dependency_key(self.work);self.bundle=self.root/'bundle';self.bundle.mkdir();(self.bundle/'mod').mkdir();(self.bundle/'mod/pkg.txt').write_text('verified module bytes')
  (self.bundle/'manifest.json').write_text(json.dumps({'key':self.key,'checksum_verified':True}))
  self.tool=self.root/'tool';self.tool.mkdir();(self.tool/'bin').mkdir();(self.tool/'bin/go').write_text('inert fixture, never execute');(self.tool/'bin/go').chmod(0o755)
  for name,value in [('DEPENDENCIES',self.root/'dependencies'),('GO_TOOLCHAIN_SOURCE',self.tool)]:
   m=patch.object(r,name,value);m.start();self.addCleanup(m.stop)
  m=patch.object(r.os,'chown');m.start();self.addCleanup(m.stop)
  m=patch.object(r,'go_dependency_bundle',return_value=self.bundle);m.start();self.addCleanup(m.stop)
 def capture(self):return r.go_runtime_capture(self.work)
 def test_first_snapshot_replay_ignores_new_installed_toolchain(self):
  first=self.capture();(self.tool/'bin/go').write_text('new toolchain')
  self.assertEqual(first,self.capture());self.assertEqual(first,r.go_runtime_capture(self.work,first))
  _,tool=r.go_runtime_lookup(first);self.assertEqual((tool/'bin/go').read_text(),'inert fixture, never execute')
 def test_exact_content_address_survives_missing_mutable_index(self):
  first=self.capture();(r.DEPENDENCIES/'go-test/selections'/ (self.key+'.json')).unlink()
  self.assertEqual(first,r.go_runtime_capture(self.work,first))
 def test_generated_source_sums_do_not_substitute_selected_runtime(self):
  first=self.capture();(self.work/'go.sum').write_text('new sums')
  self.assertNotEqual(r.go_dependency_key(self.work),first['go_dependency_key'])
  self.assertEqual(r.go_runtime_capture(self.work,first),first)
 def test_tampered_snapshot_bytes_and_mode_refused(self):
  for mutation in ['bytes','mode']:
   with self.subTest(mutation=mutation):
    first=self.capture();modules,_=r.go_runtime_lookup(first);file=modules/'mod/pkg.txt'
    file.chmod(0o644)
    if mutation=='bytes':file.write_text('modified');file.chmod(0o444)
    with self.assertRaises(ValueError):r.go_runtime_lookup(first)
    file.chmod(0o644);file.write_text('verified module bytes');file.chmod(0o444)
 def test_symlinks_and_source_mutation_refused(self):
  (self.tool/'escape').symlink_to('/etc/passwd')
  with self.assertRaises(ValueError):self.capture()
 def test_missing_cache_does_not_fetch(self):
  with patch.object(r,'go_dependency_bundle',return_value=None),patch.object(r,'run') as run:
   with self.assertRaises(FileNotFoundError):self.capture()
   run.assert_not_called()
 def test_historical_generated_sums_need_original_module_hash(self):
  (self.work/'go.sum').write_text('generated sums')
  with self.assertRaisesRegex(ValueError,'module declaration changed'):r.go_probe_source_key(self.work,self.key)
  metadata=json.loads((self.bundle/'manifest.json').read_text());metadata['go_mod_sha256']=r.digest_file(self.work/'go.mod');(self.bundle/'manifest.json').write_text(json.dumps(metadata))
  self.assertEqual(r.go_probe_source_key(self.work,self.key),r.go_dependency_key(self.work))
  (self.work/'go.mod').write_text('different module')
  with self.assertRaisesRegex(ValueError,'module declaration changed'):r.go_probe_source_key(self.work,self.key)
 def test_revoked_generation_cannot_launch_and_new_generation_is_owned(self):
  from types import SimpleNamespace
  import uuid
  job=str(uuid.uuid4());jobs=self.root/'jobs';(jobs/job).mkdir(parents=True)
  with patch.object(r,'ROOT',jobs),patch.object(r,'completion_service_active',return_value=False),patch.object(r,'run',return_value=SimpleNamespace(stdout='')) as run:
   self.assertEqual(r.go_probe_runtime(job,self.key,'e'*64,1,stop=True)['state'],'stopped')
   self.assertEqual(r.go_probe_runtime(job,self.key,'e'*64,1)['state'],'cancelled');run.assert_not_called()
   self.assertEqual(r.go_probe_runtime(job,self.key,'e'*64,2)['state'],'recovering');self.assertEqual(run.call_count,1)
   with self.assertRaisesRegex(ValueError,'source changed'):r.go_probe_runtime(job,self.key,'f'*64,2)
 def test_busy_launch_guard_returns_pending_without_waiting(self):
  import fcntl,uuid
  job=str(uuid.uuid4());jobs=self.root/'jobs';(jobs/job).mkdir(parents=True)
  with patch.object(r,'ROOT',jobs):
   stage,_=r.go_probe_paths(job,self.key,'e'*64)
   with (stage/'guard').open('a') as guard:
    fcntl.flock(guard,fcntl.LOCK_EX)
    self.assertEqual(r.go_probe_runtime(job,self.key,'e'*64,1)['state'],'recovering')
    self.assertEqual(r.go_probe_runtime(job,self.key,'e'*64,1,stop=True)['state'],'stopping')
 def test_failed_expected_runtime_revalidates_exact_bytes_without_recapture(self):
  import uuid
  first=self.capture();job=str(uuid.uuid4());jobs=self.root/'jobs';(jobs/job).mkdir(parents=True)
  with patch.object(r,'ROOT',jobs),patch.object(r,'go_runtime_requirement',return_value=first):
   stage,intent=r.go_probe_paths(job,self.key,'e'*64)
   self.assertEqual(intent['expected_runtime'],first)
   modules,_=r.go_runtime_lookup(first);member=modules/'mod/pkg.txt';member.chmod(0o644);member.write_text('corrupt');member.chmod(0o444)
   with self.assertRaises(ValueError):r.go_runtime_capture(self.work,expected=intent['expected_runtime'])
   (self.tool/'bin/go').write_text('new unrelated compiler')
   member.chmod(0o644);member.write_text('verified module bytes');member.chmod(0o444)
   self.assertEqual(r.go_runtime_capture(self.work,expected=intent['expected_runtime']),first)
   altered=dict(first,go_toolchain_digest='f'*64)
   with patch.object(r,'go_runtime_requirement',return_value=altered),self.assertRaisesRegex(ValueError,'source changed'):
    r.go_probe_paths(job,self.key,'e'*64)
 def test_mixed_go_node_path_preserves_selected_toolchain(self):
  spec=importlib.util.spec_from_file_location('go_expert',Path(__file__).with_name('autonomy-expert-probe.py'));e=importlib.util.module_from_spec(spec);spec.loader.exec_module(e);e.R=r
  runtime={'python_bundle_key':'','python_input_key':'','browser_key':'','python_test_key':'',**dict(zip(r.GO_RUNTIME_FIELDS,['a'*64,'b'*64,'c'*64]))}
  cmd=e.sandbox_command({'runtime':runtime,'argv':[]});self.assertIn('/usr/local/go',cmd);self.assertEqual(cmd[cmd.index('/usr/local/go')-1],'/tmp/expert-go-toolchain');self.assertIn('/opt/go-toolchain/bin:/usr/bin:/bin',cmd);self.assertIn('off',cmd)
  runtime.update(dict.fromkeys(e.NODE_RUNTIME_FIELDS,'d'*64));cmd=e.sandbox_command({'runtime':runtime,'argv':[]});self.assertIn('/opt/node-project/node_modules/.bin:/opt/node/bin:/opt/go-toolchain/bin:/usr/bin:/bin',cmd)
if __name__=='__main__':unittest.main()
