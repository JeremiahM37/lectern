"""Startup races use real flock with isolated files and simulated systemd only."""
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import threading
from types import SimpleNamespace
import unittest
from unittest.mock import patch
import uuid

spec = importlib.util.spec_from_file_location('runner', Path(__file__).with_name('autonomy-runner.py'))
r = importlib.util.module_from_spec(spec); spec.loader.exec_module(r)

class LaunchTests(unittest.TestCase):
 def setUp(self):
  self.tmp=tempfile.TemporaryDirectory(); self.addCleanup(self.tmp.cleanup)
  self.root=Path(self.tmp.name); self.job=str(uuid.uuid4());self.p=self.root/self.job;self.p.mkdir()
  self.active=False;self.starts=0;self.stops=0
  self.args=SimpleNamespace(job=self.job,provider='selftest',model='',prompt=str(self.p/'prompt.txt'))
  for name,value in [('ROOT',self.root)]:
   m=patch.object(r,name,value);m.start();self.addCleanup(m.stop)
  m=patch.object(r.os,'chown');m.start();self.addCleanup(m.stop)
  m=patch.object(r.subprocess,'run',side_effect=self.systemd);m.start();self.addCleanup(m.stop)
 def systemd(self,args,**kw):
  if 'stop' in args:self.active=False;self.stops+=1
  state='active' if self.active else 'inactive'
  out=state+'\n' if '--value' in args else 'ActiveState='+state+'\nExecMainStatus=0\nResult=success\n'
  return SimpleNamespace(stdout=out,returncode=0)
 def fixture_start(self,args,*unused):
  self.starts+=1;self.active=True
  return {'state':'running','exit_code':None}
 def test_unused_and_active_unit_are_distinguished(self):
  self.assertEqual(r.launch_state(self.job),{'state':'unused'})
  self.active=True
  with patch.object(r,'start_locked') as start:
   self.assertEqual(r.start(self.args)['state'],'running');start.assert_not_called()
 def test_interrupted_before_and_after_job_receipt_never_reuses_uuid(self):
  for with_receipt in (False,True):
   with self.subTest(with_receipt=with_receipt):
    job=str(uuid.uuid4());p=self.root/job;p.mkdir();args=SimpleNamespace(job=job)
    def broken(args,*unused):
     (p/'assets').mkdir()
     if with_receipt:(p/'job.json').write_text(json.dumps({'provider':'selftest','model':''}))
     raise RuntimeError('interrupted launcher')
    with patch.object(r,'start_locked',side_effect=broken):
     with self.assertRaisesRegex(RuntimeError,'interrupted'):r.start(args)
    self.assertEqual(r.launch_state(job),{'state':'consumed','worker_state':'failed'})
    with patch.object(r,'start_locked') as start:
     with self.assertRaisesRegex(ValueError,'already been used'):r.start(args)
     start.assert_not_called()
    self.assertTrue((p/'start-intent.json').exists());self.assertTrue((p/'assets').exists())
 def test_legacy_partial_assets_and_malformed_receipts(self):
  (self.p/'assets').mkdir();self.assertEqual(r.launch_state(self.job)['state'],'consumed')
  (self.p/'job.json').write_text('{bad')
  with self.assertRaises(ValueError):r.launch_state(self.job)
  (self.p/'job.json').unlink();(self.p/'job.json').symlink_to('/etc/passwd')
  with self.assertRaises(ValueError):r.launch_state(self.job)
 def test_live_launcher_survives_observer_timeout_without_duplicate_start(self):
  entered=threading.Event();release=threading.Event();errors=[]
  def delayed(args,*unused):
   entered.set();self.assertTrue(release.wait(3));return self.fixture_start(args)
  def start():
   try:r.start(self.args)
   except Exception as e:errors.append(e)
  with patch.object(r,'start_locked',side_effect=delayed):
   first=threading.Thread(target=start);first.start();self.assertTrue(entered.wait(3))
   self.assertEqual(r.launch_state(self.job),{'state':'launching'})
   self.assertEqual(r.status(self.job)['state'],'running')
   second=threading.Thread(target=start);second.start()
   release.set();first.join(3);second.join(3)
  self.assertFalse(first.is_alive());self.assertFalse(second.is_alive());self.assertEqual(errors,[])
  self.assertEqual(self.starts,1);self.assertEqual(r.launch_state(self.job)['state'],'running')
 def test_stop_waits_for_launcher_and_prevents_delayed_start(self):
  entered=threading.Event();release=threading.Event();stopped=threading.Event();errors=[]
  def delayed(args,*unused):
   entered.set();self.assertTrue(release.wait(3));return self.fixture_start(args)
  def launch():
   try:r.start(self.args)
   except Exception as e:errors.append(e)
  def stop():
   try:r.stop(self.job)
   except Exception as e:errors.append(e)
   finally:stopped.set()
  with patch.object(r,'start_locked',side_effect=delayed):
   first=threading.Thread(target=launch);first.start();self.assertTrue(entered.wait(3))
   stopper=threading.Thread(target=stop);stopper.start();self.assertFalse(stopped.wait(.05))
   release.set();first.join(3);stopper.join(3)
  self.assertEqual(errors,[]);self.assertTrue(stopped.is_set());self.assertFalse(self.active);self.assertEqual(self.stops,1)
  with patch.object(r,'start_locked') as start:
   with self.assertRaisesRegex(ValueError,'already been used'):r.start(self.args)
   start.assert_not_called()
 def test_stop_before_start_and_unit_query_errors_fail_closed(self):
  r.stop(self.job)
  with patch.object(r,'start_locked') as start:
   with self.assertRaises(ValueError):r.start(self.args)
   start.assert_not_called()
  (self.p/'stopped').unlink()
  with patch.object(r.subprocess,'run',return_value=SimpleNamespace(stdout='',returncode=1)):
   with self.assertRaisesRegex(RuntimeError,'status unavailable'):r.launch_state(self.job)

 def test_stop_failure_and_early_result_never_hide_a_live_unit(self):
  self.active=True
  real=r.run
  def fail_stop(args,**kw):
   if 'stop' in args:raise RuntimeError('stop failed')
   return real(args,**kw)
  with patch.object(r,'run',side_effect=fail_stop):
   with self.assertRaisesRegex(RuntimeError,'stop failed'):r.stop(self.job)
  self.assertTrue((self.p/'stopped').exists())
  self.assertEqual(r.status(self.job)['state'],'running')
  (self.p/'result.json').write_text('{"state":"done","exit_code":0}')
  self.assertEqual(r.status(self.job)['state'],'running')
  self.assertEqual(r.launch_state(self.job)['state'],'running')
  with self.assertRaisesRegex(ValueError,'requires a stopped worker'):r.snapshot(self.job)
  with patch.object(r,'start_locked') as start:
   self.assertEqual(r.start(self.args)['state'],'running');start.assert_not_called()
  r.stop(self.job);self.assertFalse(self.active)
  self.assertEqual(r.launch_state(self.job)['state'],'consumed')

 def test_launch_client_holds_lock_after_launcher_process_dies(self):
  import subprocess,sys,time
  ready=self.root/'client-ready';release=self.root/'release'
  child_code="import pathlib,time,sys; pathlib.Path(sys.argv[1]).touch(); deadline=time.monotonic()+8;\nwhile not pathlib.Path(sys.argv[2]).exists() and time.monotonic()<deadline: time.sleep(.01)"
  parent_code="""
import importlib.util,sys,os,subprocess,time
from pathlib import Path
from types import SimpleNamespace
spec=importlib.util.spec_from_file_location('runner',sys.argv[1]);r=importlib.util.module_from_spec(spec);spec.loader.exec_module(r)
r.ROOT=Path(sys.argv[2]);job=sys.argv[3];p=r.ROOT/job
(p/'work').mkdir();(p/'prompt.txt').write_text('isolated test')
r.os.chown=lambda *a,**k:None
r.ensure_work=lambda p:p/'work'
r.properties=lambda:[]
r.subprocess.run=lambda *a,**k:SimpleNamespace(returncode=0,stdout='ActiveState=inactive\\nExecMainStatus=0\\nResult=success\\n')
def delayed_client(args,**kw):
 assert args[0]=='/usr/bin/systemd-run'
 assert len(kw['pass_fds'])==1
 child=subprocess.Popen([sys.executable,'-c',sys.argv[6],sys.argv[4],sys.argv[5]],pass_fds=kw['pass_fds'],stdin=subprocess.DEVNULL,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
 child.wait(timeout=10)
r.run=delayed_client
r.start(SimpleNamespace(job=job,provider='selftest',model='',prompt=str(p/'prompt.txt'),hold_seconds=0,network_selftest=False))
"""
  proc=subprocess.Popen([sys.executable,'-c',parent_code,str(Path(r.__file__)),str(self.root),self.job,str(ready),str(release),child_code],stdout=subprocess.DEVNULL,stderr=subprocess.PIPE)
  try:
   deadline=time.monotonic()+4
   while not ready.exists() and proc.poll() is None and time.monotonic()<deadline:time.sleep(.01)
   self.assertTrue(ready.exists(),proc.stderr.read().decode() if proc.poll() is not None else 'launch client not ready')
   proc.kill();proc.wait(timeout=3)
   self.assertEqual(r.launch_state(self.job),{'state':'launching'})
   self.assertEqual(r.status(self.job)['state'],'running')
   release.touch();deadline=time.monotonic()+4
   while r.launch_state(self.job)['state']=='launching' and time.monotonic()<deadline:time.sleep(.01)
   self.assertEqual(r.launch_state(self.job),{'state':'consumed','worker_state':'failed'})
  finally:
   release.touch()
   if proc.poll() is None:proc.kill();proc.wait(timeout=3)
   proc.stderr.close()

if __name__=='__main__':unittest.main()
