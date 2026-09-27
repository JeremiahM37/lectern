"""Real local flock/process tests with simulated guest transport; no GPU access."""
import contextlib
import importlib.util
import json
import multiprocessing
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time
import unittest
import uuid

spec=importlib.util.spec_from_file_location('gpu_lease',Path(__file__).with_name('autonomy-gpu-lease.py'))
G=importlib.util.module_from_spec(spec);spec.loader.exec_module(G)

class SimulatedGuestBackend:
    def __init__(self): self.process=None;self.starts=0;self.identity=str(uuid.uuid4());self.uncertain=False
    def boot_id(self):return self.identity
    def launch(self,request,guard):
        self.starts+=1
        self.process=subprocess.Popen(['/usr/bin/python3','-I','-c','import time; time.sleep(30)'],start_new_session=True,pass_fds=(guard,),stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        # A real asynchronous launcher must retain FD until launch settles, not
        # pass it indefinitely to workload. This simulated direct child closes it.
        # Test uses a separate direct backend below for ordinary completed launch.
    def stop(self,request):
        if self.process and self.process.poll() is None:
            os.killpg(self.process.pid,signal.SIGKILL);self.process.wait(timeout=3)
    def observe(self,request):
        empty=self.process is None or self.process.poll() is not None
        return {'boot_id':self.identity,'inactive':empty,'cgroup_empty':empty and not self.uncertain}

class DirectBackend(SimulatedGuestBackend):
    def launch(self,request,guard):
        self.starts+=1
        self.process=subprocess.Popen(['/usr/bin/python3','-I','-c','import time; time.sleep(30)'],start_new_session=True,stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)

class LeaseTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.root=Path(self.tmp.name)
        self.lock=self.root/'physical.lock';self.lock.write_bytes(b'original inode')
        self.host=G.HostFence(G.Store(self.root/'host',os.getuid()),self.lock)
        self.backend=DirectBackend();self.guest=G.GuestFence(G.Store(self.root/'guest',os.getuid()),self.backend)
        self.request={'schema_version':1,'target':G.TARGET,'run_id':'a'*64,'request_sha256':'b'*64,'generation':1,'guest_boot_id':self.backend.boot_id()}
    def tearDown(self):self.backend.stop(self.request);self.tmp.cleanup()
    def test_actual_flock_exclusion_and_inode_preservation(self):
        inode=self.lock.stat().st_ino
        with self.host.acquire(self.request):
            result=subprocess.run(['flock','-n','-E','75',str(self.lock),'true'])
            self.assertEqual(result.returncode,75)
            with self.assertRaises(G.Busy):self.host.check()
        self.assertEqual(self.lock.stat().st_ino,inode);self.assertEqual(self.lock.read_bytes(),b'original inode')
        with self.assertRaises(G.Busy):self.host.check()
        self.assertEqual(self.host.reconcile(self.guest)['state'],'stopped')
        self.assertEqual(self.host.check()['state'],'clear')
        with self.assertRaises(G.Revoked):
            with self.host.acquire(self.request):pass
    def test_supervisor_sigkill_releases_flock_but_not_fence(self):
        ready_r,ready_w=os.pipe()
        pid=os.fork()
        if pid==0:
            os.close(ready_r)
            with self.host.acquire(self.request):
                os.write(ready_w,b'1');time.sleep(30)
            os._exit(0)
        os.close(ready_w)
        try:
            self.assertEqual(os.read(ready_r,1),b'1')
            self.guest.start(self.request)
            os.kill(pid,signal.SIGKILL);os.waitpid(pid,0);pid=None
            self.assertEqual(subprocess.run(['flock','-n',str(self.lock),'true']).returncode,0)
            self.assertIsNone(self.backend.process.poll())
            with self.assertRaises(G.Busy):self.host.check()
            other=dict(self.request,run_id='c'*64)
            with self.assertRaises(G.Busy):
                with self.host.acquire(other):pass
            self.assertEqual(self.host.reconcile(self.guest)['state'],'stopped')
            self.assertIsNotNone(self.backend.process.poll())
            with self.host.acquire(other):pass
        finally:
            os.close(ready_r)
            if pid is not None:os.kill(pid,signal.SIGKILL);os.waitpid(pid,0)
    def test_cancel_before_dispatch_and_generation_replay(self):
        self.assertEqual(self.guest.stop_and_observe(self.request)['state'],'stopped')
        with self.assertRaises(G.Revoked):self.guest.start(self.request)
        with self.assertRaises(ValueError):self.guest.start(dict(self.request,generation=2))
        self.assertEqual(self.backend.starts,0)
    def test_host_cancel_before_reservation_and_failed_transport(self):
        class Unreachable:
            def stop_and_observe(inner,r):raise RuntimeError('offline')
        with self.assertRaises(RuntimeError):self.host.cancel(self.request,Unreachable())
        with self.assertRaises(G.Revoked):
            with self.host.acquire(self.request):pass
        self.assertEqual(self.host.cancel(self.request,self.guest)['state'],'stopped')
        with self.assertRaises(G.Revoked):self.guest.start(self.request)
        self.assertEqual(self.backend.starts,0)

    def test_uncertain_or_foreign_cleanup_never_clears(self):
        with self.host.acquire(self.request):self.guest.start(self.request)
        self.backend.uncertain=True
        self.assertEqual(self.host.reconcile(self.guest)['state'],'stopping')
        with self.assertRaises(G.Busy):self.host.check()
        class Forged:
            def stop_and_observe(inner,r):return {'binding':dict(r,run_id='c'*64),'unit':G.unit(r),'state':'stopped','guest_boot_id':r['guest_boot_id'],'cgroup_empty':True,'launch_revoked':True}
        self.assertEqual(self.host.reconcile(Forged())['state'],'stopping')
        self.backend.uncertain=False
        self.assertEqual(self.host.reconcile(self.guest)['state'],'stopped')
    def test_dispatch_is_single_execution_after_lost_ack(self):
        self.guest.start(self.request);self.guest.start(self.request)
        self.assertEqual(self.backend.starts,1)
        self.guest.stop_and_observe(self.request)
        with self.assertRaises(G.Revoked):self.guest.start(self.request)
    def test_symlink_corruption_and_nonprivate_store_fail_closed(self):
        with self.host.acquire(self.request):pass
        path=self.root/'host/owner.json';raw=json.loads(path.read_bytes());raw['binding']['generation']=2;path.write_text(json.dumps(raw))
        with self.assertRaises(ValueError):self.host.check()
        path.unlink();path.symlink_to('/etc/passwd')
        with self.assertRaises(OSError):self.host.check()
        (self.root/'unsafe').mkdir(mode=0o755)
        with self.assertRaises(ValueError):G.Store(self.root/'unsafe',os.getuid())
    def test_guest_reboot_cleanup_is_bound_and_old_launch_refused(self):
        with self.host.acquire(self.request):pass
        self.backend.identity=str(uuid.uuid4())
        with self.assertRaises(ValueError):self.guest.start(self.request)
        self.assertEqual(self.host.reconcile(self.guest)['state'],'stopped')
        evidence=self.host.store.read(self.request['run_id']+'.terminal.json')
        self.assertTrue(evidence['cleanup']['boot_changed'])
        self.assertEqual(evidence['cleanup']['binding']['guest_boot_id'],self.request['guest_boot_id'])
        self.assertEqual(self.backend.starts,0)

    def test_replaced_physical_lock_fifo_refused_without_blocking(self):
        self.lock.unlink();os.mkfifo(self.lock)
        with self.assertRaises(ValueError):
            with self.host.acquire(self.request):pass

    def test_malformed_target_and_path_identity_refused(self):
        for key,value in [('run_id','../../host'),('target','worker-chosen'),('generation',True),('guest_boot_id','x')]:
            with self.assertRaises((ValueError,AttributeError)):
                with self.host.acquire(dict(self.request,**{key:value})):pass
    def test_inherited_launch_guard_blocks_stop_until_child_settles(self):
        # Real inherited FD models systemd-run handoff surviving killed parent.
        store=self.guest.store
        with store.guard() as fd:
            child=subprocess.Popen(['/usr/bin/python3','-I','-c','import time;time.sleep(.3)'],pass_fds=(fd,))
        try:
            with self.assertRaises(G.Busy):self.guest.stop_and_observe(self.request)
            child.wait(timeout=3)
            self.assertEqual(self.guest.stop_and_observe(self.request)['state'],'stopped')
            with self.assertRaises(G.Revoked):self.guest.start(self.request)
        finally:
            if child.poll() is None:child.kill();child.wait()

class GuestEnvelopeTests(unittest.TestCase):
    def test_advertised_script_survives_bounded_wire(self):
        import io
        payload={'script':'#'+('x'*(128*1024-1)),'argv':[]}
        raw=json.dumps(payload).encode()
        self.assertEqual(G.read_guest_request(io.BytesIO(raw)),payload)
        with self.assertRaises(ValueError):G.read_guest_request(io.BytesIO(b' '*(G.GUEST_REQUEST_LIMIT+1)))

    def test_maximum_script_and_argv_json_escape_expansion(self):
        import io
        payload={'script':'#'+'\x01'*(128*1024-1),'argv':['\x01'*4096]*32}
        raw=json.dumps(payload).encode()
        self.assertGreater(len(raw),1024*1024)
        self.assertLess(len(raw),G.GUEST_REQUEST_LIMIT)
        self.assertEqual(G.read_guest_request(io.BytesIO(raw)),payload)

class StoreBoundsTests(unittest.TestCase):
    def setUp(self):
        self.tmp=tempfile.TemporaryDirectory();self.addCleanup(self.tmp.cleanup)
        self.root=Path(self.tmp.name);self.store=G.Store(self.root,os.getuid())
    def test_large_script_and_source_inventory_roundtrip(self):
        request={'script':'#'+'x'*(128*1024-1),'argv':[]}
        source={'files':[{'path':'source/file'+str(i),'sha256':'a'*64,'size':1,'mode':292} for i in range(50000)]}
        for name,value in [('request.json',request),('source.json',source),('receipt.json',request)]:
            self.store.write(name,value)
            self.assertGreater((self.root/name).stat().st_size,65536)
            self.assertEqual(self.store.read(name),value)
    def test_oversized_write_never_publishes_or_replaces_record(self):
        for name,limit in [('owner.json',65536),('request.json',2*1024**2),('receipt.json',2*1024**2)]:
            with self.assertRaisesRegex(ValueError,'exceeds limit'):
                self.store.write(name,{'text':'x'*limit})
            self.assertFalse((self.root/name).exists())
            self.assertEqual(list(self.root.glob('.pending-*')),[])
            self.store.write(name,{'old':'retained'})
            before=(self.root/name).read_bytes()
            with self.assertRaises(ValueError):self.store.write(name,{'text':'x'*limit})
            self.assertEqual((self.root/name).read_bytes(),before)
    def test_record_specific_read_limits_reject_sparse_oversize_before_decode(self):
        for name,limit in [('owner.json',65536),('request.json',2*1024**2),('source.json',64*1024**2),('receipt.json',2*1024**2)]:
            path=self.root/name
            with path.open('wb') as stream:stream.truncate(limit+1)
            path.chmod(0o600)
            with self.assertRaisesRegex(ValueError,'unsafe ownership'):self.store.read(name)

    def test_ownership_read_remains_small_and_nofollow(self):
        value={'text':'x'*65536};value['receipt_sha256']=G.digest(value)
        path=self.root/'owner.json';path.write_bytes(G.canonical(value));path.chmod(0o600)
        with self.assertRaisesRegex(ValueError,'unsafe ownership'):self.store.read('owner.json')
        path.unlink();path.symlink_to(self.root/'request.json')
        with self.assertRaises(OSError):self.store.read('owner.json')

class HelperIdentityTests(unittest.TestCase):
    def test_exact_frozen_helpers_and_mutation_refusal(self):
        import hashlib
        with tempfile.TemporaryDirectory() as tmp:
            root=Path(tmp)
            for name in ('lease','executor'):
                p=root/('lectern-autonomy-gpu-'+name+'.py');p.write_bytes(name.encode());p.chmod(0o444)
            expected={name+'_sha256':hashlib.sha256(name.encode()).hexdigest() for name in ('lease','executor')}
            self.assertEqual(G.helper_identity(root,os.getuid()),expected)
            executor=root/'lectern-autonomy-gpu-executor.py';executor.chmod(0o644)
            with self.assertRaisesRegex(ValueError,'unsafe qualified'):G.helper_identity(root,os.getuid())
            executor.chmod(0o444);executor.unlink();executor.symlink_to(root/'lectern-autonomy-gpu-lease.py')
            with self.assertRaises(OSError):G.helper_identity(root,os.getuid())

if __name__=='__main__':unittest.main()
