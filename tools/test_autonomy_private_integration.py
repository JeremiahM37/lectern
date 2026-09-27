#!/usr/bin/python3
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import uuid

HERE=Path(__file__).parent

def load(name,path):
 spec=importlib.util.spec_from_file_location(name,path);module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module
R=load('private_runner',HERE/'autonomy-runner.py')
H=load('private_helper',HERE/'autonomy-private-integration.py')

class IntegrationTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name);self.jobs=self.root/'jobs';self.jobs.mkdir();self.identity='a'*64
  H.R=SimpleNamespace(**R.__dict__);H.R.ROOT=self.jobs;H.R.INTEGRATION_ROOT=self.root/'integrations';H.R.job_path=lambda job:self.jobs/str(uuid.UUID(job))
  def write(path,value):
   temporary=path.with_suffix('.tmp');temporary.write_text(json.dumps(value));temporary.chmod(0o600);os.replace(temporary,path)
  H.R.completion_write=write
  self.repo=self.root/'canonical';self.repo.mkdir()
  self.git('init','-q');self.git('config','user.email','fixture@localhost');self.git('config','user.name','Fixture')
  (self.repo/'main.py').write_text('old\n');(self.repo/'ignored.txt').write_text('raw committed content\n');(self.repo/'.gitattributes').write_text('ignored.txt export-ignore\n')
  self.git('add','.');self.git('commit','-qm','base');self.commit=self.git('rev-parse','HEAD');self.tree=self.git('rev-parse','HEAD^{tree}')
 def tearDown(self):
  for path in self.root.rglob('*'):
   if path.is_dir() and not path.is_symlink():path.chmod(0o700)
  self.tmp.cleanup()
 def git(self,*args):return subprocess.check_output(['git','-C',str(self.repo),*args],stderr=subprocess.DEVNULL).decode().strip()
 def value(self,phase='prepare'):
  return dict(integration_id=self.identity,pin_key='b'*64,root_id='c'*64,owner_job=str(uuid.uuid4()),owner_task=1,project_id=1,phase=phase,request_sha256='d'*64,generation=1)
 def test_raw_objects_ignore_export_attributes_and_dirty_work(self):
  stage=self.root/'stage';stage.mkdir();(self.repo/'main.py').write_text('dirty\n')
  view=H.canonical_object_view(str(self.repo),self.commit,stage);target=self.root/'export'
  tree,rows,digest=H.export_tree(view,self.commit,target)
  self.assertEqual(tree,self.tree);self.assertEqual((target/'main.py').read_text(),'old\n');self.assertTrue((target/'ignored.txt').exists())
  self.assertEqual((self.repo/'main.py').read_text(),'dirty\n');self.assertEqual(self.git('rev-parse','HEAD'),self.commit)
 def test_candidate_scope_and_restart_receipt(self):
  run=H.attempt(self.identity);base=run/'base';shutil.copytree(self.repo,base,ignore=shutil.ignore_patterns('.git'));rows,digest=H.inventory(base)
  meta=dict(tree_sha256=digest,rows=rows,base_kind='canonical',base_commit=self.commit,tree_oid=self.tree,scope=['main.py'])
  H.write(run/'base.json',meta);H.write(run/'prepare.json',{'request_sha256':'d'*64})
  archive=self.root/'builder';shutil.copytree(base,archive);(archive/'main.py').write_text('new\n')
  data=self.value('seal');data.update(prepare_request_sha256='d'*64,expected_base_tree_sha256=digest,builder_archive_sha256='e'*64)
  stage=run/'seal';stage.mkdir()
  def unpack(a,b,target):shutil.copytree(archive,target);return target
  with patch.object(H,'selected_archive',return_value=archive),patch.object(H,'unpack',side_effect=unpack):
   result=H.seal_operation(run,stage,data,dict(data));self.assertEqual(result['state'],'sealed');self.assertEqual(result['changed_paths'],1)
   (run/'sealed.json').unlink();shutil.rmtree(stage/'submitted',ignore_errors=True)
   result2=H.seal_operation(run,stage,data,dict(data));self.assertEqual(result2['candidate_commit'],result['candidate_commit']);self.assertTrue((run/'sealed.json').exists())
 def test_publication_consumer_cas_and_immutable_reconciliation(self):
  run=H.attempt(self.identity);base=run/'base';shutil.copytree(self.repo,base,ignore=shutil.ignore_patterns('.git'));rows,digest=H.inventory(base)
  H.write(run/'base.json',dict(tree_sha256=digest,rows=rows,base_kind='canonical',base_commit=self.commit,tree_oid=self.tree,scope=['main.py'],destination={'expected_managed_commit':''},source={'archive_sha256':'e'*64}))
  H.write(run/'prepare.json',{'request_sha256':'d'*64})
  archive=self.root/'builder';shutil.copytree(base,archive);(archive/'main.py').write_text('new\n')
  data=self.value('seal');data.update(prepare_request_sha256='d'*64,expected_base_tree_sha256=digest,builder_archive_sha256='e'*64)
  stage=run/'seal';stage.mkdir()
  def unpack(a,b,target):shutil.copytree(a,target);return target
  with patch.object(H,'selected_archive',return_value=archive),patch.object(H,'unpack',side_effect=unpack):sealed=H.seal_operation(run,stage,data,dict(data))
  review=self.root/'review';review.mkdir();(review/'autonomy-report.json').write_text('{"approved":true}')
  pub=self.value('publish');pub.update(candidate_receipt_sha256=sealed['receipt_sha256'],candidate_tree_sha256=sealed['candidate_tree_sha256'],candidate_commit=sealed['candidate_commit'],expected_managed_commit='',authorization_sha256='f'*64,checks=[])
  pub['review']={'job':pub['owner_job'],'archive_sha256':'e'*64,'report_sha256':R.digest_file(review/'autonomy-report.json')}
  pubstage=run/'publish';pubstage.mkdir()
  with patch.object(H,'selected_archive',return_value=review),patch.object(H,'unpack',side_effect=unpack),patch.object(H,'check_receipts',return_value=[{'check_id':'f'*64}]):
   H.write(run/'revoked.json',{'generation':1})
   waiting=H.publish_operation(run,pubstage,pub,dict(pub));self.assertEqual(waiting['state'],'waiting');shutil.rmtree(pubstage/'review');pub['generation']=2
   published=H.publish_operation(run,pubstage,pub,dict(pub));self.assertEqual(published['state'],'published_private');self.assertFalse(published['canonical_changed']);self.assertEqual(H.integration_tip(1)['commit'],sealed['candidate_commit'])
   # A later managed tip must never be reset by historical replay.
   repo=H.project(1)/'repo.git';later=H.make_commit(repo,sealed['candidate_tree_oid'],sealed['candidate_commit'],'later');H.git(repo,['update-ref','refs/heads/managed',later])
   shutil.rmtree(pubstage/'review');again=H.publish_operation(run,pubstage,pub,dict(pub));self.assertEqual(again['receipt_sha256'],published['receipt_sha256']);self.assertEqual(H.ref_read(repo,'refs/heads/managed'),later)
  fresh=self.root/'consumer';fresh.mkdir();consume=self.value('consume');consume.update(publication_receipt_sha256=published['receipt_sha256'],commit=published['commit'],tree_oid=published['tree_oid'],tree_sha256=published['tree_sha256']);outstage=run/'consume';outstage.mkdir()
  with patch.object(H,'mutable_work',return_value=fresh),patch.object(H,'admin_work'):
   copied=H.consume_operation(run,outstage,consume,dict(consume));self.assertEqual(copied['state'],'copied');self.assertEqual(H.inventory(fresh)[1],sealed['candidate_tree_sha256'])
  rollback=self.value('rollback');rollback.update(generation=2,operation_id='9'*64,publication_receipt_sha256=published['receipt_sha256'],target_publication_id=self.identity,target_commit=published['commit'],target_tree_sha256=published['tree_sha256'],expected_managed_commit=later,authorization_sha256='f'*64)
  rollbackstage=run/'rollback';rollbackstage.mkdir();rolled=H.rollback_operation(run,rollbackstage,rollback,dict(rollback));self.assertEqual(rolled['state'],'rolled_back');self.assertEqual(H.ref_read(repo,'refs/lectern/integrations/'+self.identity),published['commit'])
  H.git(repo,['update-ref','refs/heads/managed',later]);again=H.rollback_operation(run,rollbackstage,rollback,dict(rollback));self.assertEqual(H.ref_read(repo,'refs/heads/managed'),later)
  self.assertEqual(self.git('rev-parse','HEAD'),self.commit)
 def test_forbidden_links_hardlinks_and_scope(self):
  tree=self.root/'bad';tree.mkdir();(tree/'data').write_text('x');(tree/'escape').symlink_to('/etc/passwd')
  with self.assertRaises(ValueError):H.inventory(tree)
  (tree/'escape').unlink();os.link(tree/'data',tree/'hard')
  with self.assertRaises(ValueError):H.inventory(tree)
  self.assertTrue(H.allowed('src/a.py',['src/**']));self.assertFalse(H.allowed('src-other/a.py',['src/**']))
 def test_execution_generation_is_immutable_observation_is_not_hashed(self):
  path=self.root/'receipt.json';v=H.seal(path,dict(state='exited',generation=1,execution_generation=1,executed=True,charged_ms=10))
  v['generation']=2;H.write(path,v);self.assertEqual(H.receipt(path)['execution_generation'],1)
  v['execution_generation']=2;H.write(path,v)
  with self.assertRaises(ValueError):H.receipt(path)
 def test_pre_request_stop_revokes_generation(self):
  H.R.completion_service_active=lambda name:False
  result=H.stop(self.identity,3);self.assertEqual(result['state'],'stopped');self.assertEqual(H.revoked(H.attempt(self.identity)),3)
  missing=H.status(str(uuid.uuid4()),self.identity,'check','f'*64,3);self.assertEqual(missing['state'],'cancelled');self.assertEqual(missing['evidence_scope'],'revoked_missing_request');self.assertNotIn('execution_generation',missing);self.assertIs(missing['executed'],False)
  later=H.status(missing['owner_job'],self.identity,'check','f'*64,4);self.assertEqual(later['receipt_sha256'],missing['receipt_sha256']);self.assertEqual(later['generation'],4);self.assertEqual(later['revoked_through_generation'],3)
 def test_exact_tooling_metadata_preflight_and_no_substitution(self):
  key='8'*64
  with patch.object(R,'python_test_bundle',return_value=Path('/immutable/exact')) as bundle:
   value=R.python_test_runtime_status(key);self.assertEqual(value['state'],'verified');self.assertIn('metadata preflight',value['scope']);bundle.assert_called_once_with(key,full=False)
  with patch.object(R,'python_test_bundle',side_effect=FileNotFoundError('retained bundle missing')):
   self.assertEqual(R.python_test_runtime_status(key)['state'],'unavailable')
   with self.assertRaises(R.PythonUnsupported):R.python_test_runtime_mount(key)
   self.assertIn('unavailable',R.python_test_runtime_mount())
  with patch.object(R,'python_test_runtime_status',return_value={'state':'unavailable','key':key}),patch.object(R,'launch_lock') as lock:
   result=R.start(SimpleNamespace(python_test_key=key));self.assertEqual(result['state'],'unavailable');lock.assert_not_called()
  job=str(uuid.uuid4());(self.jobs/job).mkdir()
  with patch.object(R,'job_path',side_effect=H.R.job_path),patch.object(R,'completion_write',side_effect=H.R.completion_write),patch.object(R,'python_test_bundle',side_effect=ValueError('content checksum mismatch')):
   with self.assertRaises(R.PythonUnsupported):R.verify_expected_python_test(job,key)
   with patch.object(R,'completion_json',side_effect=H.record):
    failure=R.python_test_runtime_status(key,job);self.assertEqual(failure['state'],'unavailable');self.assertIs(failure['executed'],False);self.assertIn('full content',failure['scope'])
 def test_request_identity_cannot_change_project(self):
  data=self.value();data['schema_version']=1;job=self.jobs/data['owner_job'];directory=job/'integration-requests';directory.mkdir(parents=True)
  path=directory/(self.identity+'.prepare.json');path.write_text(json.dumps(data));path.chmod(0o600)
  H.request(data['owner_job'],self.identity,'prepare');data['project_id']=2;path.write_text(json.dumps(data))
  with self.assertRaises(ValueError):H.request(data['owner_job'],self.identity,'prepare')

if __name__=='__main__':unittest.main()
