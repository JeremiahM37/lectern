"""Immutable browser inventory and genuine offline browser/pytest namespace tests."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


def load(name,file):
 spec=importlib.util.spec_from_file_location(name,Path(__file__).with_name(file));module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module);return module
H=load('browser_runtime','provision-browser-runtime.py')
R=load('browser_runner','autonomy-runner.py')

class BrowserTests(unittest.TestCase):
 def setUp(self):
  self.temp=tempfile.TemporaryDirectory(dir='/mnt/bulk');self.addCleanup(self.cleanup)
  self.root=Path(self.temp.name);self.cache=self.root/'cache';self.cache.mkdir();self.dest=self.root/'bundles'
  self.driver=self.root/'browsers.json';self.driver.write_text(json.dumps({'browsers':[{'name':name,'revision':'1','browserVersion':'123'} for name in ('chromium','chromium-headless-shell','ffmpeg')]}))
  for name in ('chromium','chromium_headless_shell','ffmpeg'):
   directory=self.cache/(name+'-1');directory.mkdir();(directory/'binary').write_bytes(b'fixture executable');(directory/'binary').chmod(0o755)
 def cleanup(self):
  for p in Path(self.temp.name).rglob('*'):
   if p.is_dir() and not p.is_symlink():p.chmod(0o755)
  self.temp.cleanup()
 def build(self):return H.provision(self.cache,self.driver,'1.62.0',self.dest,stage=True)
 def test_hash_addressed_modes_and_honest_provenance(self):
  path,data=self.build();self.assertEqual(H.verify(path,owner=os.geteuid()),data)
  self.assertEqual(data['provenance'],'local-installed-cache-inventory');self.assertNotIn('upstream_sha256',data)
  self.assertEqual((path/'browsers/chromium-1/binary').stat().st_mode&0o777,0o555)
  again,receipt=self.build();self.assertEqual(again,path);self.assertEqual(receipt,data)
 def test_links_and_hardlinks_never_copied(self):
  source=self.cache/'chromium-1/binary';link=self.cache/'chromium-1/escape';link.symlink_to('/etc/passwd')
  with self.assertRaises(ValueError):self.build()
  link.unlink();os.link(source,link)
  with self.assertRaises(ValueError):self.build()
 def test_content_and_executable_mode_tampering_refused(self):
  path,data=self.build();binary=path/'browsers/chromium-1/binary';binary.chmod(0o444)
  with self.assertRaisesRegex(ValueError,'changed'):H.verify(path,owner=os.geteuid())
  binary.chmod(0o755);binary.write_bytes(b'changed');binary.chmod(0o555)
  with self.assertRaisesRegex(ValueError,'changed'):H.verify(path,owner=os.geteuid())
 def test_actual_sandbox_file_limit_accepts_large_installed_member(self):
  (self.root/'heartbeat').touch()
  output=self.root/'large-member';limit=self.root/'observed-limit.json'
  code="""import errno,json,resource
from pathlib import Path
root=Path(ROOT)
with (root/'large-member').open('wb') as out:
 for _ in range(65):out.write(b'\\0'*(1024**2))
soft,hard=resource.getrlimit(resource.RLIMIT_FSIZE)
assert soft==hard==256*1024**2
try:
 with (root/'too-large').open('wb') as out:out.truncate(soft+1)
