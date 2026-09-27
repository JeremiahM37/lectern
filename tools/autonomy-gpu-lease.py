#!/usr/bin/python3 -I
"""Shared physical GPU ownership fence; no GPU workload or package code runs here.

Host and guest stores are root-owned. Backend calls are trusted transport, never
worker-supplied evidence. Production workload preparation is a separate executor.
"""
import argparse
import contextlib
import fcntl
import hashlib
import importlib.util
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import uuid

# Frozen per-run helper directories must never acquire mutable bytecode caches.
sys.dont_write_bytecode=True

PHYSICAL_LOCK = Path('/mnt/bulk/inference-research/floor/.machine-leases/host-lxc105.lock')
HOST_STORE = Path('/mnt/bulk/lectern-autonomy/gpu-lease')
GUEST_STORE = Path('/var/lib/lectern-gpu-lease')
TARGET = 'aiserver-amd-research-v1'
STORAGE_FLOOR=22*1024**3
HEX = re.compile(r'[0-9a-f]{64}\Z')

class Busy(RuntimeError): pass
class Revoked(RuntimeError): pass


def canonical(value): return json.dumps(value, sort_keys=True, separators=(',', ':')).encode()
def digest(value): return hashlib.sha256(canonical(value)).hexdigest()
def boot_id(): return Path('/proc/sys/kernel/random/boot_id').read_text().strip()


