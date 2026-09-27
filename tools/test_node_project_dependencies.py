import copy,hashlib,importlib.util,io,json,os,tarfile,tempfile,unittest
from pathlib import Path
from unittest.mock import patch

def load(name,file):
 s=importlib.util.spec_from_file_location(name,Path(__file__).with_name(file));m=importlib.util.module_from_spec(s);s.loader.exec_module(m);return m
H=load('node_project','node-project-dependencies.py')
N=load('node_runtime','autonomy-node-runtime.py')
class NodeProvisionerTests(unittest.TestCase):
 def test_mirror_normalization_exact(self):
  lock={'packages':{'node_modules/@x/a':{'version':'1.2.3','resolved':H.BROKER+'@x/a/-/a-1.2.3.tgz'}}}
  raw=H.normalize_lock(json.dumps(lock).encode())
  self.assertIn(b'https://registry.npmjs.org/@x/a/-/a-1.2.3.tgz',raw)
  for url in ('http://localhost:18080/npm/@x/a/-/a-1.2.3.tgz',H.BROKER+'@x/a/-/a-1.2.3.tgz?','https://evil/a.tgz'):
   lock['packages']['node_modules/@x/a']['resolved']=url
   with self.assertRaises(H.Unsupported):H.normalize_lock(json.dumps(lock).encode())
 def test_manifest_external_dependency_rejected(self):
  for value in ('file:../x','git+https://evil/x','https://evil/x','npm:other@1'):
   with self.assertRaises(H.Unsupported):H.manifest_safe(json.dumps({'dependencies':{'a':value}}).encode())
 def test_source_subtree_exact_and_root_hooks_retained(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d);archive=p/'source.tgz';out=p/'out';out.mkdir()
   with tarfile.open(archive,'w:gz') as t:
    for name,data in {'work/project/package.json':b'{"scripts":{"postinstall":"node src/build.js"}}','work/project/src/build.js':b'console.log("build")','work/other/private':b'elsewhere'}.items():
     m=tarfile.TarInfo(name);m.size=len(data);t.addfile(m,io.BytesIO(data))
   H.extract_project(archive,'project',out)
   self.assertTrue((out/'src/build.js').exists());self.assertFalse((out/'other').exists())
 def test_installed_symlink_and_mode_inventory(self):
  with tempfile.TemporaryDirectory() as d:
   p=Path(d);(p/'bin').mkdir();(p/'lib').mkdir();(p/'lib/cli.js').write_text('x');(p/'lib/cli.js').chmod(0o755);(p/'bin/cli').symlink_to('../lib/cli.js')
   rows=H.inventory(p);self.assertEqual(rows[0],{'path':'bin/cli','link':'../lib/cli.js'});self.assertEqual(rows[1]['mode'],0o555)
   (p/'bin/evil').symlink_to('/etc/passwd')
   with self.assertRaises(H.Unsupported):H.inventory(p)
 def test_offline_hook_and_probe_namespace_has_no_socket(self):
  class R:
   UID=GID=os.getuid()
   @staticmethod
   def python_failure_diagnostic(p):return ''
  with tempfile.TemporaryDirectory() as d:
   stage=Path(d);(stage/'heartbeat').touch();captured=[]
   class Proc:
    returncode=0
    def poll(self):return 0
   with patch.object(N.subprocess,'Popen',side_effect=lambda command,**kw:(captured.append(command) or Proc())):
    N.sandbox(R,'job',stage,'install',False);N.sandbox(R,'job',stage,'probe',False);N.sandbox(R,'job',stage,'resolve',True)
   for cmd in captured[:2]:
    self.assertNotIn('/dependency.sock',cmd);self.assertIn('--unshare-all',cmd);self.assertIn('/usr/local/bin',cmd)
   self.assertIn('/dependency.sock',captured[2])
 def test_cancel_tombstone_blocks_delayed_launch(self):
  with tempfile.TemporaryDirectory() as d:
   stage=Path(d);q={'source_job':'a','source_archive_sha256':'b','admission_sha256':'c'}
   class R:
    @staticmethod
    def status(j):return {'state':'done'}
    @staticmethod
    def completion_json(p):return json.loads(p.read_text())
   (stage/'cancel.json').write_text('{"revoked_through":2}')
   with patch.object(N,'request',return_value=(stage,q)),patch.object(N,'freeze',side_effect=AssertionError('no freeze/start after revocation')):
    self.assertEqual(N.launch(R,'j',2)['state'],'waiting')
 def test_full_validation_failure_is_bound_and_does_not_execute(self):
  with tempfile.TemporaryDirectory() as d:
   job=Path(d);stage=job/'node-prerequisite';stage.mkdir()
   old={'state':'verified','generation':3,'capability':'node_packages','source_job':'source','source_archive_sha256':'a'*64,'admission_sha256':'b'*64,'bundle_key':'c'*64}
   (stage/'receipt.json').write_text(json.dumps(old))
   class R:
    @staticmethod
    def completion_json(p):return json.loads(p.read_text())
    @staticmethod
    def completion_write(p,value):p.write_text(json.dumps(value))
    @staticmethod
    def job_path(j):return job
   with patch.object(N,'request',return_value=(stage,{})):
    receipt=N.validation_failure(R,'job',ValueError('checksum changed'))
   self.assertFalse(receipt['executed']);self.assertTrue(receipt['unsupported']);self.assertEqual(receipt['generation'],3)
   self.assertEqual(receipt['bundle_key'],old['bundle_key']);self.assertEqual(receipt['source_archive_sha256'],old['source_archive_sha256'])
   self.assertEqual(json.loads((job/'node-runtime.json').read_text()),receipt)
 def test_stop_old_generation_does_not_kill_new_owner(self):
  with tempfile.TemporaryDirectory() as d:
   job=Path(d);stage=job/'node-prerequisite';stage.mkdir();(stage/'receipt.json').write_text('{"generation":3,"state":"recovering"}')
   class R:
    @staticmethod
    def job_path(j):return job
    @staticmethod
    def completion_json(p):return json.loads(p.read_text())
    @staticmethod
    def completion_write(p,v):p.write_text(json.dumps(v))
    @staticmethod
    def run(*a,**kw):raise AssertionError('old stop killed newer execution')
   with patch.object(N,'safe_dir',side_effect=lambda p:p),patch.object(N,'active',return_value=True):
    receipt=N.stop(R,'job',2)
   self.assertEqual(receipt,{'state':'stopped','job':'job','generation':2})
   self.assertEqual(json.loads((stage/'cancel.json').read_text())['revoked_through'],2)
 def test_dependency_gzip_padding_limit(self):
  import gzip,base64
  data=gzip.compress(b'\x00'*4096);row={'integrity':'sha512-'+base64.b64encode(hashlib.sha512(data).digest()).decode()}
  with patch.object(H.V,'MAX_UNPACKED',1024),self.assertRaises(ValueError):H.V.validate_tarball(data,row)
if __name__=='__main__':unittest.main()