except OSError as error:assert error.errno==errno.EFBIG
else:raise AssertionError('file bound absent')
(root/'observed-limit.json').write_text(json.dumps([soft,hard]))
""".replace('ROOT',repr(str(self.root)))
  with patch.object(R,'python_sandbox_command',return_value=[sys.executable,'-I','-S','-c',code]),patch.object(R,'UID',os.getuid()),patch.object(R,'GID',os.getgid()),patch.object(R.os,'setgroups'):
   R.python_sandbox_run('fixture',self.root,'install')
  self.assertEqual(output.stat().st_size,65*1024**2)
  self.assertEqual(json.loads(limit.read_text()),[R.PYTHON_SANDBOX_FILE_LIMIT]*2)
  self.assertGreaterEqual(R.PYTHON_PROVISIONER_FILE_LIMIT,2*1024**3)

 def test_report_symlink_and_oversized_proof_refused(self):
  proof=self.root/'proof';proof.symlink_to('/etc/passwd')
  with self.assertRaises(R.PythonUnsupported):R.browser_probe_receipt(proof,'a'*64)
  proof.unlink();proof.write_bytes(b' '*32769)
  with self.assertRaises(R.PythonUnsupported):R.browser_probe_receipt(proof,'a'*64)
 def test_unsupported_or_escaped_revisions_refused(self):
  self.driver.write_text('{"browsers":[]}')
  with self.assertRaises(ValueError):self.build()

 def test_missing_registered_payload_and_platform_are_typed_unsupported(self):
  bundle,data=self.build();selector=self.dest/'active.json'
  selector.write_text(json.dumps({'schema_version':1,'playwright':{'1.62.0':'a'*64}}));selector.chmod(0o444)
  real=Path.lstat
  def root_stat(path,*args,**kwargs):
   values=list(real(path,*args,**kwargs));values[4]=0;return os.stat_result(values)
  browser_root=self.root/'dependencies';browser_root.mkdir();self.dest.rename(browser_root/'browser');self.dest=browser_root/'browser'
  with patch.object(R,'DEPENDENCIES',browser_root),patch.object(R,'python_helper',return_value=H),patch.object(Path,'lstat',root_stat):
   with self.assertRaises(R.PythonUnsupported):R.browser_select('1.62.0')
   with patch.object(H.platform,'machine',return_value='incompatible-machine'):
    with self.assertRaisesRegex(R.PythonUnsupported,'platform mismatch'):R.browser_runtime(data['key'],'1.62.0',full=False)
   with patch.object(H,'verify',side_effect=ValueError('browser payload changed')):
    with self.assertRaisesRegex(ValueError,'payload changed') as caught:R.browser_runtime(data['key'],'1.62.0')
    self.assertNotIsInstance(caught.exception,R.PythonUnsupported)

 def test_cached_interpreter_mismatch_persists_diagnosable_receipt(self):
  stage=self.root/'stage';stage.mkdir();deps=self.root/'dependencies';deps.mkdir();cache=deps/'python-project';cache.mkdir()
  request={'source_job':'fixture','source_archive_sha256':'a'*64,'admission_sha256':'b'*64}
  identity={'input_key':'c'*64,'runtime_digest':'d'*64,'runtime':{'policy':'pypi-pure-wheel-v1','python_sha256':'0'*64}}
  manifest={'input_key':identity['input_key'],'runtime_digest':identity['runtime_digest'],'checksum_verified':True,'files':[]}
  key=hashlib.sha256(H.canonical(manifest)).hexdigest();manifest['key']=key
  bundle=cache/key;bundle.mkdir();(bundle/'site-packages').mkdir();(bundle/'manifest.json').write_bytes(H.canonical(manifest));(bundle/'manifest.json').chmod(0o444)
  receipt=dict(R.python_binding(request,identity),state='verified',bundle_key=key)
  (stage/'identity.json').write_bytes(H.canonical(identity));(stage/'receipt.json').write_bytes(H.canonical(receipt))
  job=self.root/'job';job.mkdir();(job/'python-requirement.json').touch()
  real=Path.lstat
  def root_stat(path,*args,**kwargs):
   values=list(real(path,*args,**kwargs));values[4]=0;return os.stat_result(values)
  with patch.object(R,'job_path',return_value=job),patch.object(R,'python_request',return_value=(stage,request)),patch.object(R,'DEPENDENCIES',deps),patch.object(Path,'lstat',root_stat),patch.object(R.os,'geteuid',return_value=0),patch.object(R.os,'chown'):
   with self.assertRaisesRegex(R.PythonUnsupported,'interpreter changed'):R.python_project_bundle('fixture')
  changed=json.loads((stage/'receipt.json').read_text());self.assertEqual(changed['state'],'unavailable');self.assertTrue(changed['unsupported'])
  self.assertEqual(changed['bundle_key'],key)

 @unittest.skipUnless(os.environ.get('LECTERN_BROWSER_REAL')=='1','explicit existing-browser sandbox proof; copies 651 MiB into disposable bulk fixture')
 def test_real_browser_snapshot_nested_offline_pytest(self):
  cache=Path('/home/admin/.cache/ms-playwright');site=Path('/home/admin/.venvs/verify/lib/python3.13/site-packages')
  bundle,data=H.provision(cache,site/'playwright/driver/package/browsers.json','1.62.0',self.dest,stage=True)
  H.verify(bundle,owner=os.geteuid())
  fetch=self.root/'fetch';fetch.mkdir();(fetch/'site-packages').mkdir()
  proof=self.root/'proof';proof.mkdir()
  mapped={'/tmp/python-fetch':str(fetch),'/tmp/python-proof':str(proof),'/tmp/python-browser':str(bundle),'/tmp/python-tooling':str(site),'/tmp/python-helper':str(Path(__file__).with_name('python-project-dependencies.py').resolve())}
  probe=[mapped.get(arg,arg) for arg in R.python_sandbox_command('browser-probe',probe=True)]
  separator=probe.index('--');probe[separator:separator]=['--ro-bind',str(site),'/fetch/site-packages']
  result=subprocess.run(['/usr/bin/timeout','40',*probe],text=True,capture_output=True)
  self.assertEqual(result.returncode,0,result.stdout+result.stderr)
  self.assertEqual(R.browser_probe_receipt(proof/'browser.json',data['key'])['browser_version'],data['browser_version'])
  work=self.root/'work';work.mkdir();(work/'test_browser.py').write_text('''import http.server,threading,socket,os,subprocess,sys
from pathlib import Path
from playwright.sync_api import sync_playwright
def test_browser():
 class H(http.server.BaseHTTPRequestHandler):
  def do_GET(self):
   self.send_response(200);self.end_headers();self.wfile.write(b'<iframe src="/frame"></iframe>' if self.path=='/' else b'<button id="ok">offline</button>')
  def log_message(self,*args):pass
 server=http.server.HTTPServer(('127.0.0.1',0),H);threading.Thread(target=server.serve_forever,daemon=True).start()
 with sync_playwright() as p:
  browser=p.chromium.launch();context=browser.new_context();context.add_init_script('window.works=42')
  page=context.new_page();page.goto('http://127.0.0.1:'+str(server.server_port))
  assert page.frames[1].evaluate('window.works')==42
  assert page.frames[1].locator('#ok').inner_text()=='offline'
  page.add_style_tag(content='html {background:rgb(12,34,56)} body {margin:0} iframe {margin-left:10px}')
  page.screenshot(path='/work/fixture.png');browser.close()
 from PIL import Image
 assert Image.open('/work/fixture.png').getpixel((0,0))[:3]==(12,34,56)
 assert not Path('/home/admin').exists() and not Path('/network.sock').exists() and not Path('/bridge.sock').exists()
 try:socket.create_connection(('1.1.1.1',443),timeout=.2)
 except OSError:pass
 else:raise AssertionError('public network available')
 subprocess.run([sys.executable,'-I','-c','from playwright.sync_api import sync_playwright; p=sync_playwright().start(); b=p.chromium.launch(); b.close(); p.stop()'],env={'HOME':'/home/agent','PATH':'/usr/bin:/bin'},check=True)
''')
  overlay=self.root/'site';overlay.mkdir()
  (overlay/'runtime.pth').write_text('/usr/lib/python3/dist-packages\n'+"import pathlib; pathlib.Path('/work/outer-hook' if pathlib.Path('/network.sock').exists() else '/work/inner-hook').write_text('executed')\n")
  fake_bridge=self.root/'network.sock';fake_bridge.write_text('fixture bridge sentinel, no actual socket')
  cmd=['/usr/bin/timeout','60','/usr/bin/bwrap','--die-with-parent','--new-session','--unshare-all','--cap-drop','ALL','--clearenv','--tmpfs','/','--ro-bind','/usr','/usr']
  for p in ('/lib','/lib64','/bin'):
   if Path(p).exists():cmd+=['--ro-bind',p,p]
  cmd+=['--proc','/proc','--dev','/dev','--tmpfs','/tmp','--dir','/home/agent','--ro-bind',str(site),'/usr/lib/python3/dist-packages','--ro-bind',str(overlay),'/usr/local/lib/python3.13/dist-packages','--ro-bind',str(fake_bridge),'/network.sock','--ro-bind',str(bundle),'/opt/browser-runtime','--bind',str(work),'/work','--chdir','/work','--setenv','HOME','/home/agent','--setenv','PATH','/usr/bin:/bin','--','/opt/browser-runtime/browsers/offline-test','/usr/bin/python3','-m','pytest','-q','-p','no:cacheprovider','/work/test_browser.py']
  result=subprocess.run(cmd,text=True,capture_output=True);self.assertEqual(result.returncode,0,result.stdout+result.stderr)
  self.assertTrue((work/'fixture.png').exists())
  self.assertTrue((work/'inner-hook').exists());self.assertFalse((work/'outer-hook').exists(),'startup hooks ran before bridge isolation')
  (work/'test_browser.py').write_text('def test_fail(): assert False\n')
  failed=subprocess.run(cmd,text=True,capture_output=True);self.assertEqual(failed.returncode,1,failed.stdout+failed.stderr)
  print('Actual immutable browser + nested offline pytest PASS; iframe/init-script/screenshot/minimal-env child PASS; deliberate failure detected')

if __name__=='__main__':unittest.main()