def helper_sources(directory=None, owner=0):
    directory=Path(__file__).parent if directory is None else Path(directory)
    info=directory.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid!=owner or info.st_mode&0o022:
        raise ValueError('unsafe qualified GPU helper directory')
    result={}
    for name in ('executor','lease'):
        path=directory/('autonomy-gpu-'+name+'.py')
        if not path.exists():path=directory/('lectern-autonomy-gpu-'+name+'.py')
        fd=os.open(path,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
        with os.fdopen(fd,'rb') as stream:
            info=os.fstat(stream.fileno())
            if not stat.S_ISREG(info.st_mode) or info.st_uid!=owner or info.st_nlink!=1 or info.st_mode&0o222 or info.st_size>1024*1024:
                raise ValueError('unsafe qualified GPU helper')
            raw=stream.read(1024*1024+1)
            if len(raw)>1024*1024:raise ValueError('qualified GPU helper exceeds limit')
        result[name]=raw
    return result


def helper_identity(directory=None, owner=0):
    return {name+'_sha256':hashlib.sha256(raw).hexdigest()
            for name,raw in helper_sources(directory,owner).items()}

def binding(value):
    if not isinstance(value, dict) or set(value) != {'schema_version','target','run_id','generation','request_sha256','guest_boot_id'}:
        raise ValueError('invalid ownership binding')
    if value['schema_version'] != 1 or value['target'] != TARGET:
        raise ValueError('unsupported ownership target')
    if not all(isinstance(value[k], str) and HEX.fullmatch(value[k]) for k in ('run_id','request_sha256')):
        raise ValueError('invalid ownership identity')
    if type(value['generation']) is not int or not 1 <= value['generation'] <= 1000000:
        raise ValueError('invalid ownership generation')
    if str(uuid.UUID(value['guest_boot_id'])) != value['guest_boot_id']:
        raise ValueError('invalid guest boot identity')
    return dict(value)


def unit(value): return 'lectern-gpu-run-' + binding(value)['run_id'] + '.service'


def valid_cleanup_boot(proof, request):
    try: current = str(uuid.UUID(proof['guest_boot_id']))
    except (ValueError, KeyError, AttributeError): return False
    return proof.get('boot_changed') is (current != request['guest_boot_id'])


RECORD_LIMITS = {'request.json': 2*1024**2, 'source.json': 64*1024**2, 'receipt.json': 2*1024**2}
DEFAULT_RECORD_LIMIT = 65536
GUEST_REQUEST_LIMIT = 2*1024**2


class Store:
    def __init__(self, root, owner=0):
        self.root = Path(root)
        self.owner = owner
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        st = self.root.lstat()
        if not stat.S_ISDIR(st.st_mode) or st.st_uid != owner or st.st_mode & 0o077:
            raise ValueError('unsafe private ownership store')

    def read(self, name):
        limit = RECORD_LIMITS.get(name, DEFAULT_RECORD_LIMIT)
        path = self.root / name
        try: fd = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        except FileNotFoundError: return None
        with os.fdopen(fd, 'rb') as f:
            st = os.fstat(f.fileno())
            if not stat.S_ISREG(st.st_mode) or st.st_nlink != 1 or st.st_uid != self.owner or st.st_mode & 0o077 or st.st_size > limit:
                raise ValueError('unsafe ownership record')
            raw = f.read(limit+1)
            if len(raw) > limit: raise ValueError('ownership record exceeds limit')
        value = json.loads(raw)
        checksum = value.pop('receipt_sha256', None)
        if checksum != digest(value): raise ValueError('ownership record checksum differs')
        return value

    def write(self, name, value):
        value = dict(value)
        value['receipt_sha256'] = digest(value)
        raw = canonical(value)
        if len(raw) > RECORD_LIMITS.get(name, DEFAULT_RECORD_LIMIT):
            raise ValueError('ownership record exceeds limit')
        pending = self.root / ('.pending-' + uuid.uuid4().hex)
        fd = os.open(pending, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        try:
            with os.fdopen(fd, 'wb') as f:
                f.write(raw); f.flush(); os.fsync(f.fileno())
            os.replace(pending, self.root / name)
            self.sync()
        finally:
            if pending.exists(): pending.unlink()

    def sync(self):
        fd = os.open(self.root, os.O_DIRECTORY | os.O_NOFOLLOW)
        try: os.fsync(fd)
        finally: os.close(fd)

    @contextlib.contextmanager
    def guard(self):
        fd = os.open(self.root/'guard', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            st = os.fstat(fd)
            if not stat.S_ISREG(st.st_mode) or st.st_uid != self.owner or st.st_nlink != 1 or st.st_mode & 0o077:
                raise ValueError('unsafe ownership guard')
            # Bounded callers retry; OFF must not block behind a stalled launcher.
            try: fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError: raise Busy('ownership transition pending')
            yield fd
        finally: os.close(fd)


class HostFence:
    def __init__(self, store, physical_lock=PHYSICAL_LOCK):
        self.store, self.physical_lock = store, Path(physical_lock)

    def check(self):
        with self.store.guard():
            if self.store.read('owner.json') is not None:
                raise Busy('owned guest cleanup remains unresolved')
        return {'state':'clear','scope':'cooperative lease fence only'}

    @contextlib.contextmanager
    def acquire(self, request):
        request = binding(request)
        # Existing lock must already exist. Never create, truncate or replace it.
        fd = os.open(self.physical_lock, os.O_RDWR | os.O_NOFOLLOW | os.O_NONBLOCK)
        try:
            info = os.fstat(fd)
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 1:
                raise ValueError('unsafe physical lease inode')
            try: fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
            except BlockingIOError: raise Busy('physical lease busy')
            with self.store.guard():
                old = self.store.read('owner.json')
                if old is not None: raise Busy('owned guest cleanup remains unresolved')
                if self.store.read(request['run_id']+'.terminal.json') is not None or self.store.read(request['run_id']+'.revoked.json') is not None:

                    raise Revoked('executed or cancelled run cannot be launched again')
                self.store.write('owner.json', {'binding':request,'host_boot_id':boot_id(),
                    'lock_device':info.st_dev,'lock_inode':info.st_ino,'state':'reserved'})
            # Fence deliberately survives this context, any exception, or SIGKILL.
            # Caller must invoke reconcile before a new owner can be admitted.
            yield request
        finally: os.close(fd)

    def _cleanup(self, request, backend):
        proof = backend.stop_and_observe(request)
        if proof.get('binding') != request or proof.get('state') != 'stopped' or proof.get('unit') != unit(request) or not valid_cleanup_boot(proof, request) or proof.get('cgroup_empty') is not True or proof.get('launch_revoked') is not True:
            return {'state':'stopping','reason':'authoritative owned cleanup not yet confirmed'}
        terminal = request['run_id']+'.terminal.json'
        if self.store.read(terminal) is None:
            self.store.write(terminal, {'binding':request,'state':'stopped','cleanup':proof})
        owner = self.store.read('owner.json')
        if owner and owner['binding'] == request:
            (self.store.root/'owner.json').unlink(); self.store.sync()
        return {'state':'stopped','binding':request}

    def cancel(self, request, backend):
        request = binding(request)
        with self.store.guard():
            name = request['run_id']+'.revoked.json'
            old = self.store.read(name)
            if old and old['binding'] != request: raise ValueError('cancel identity differs')
            self.store.write(name, {'binding':request,'state':'revoked'})
            # A transport failure leaves the tombstone and any owner fence intact.
            return self._cleanup(request, backend)

    def reconcile(self, backend):
        with self.store.guard():
            owner = self.store.read('owner.json')
            if owner is None: return {'state':'clear'}
            return self._cleanup(binding(owner['binding']), backend)


class GuestFence:
    """Trusted guest dispatcher; backend launches only a separately sealed unit.

    Backend.launch must preserve the supplied guard FD through any asynchronous
    launcher handoff. Otherwise stop could race a child after its parent dies.
    """
    def __init__(self, store, backend): self.store, self.backend = store, backend

    def start(self, request):
        request = binding(request); run = request['run_id']
        with self.store.guard() as guard:
            if request['guest_boot_id'] != self.backend.boot_id(): raise ValueError('guest reboot requires reconciliation')
            old = self.store.read(run+'.json')
            if old:
                if old['binding'] != request: raise ValueError('run identity differs')
                if old.get('revoked'): raise Revoked('run cancelled before launch')
                # Never repeat an uncertain dispatch: stop/reconcile, new run ID.
                return {'state':old['state'],'binding':request}
            self.store.write(run+'.json', {'binding':request,'state':'launch_requested','revoked':False})
            self.backend.launch(request, guard)
            self.store.write(run+'.json', {'binding':request,'state':'started','revoked':False})
            return {'state':'started','binding':request}

    def stop_and_observe(self, request):
        request = binding(request); run = request['run_id']
        with self.store.guard():
            old = self.store.read(run+'.json')
            if old and old['binding'] != request: raise ValueError('run identity differs')
            self.store.write(run+'.json', {'binding':request,'state':'stop_requested','revoked':True})
            # Includes cancellation before request dispatch; late start sees tombstone.
            self.backend.stop(request)
            observed = self.backend.observe(request)
            observed_boot = str(uuid.UUID(observed['boot_id']))
            stopped = observed.get('inactive') is True and observed.get('cgroup_empty') is True
            proof = {'binding':request,'unit':unit(request),'guest_boot_id':observed.get('boot_id'),
                     'state':'stopped' if stopped else 'stopping','cgroup_empty':stopped,'launch_revoked':True,
                     'boot_changed':observed_boot != request['guest_boot_id']}
            if stopped: self.store.write(run+'.json', {'binding':request,'state':'stopped','revoked':True,'cleanup':proof})
            return proof


class SystemdGuestBackend:
    """Fixed prepared executor launch and authoritative owned unit cleanup."""
    def boot_id(self): return boot_id()

    def launch(self, request, guard):
        run = GUEST_STORE/'runs'/request['run_id']
        prepared = Store(run).read('request.json')
        if prepared is None or prepared['binding'] != request: raise ValueError('prepared execution binding differs')
        helper = run/'helpers/lectern-autonomy-gpu-executor.py'
        if helper_identity(run/'helpers') != prepared.get('helpers'):
            raise ValueError('prepared GPU helper identity differs')
        st = helper.lstat()
        if not stat.S_ISREG(st.st_mode) or st.st_uid != 0 or st.st_mode & 0o022: raise ValueError('unsafe guest executor')
        spec = importlib.util.spec_from_file_location('gpu_executor',helper)
        executor = importlib.util.module_from_spec(spec);spec.loader.exec_module(executor)
        profile = executor.PROFILES[prepared['profile']]
        command = ['/usr/bin/systemd-run','--quiet','--collect','--unit='+unit(request),
            '--property=RuntimeMaxSec='+str(profile['seconds']+20),'--property=MemoryMax='+str(profile['memory']),
            '--property=MemorySwapMax=0','--property=CPUQuota='+str(profile['cpu'])+'%',
            '--property=TasksMax='+str(profile['tasks']),'--property=KillMode=control-group',
            '--property=TimeoutStopSec=5','--property=PrivateMounts=yes','--property=UMask=0077',
            '--property=TemporaryFileSystem='+str(run/'scratch')+':rw,nosuid,nodev,size='+str(profile['scratch'])+',mode=0700,uid=65534,gid=65534',
            '/usr/bin/python3','-I',str(helper),'execute','--run',request['run_id']]
        subprocess.run(command,check=True,timeout=15,pass_fds=(guard,),capture_output=True)

    def stop(self, request):
        result = subprocess.run(['/usr/bin/systemctl','stop','--no-block',unit(request)],
                                capture_output=True, timeout=5)
        if result.returncode:
            # Absent never-launched units are reconciled by authoritative show,
            # not by accepting a failed stop as proof of cleanup.
            self.observe(request)

    def observe(self, request):
        name = unit(request)
        result = subprocess.run(['/usr/bin/systemctl','show',name,
            '--property=LoadState,ActiveState,SubState,MainPID,ControlPID,ControlGroup,Job'],
            capture_output=True, text=True, timeout=5)
        values = dict(line.split('=',1) for line in result.stdout.splitlines() if '=' in line)
        required = {'LoadState','ActiveState','MainPID','ControlPID','ControlGroup','Job'}
        if not required <= values.keys() or result.returncode not in (0,1):
            raise RuntimeError('guest unit state unavailable')
        expected = '/system.slice/' + name
        group = values['ControlGroup']
        if group not in ('',expected): raise ValueError('owned unit cgroup differs')
        # Even when systemd forgot the unit, inspect its known cgroup location.
        events = Path('/sys/fs/cgroup' + expected) / 'cgroup.events'
        try:
            rows = dict(line.split() for line in events.read_text().splitlines())
            empty = rows.get('populated') == '0'
        except FileNotFoundError: empty = not events.parent.exists()
        inactive = values['ActiveState'] in ('inactive','failed') and values['MainPID']=='0' and values['ControlPID']=='0' and values['Job'] in ('','0')
        return {'boot_id':boot_id(),'inactive':inactive,'cgroup_empty':empty}


class FixedGuestTransport:
    """Only guest105 and this installed root helper; no worker path or command."""
    def stop_and_observe(self, request):
        request = binding(request)
        result = subprocess.run(['/usr/sbin/pct','exec','105','--','/usr/bin/python3','-I',
            '/usr/local/libexec/lectern-autonomy-gpu-lease.py','guest-stop'],
            input=canonical(request),capture_output=True,timeout=15)
        if result.returncode != 0 or len(result.stdout)>65536:
            raise RuntimeError('registered guest cleanup transport unavailable')
        return json.loads(result.stdout)


def read_guest_request(stream):
    # The complete wire envelope is bounded separately from its script/argv
    # fields. A valid 128 KiB script must survive transport to the validator.
    raw=stream.read(GUEST_REQUEST_LIMIT+1)
    if len(raw)>GUEST_REQUEST_LIMIT:raise ValueError('ownership request exceeds limit')
    return json.loads(raw)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('command', choices=['check','guest-stop','guest-start','guest-status','guest-prepare','guest-identity','reconcile'])
    args = parser.parse_args()
    if os.geteuid() != 0: raise SystemExit('root helper required')
    try:
        if args.command == 'guest-identity':
            result={'target':TARGET,'guest_boot_id':boot_id(),'helpers':helper_identity(),
                    'storage_free_bytes':shutil.disk_usage('/var/lib').free,'storage_floor_bytes':STORAGE_FLOOR}
        elif args.command in ('guest-stop','guest-start','guest-status','guest-prepare'):
            import sys
            request = read_guest_request(sys.stdin.buffer)
            guest = GuestFence(Store(GUEST_STORE),SystemdGuestBackend())
            if args.command == 'guest-prepare':
                helper=Path(__file__).with_name('autonomy-gpu-executor.py')
                if not helper.exists():helper=Path(__file__).with_name('lectern-autonomy-gpu-executor.py')
                spec=importlib.util.spec_from_file_location('gpu_executor',helper);executor=importlib.util.module_from_spec(spec);spec.loader.exec_module(executor)
                run=binding(request['binding'])['run_id']
                executor.prepare(request,GUEST_STORE/'incoming'/run/'source.tar.gz')
                result={'state':'prepared','binding':request['binding']}
            elif args.command == 'guest-status':
                request=binding(request)
                execution=Store(GUEST_STORE/'runs'/request['run_id']).read('execution.json')
                if execution and execution.get('binding')!=request:raise ValueError('execution receipt binding differs')
                result={'binding':request,'observation':SystemdGuestBackend().observe(request),'execution':execution,
                        'execution_receipt_sha256':digest(execution) if execution else None}
            else:result = guest.start(request) if args.command == 'guest-start' else guest.stop_and_observe(request)
        else:
            host = HostFence(Store(HOST_STORE))
            result = host.check() if args.command == 'check' else host.reconcile(FixedGuestTransport())
        print(json.dumps(result))
    except Busy as exc: print(json.dumps({'state':'busy','reason':str(exc)})); raise SystemExit(75)

if __name__ == '__main__': main()
