#!/usr/bin/python3 -I
"""Fixed asynchronous host transport. No caller host paths or shell commands.
Imported by privileged runner; controller hooks are installed separately.
"""
import argparse,hashlib,importlib.util,json,os,pwd,shutil,signal,stat,subprocess,sys,time
from pathlib import Path

def module(path,name):
    spec=importlib.util.spec_from_file_location(name,path);value=importlib.util.module_from_spec(spec)
    previous=sys.dont_write_bytecode;sys.dont_write_bytecode=True
    try:spec.loader.exec_module(value)
    finally:sys.dont_write_bytecode=previous
    return value

def companion(name):
    local=Path(__file__).with_name(name)
    return local if local.exists() else Path(__file__).with_name('lectern-'+name)
L=module(companion('autonomy-gpu-lease.py'),'gpu_lease')
FIELDS={'schema_version','owner_job','owner_task','role','experiment_root','source_job','source_snapshot_id','source_tree_sha256','source_archive_sha256','admission_sha256','acceptance_sha256','target','runtime_key','qualification_sha256','profile','script','argv','trial'}
TERMINAL={'exited','cancelled','timeout','output_limit','interrupted','unavailable'}

class Unsupported(ValueError):pass

def request(R,job,run):
    R.job_path(job)
    if not L.HEX.fullmatch(run):raise ValueError('invalid GPU run')
    path=R.job_path(job)/'gpu-requests'/(run+'.json');info=R.regular(path)
    if info.st_uid not in (0,pwd.getpwnam('admin').pw_uid) or info.st_mode&0o077 or info.st_size>2*1024**2:raise ValueError('unsafe controller GPU envelope')
    raw=path.read_bytes();value=json.loads(raw)
    if set(value)!=FIELDS or value['schema_version']!=1 or value['owner_job']!=job or type(value['owner_task']) is not int or value['owner_task']<=0:raise ValueError('invalid GPU ownership')
    R.job_path(value['source_job'])
    if not isinstance(value['source_snapshot_id'],str) or (value['source_snapshot_id'] and not L.HEX.fullmatch(value['source_snapshot_id'])):raise ValueError('invalid GPU source snapshot')
    for field in ('experiment_root','source_archive_sha256','admission_sha256','acceptance_sha256','runtime_key','qualification_sha256'):
        if not isinstance(value[field],str) or not L.HEX.fullmatch(value[field]):raise ValueError('invalid GPU identity')
    if not isinstance(value['source_tree_sha256'],str) or (value['source_tree_sha256'] and not L.HEX.fullmatch(value['source_tree_sha256'])) or (value['source_snapshot_id'] and not value['source_tree_sha256']):raise ValueError('invalid GPU source tree')
    if value['target']!=L.TARGET or value['profile']!='gpu-screen600' or value['role'] not in ('builder','reviewer','auditor_a','auditor_b'):raise ValueError('invalid GPU target/profile')
    if not isinstance(value['script'],str) or not value['script'].strip() or len(value['script'].encode())>128*1024 or '\0' in value['script']:raise ValueError('GPU script bound')
    if not isinstance(value['argv'],list) or len(value['argv'])>32 or any(not isinstance(a,str) or len(a.encode())>4096 or '\0' in a for a in value['argv']):raise ValueError('GPU argv bound')
    if type(value['trial']) is not int or not 0<=value['trial']<5:raise ValueError('GPU trial bound')
    key=hashlib.sha256(raw).hexdigest()
    if hashlib.sha256((job+':'+key).encode()).hexdigest()!=run:raise ValueError('GPU run/request digest differs')
    return value,key

def stage(R,run):
    if not L.HEX.fullmatch(run):raise ValueError('invalid GPU run')
    return L.Store(R.ROOT.parent/'gpu-research'/run)

def unit(run,cleanup=False):
    if not L.HEX.fullmatch(run):raise ValueError('invalid GPU run')
    return 'lectern-gpu-'+('cleanup-' if cleanup else 'supervisor-')+run+'.service'

def common(job,run,key):return {'run_id':run,'owner_job':job,'request_sha256':key}

def qualified_helpers(data,store=None):
    names={'supervisor_sha256':'autonomy-gpu-runtime.py','executor_sha256':'autonomy-gpu-executor.py','lease_sha256':'autonomy-gpu-lease.py','telemetry_sha256':'autonomy-gpu-telemetry.py'}
    expected=data.get('helpers')
    if not isinstance(expected,dict) or set(expected)!=set(names) or any(not isinstance(v,str) or not L.HEX.fullmatch(v) for v in expected.values()):raise Unsupported('GPU qualification lacks exact control helper identities')
    for key,name in names.items():
        frozen=store.root/'tools'/name if store else None
        path=frozen if frozen and frozen.exists() else companion(name)
        info=path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_nlink!=1 or info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe qualified GPU helper')
        if hashlib.sha256(path.read_bytes()).hexdigest()!=expected[key]:raise Unsupported('GPU qualification stale: control helper changed')

def qualification(R,value,store=None):
    data=store.read('qualification.json') if store else None
    if data:checksum=L.digest(data)
    else:
        path=R.ROOT.parent/'dependencies/gpu/qualification.json'
        try:info=R.regular(path)
        except FileNotFoundError:raise Unsupported('GPU runtime has no completed qualification')
        if info.st_uid!=0 or info.st_mode&0o222 or info.st_size>65536:raise ValueError('untrusted GPU qualification')
        data=json.loads(path.read_bytes());checksum=data.pop('receipt_sha256',None)
        if checksum!=L.digest(data):raise ValueError('GPU qualification checksum differs')
    if checksum!=value['qualification_sha256']:raise Unsupported('GPU qualification changed after admission')
    if data.get('qualified') is not True or data.get('schema_version')!=1 or any(data.get(k)!=value[k] for k in ('target','runtime_key','profile')):raise Unsupported('requested GPU runtime/profile has not qualified')
    for key in ('device_bdf','kernel_release','driver_sha256'):
        if not isinstance(data.get(key),str) or not data[key]:raise ValueError('incomplete qualified GPU identity')
    qualified_helpers(data,store)
    if store:store.write('qualification.json',data)
    return dict(data,receipt_sha256=checksum)

def guest_identity(guest,qualified):
    identity=guest.identity()
    if identity.get('target')!=L.TARGET:raise Unsupported('GPU qualification stale: guest target differs')
    helpers=identity.get('helpers',{})
    for key in ('executor_sha256','lease_sha256'):
        if helpers.get(key)!=qualified['helpers'][key]:raise Unsupported('GPU qualification stale: guest control helper changed')
    return identity

def status(R,job,run):
    store=stage(R,run)
    sealed=store.read('request.json')
    if sealed:
        if sealed['value']['owner_job']!=job:raise ValueError('GPU owner differs')
        key=sealed['key']
    else:_,key=request(R,job,run)
    receipt=store.read('receipt.json')
    if receipt:
        if receipt.get('state') in TERMINAL:receipt['receipt_sha256']=L.digest(receipt)
        return receipt
    return dict(common(job,run,key),state='waiting',reason='GPU supervisor has not produced a receipt')

def seal_request(R,job,run,store):
    value,key=request(R,job,run);old=store.read('request.json')
    if old and old!={'value':value,'key':key}:raise ValueError('sealed GPU request differs')
    if not old:store.write('request.json',{'value':value,'key':key})
    return value,key

def freeze_tools(R,store):
    directory=store.root/'tools';directory.mkdir(mode=0o700,exist_ok=True)
    sources={name:companion(name) for name in ('autonomy-gpu-runtime.py','autonomy-gpu-lease.py','autonomy-gpu-executor.py','autonomy-gpu-telemetry.py','autonomy-gpu-snapshot.py')}
    sources['autonomy-runner.py']=Path(R.INSTALL)
    hashes={}
    old=store.read('tools.json')
    if old:
        if set(old)!=set(sources):raise ValueError('incomplete frozen GPU helpers')
        for name,expected in old.items():
            target=directory/name;info=R.regular(target)
            if info.st_uid!=0 or info.st_mode&0o222 or R.digest_file(target)!=expected:raise ValueError('frozen GPU helpers changed')
        return directory
    for name,path in sources.items():
        info=R.regular(path)
        if info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe installed GPU helper')
        target=directory/name
        if not target.exists():shutil.copyfile(path,target);target.chmod(0o444)
        hashes[name]=R.digest_file(target)
    old=store.read('tools.json')
    if old and old!=hashes:raise ValueError('frozen GPU helpers changed')
    if not old:store.write('tools.json',hashes)
    return directory

def launch(R,job,run):
    store=stage(R,run)
    with store.guard() as guard:
        value,key=seal_request(R,job,run,store)
        old=store.read('receipt.json')
        if old and old.get('state') in TERMINAL and old.get('cleanup_confirmed') is True:return status(R,job,run)
        cancellation=store.read('cancel.json')
        if cancellation:
            if cancellation['owner_job']!=job:raise ValueError('GPU cancellation owner differs')
            if not store.read('binding.json'):
                store.write('receipt.json',dict(common(job,run,key),state='cancelled',reason='GPU authority revoked before dispatch',executed=False,charged_ms=0,cleanup_confirmed=True))
            return status(R,job,run)
        old=store.read('receipt.json')
        if old and old.get('state') in TERMINAL:return status(R,job,run)
        if R.completion_service_active(unit(run)):return status(R,job,run)
        # A retained guest binding must be reconciled by the frozen supervisor
        # even if today's selector or globally installed helpers have changed.
        if not store.read('binding.json'):
            try:qualification(R,value,store)
            except Unsupported as exc:
                result=dict(common(job,run,key),state='unavailable',reason=str(exc),executed=False,charged_ms=0,cleanup_confirmed=True)
                store.write('receipt.json',result);return status(R,job,run)
        directory=freeze_tools(R,store)
        command=['/usr/bin/systemd-run','--quiet','--collect','--unit='+unit(run),'--property=RuntimeMaxSec=900','--property=TimeoutStopSec=20','--property=MemoryMax=1G','--property=CPUQuota=200%','--property=TasksMax=64','--property=KillMode=control-group','--property=UMask=0077',
                 '/usr/bin/python3','-I',str(directory/'autonomy-gpu-runtime.py'),'execute','--job',job,'--run',run]
        store.write('receipt.json',dict(common(job,run,key),state='preparing'))
        subprocess.run(command,check=True,capture_output=True,timeout=5,pass_fds=(guard,))
        return status(R,job,run)

class GuestTransport(L.FixedGuestTransport):
    def call(self,command,data=None,timeout=20):
        result=subprocess.run(['/usr/sbin/pct','exec','105','--','/usr/bin/python3','-I','/usr/local/libexec/lectern-autonomy-gpu-lease.py',command],input=L.canonical(data) if data is not None else b'',capture_output=True,timeout=timeout)
        if result.returncode or len(result.stdout)>65536:raise RuntimeError('fixed GPU guest transport failed')
        return json.loads(result.stdout)
    def identity(self):return self.call('guest-identity')
    def prepare(self,execution,archive):
        bound=L.binding(execution['binding']);incoming='/var/lib/lectern-gpu-lease/incoming/'+bound['run_id']
        subprocess.run(['/usr/sbin/pct','exec','105','--','/usr/bin/install','-d','-m','0700',incoming],check=True,capture_output=True,timeout=10)
        subprocess.run(['/usr/sbin/pct','push','105',str(archive),incoming+'/source.tar.gz','--perms','0400'],check=True,capture_output=True,timeout=120)
        result=self.call('guest-prepare',execution,timeout=180)
        if result.get('binding')!=bound or result.get('state')!='prepared':raise ValueError('guest preparation binding differs')
    def start(self,bound):return self.call('guest-start',bound)
    def status(self,bound):return self.call('guest-status',bound)
    def output(self,bound,store,execution):
        bound=L.binding(bound);path=store.root/'output.log'
        if not path.exists():
            pending=store.root/'output.pending'
            if pending.exists():pending.unlink()
            subprocess.run(['/usr/sbin/pct','pull','105','/var/lib/lectern-gpu-lease/runs/'+bound['run_id']+'/output.log',str(pending)],check=True,capture_output=True,timeout=30)
            info=pending.lstat()
            if not stat.S_ISREG(info.st_mode) or info.st_nlink!=1 or info.st_size>16*1024**2:raise ValueError('unsafe GPU output')
            if info.st_size!=execution['output_bytes'] or hashlib.sha256(pending.read_bytes()).hexdigest()!=execution['output_sha256']:raise ValueError('GPU output differs from execution receipt')
            pending.chmod(0o400)
            with pending.open('rb') as source:os.fsync(source.fileno())
            os.replace(pending,path);store.sync()
        info=path.lstat()
        if not stat.S_ISREG(info.st_mode) or info.st_nlink!=1 or info.st_size!=execution['output_bytes'] or info.st_size>16*1024**2 or hashlib.sha256(path.read_bytes()).hexdigest()!=execution['output_sha256']:raise ValueError('GPU retained output differs')

def telemetry(qualified,running=False):
    path=companion('autonomy-gpu-telemetry.py');helper=module(path,'gpu_telemetry')
    try:
        collected=subprocess.run(['/usr/bin/python3','-I',str(path)],capture_output=True,timeout=3)
        if collected.returncode or len(collected.stdout)>65536:raise RuntimeError('telemetry collector unavailable')
        observed=json.loads(collected.stdout)['observation']
    except (subprocess.SubprocessError,ValueError,KeyError,RuntimeError):
        return {},{'decision':'abort' if running else 'wait','reason':'telemetry unavailable or timed out'}
    if observed.get('state')=='observed':
        for key in ('device_bdf','kernel_release','driver_sha256'):
            if observed.get(key)!=qualified.get(key):raise Unsupported('GPU target identity changed after qualification')
    screening=L.Store(L.HOST_STORE)
    with screening.guard():
        old=screening.read('thermal.json') or {}
        decision=helper.evaluate(observed,prior_latched=old.get('latched',False),running=running)
        screening.write('thermal.json',{'latched':decision.get('latched',old.get('latched',False))})
    return observed,decision

def save_telemetry(store,observation,decision):
    raw=L.canonical({'observation':observation,'decision':decision})+b'\n'
    path=store.root/'telemetry.jsonl'
    fd=os.open(path,os.O_WRONLY|os.O_APPEND|os.O_CREAT|os.O_NOFOLLOW,0o600)
    with os.fdopen(fd,'ab') as output:
        info=os.fstat(output.fileno())
        if not stat.S_ISREG(info.st_mode) or info.st_nlink!=1 or info.st_size+len(raw)>4*1024**2:raise RuntimeError('GPU telemetry bound')
        output.write(raw);output.flush()


def finish(store,job,run,key,bound,guest,fence):
    old=store.read('receipt.json')
    if old and old.get('state') in TERMINAL and old.get('cleanup_confirmed') is True:
        if any(old.get(k)!=v for k,v in common(job,run,key).items()):raise ValueError('GPU terminal ownership differs')
        return True
    cleanup=fence.cancel(bound,guest)
    if cleanup.get('state')!='stopped':return False
    report=guest.status(bound)
    execution=report.get('execution')
    if execution and execution.get('binding')!=bound:raise ValueError('GPU execution binding differs')
    if execution:
        if report.get('execution_receipt_sha256')!=L.digest(execution):raise ValueError('GPU guest execution checksum differs')
        if execution.get('state') not in ('exited','cancelled','timeout','output_limit') or type(execution.get('elapsed_ms')) is not int or execution['elapsed_ms']<0 or type(execution.get('exit_code')) is not int or not L.HEX.fullmatch(execution.get('output_sha256','')):raise ValueError('GPU guest execution fields differ')
        if hasattr(guest,'output'):guest.output(bound,store,execution)
        state=execution['state'];elapsed=min(600000,max(0,int(execution['elapsed_ms'])))
        result=dict(common(job,run,key),state=state,executed=True,elapsed_ms=execution['elapsed_ms'],charged_ms=elapsed,exit_code=execution['exit_code'],output_sha256=execution['output_sha256'],execution_receipt_sha256=report['execution_receipt_sha256'])
    else:
        started=store.read('dispatch.json') is not None
        result=dict(common(job,run,key),state='interrupted' if started else 'cancelled',executed=None if started else False,charged_ms=600000 if started else 0)
    failure=store.read('failure.json')
    if failure:result['reason']=failure['reason']
    result.update(cleanup_confirmed=True,execution_binding=bound)
    if (store.root/'telemetry.jsonl').exists():result['telemetry_sha256']=hashlib.sha256((store.root/'telemetry.jsonl').read_bytes()).hexdigest()
    store.write('receipt.json',result);return True

def execute(R,job,run,guest=None,fence=None):
    store=stage(R,run);sealed=store.read('request.json');value,key=sealed['value'],sealed['key']
    guest=guest or GuestTransport();fence=fence or L.HostFence(L.Store(L.HOST_STORE))
    cancelled=[False];signal.signal(signal.SIGTERM,lambda *_:cancelled.__setitem__(0,True))
    bound=store.read('binding.json');reserved=False;must_cleanup=False
    try:
        owner=fence.store.read('owner.json') if bound and hasattr(fence,'store') else None
        retained_owner=isinstance(owner,dict) and owner.get('binding')==bound
        if bound and (retained_owner or store.read('dispatch.json') or store.read('cancel.json')):
            # A recorded dispatch is never replayed after supervisor loss.
            finish(store,job,run,key,bound,guest,fence);return
        qualified=qualification(R,value,store)
        if store.read('cancel.json'):raise L.Revoked('GPU authority revoked')
        observed,decision=telemetry(qualified);save_telemetry(store,observed,decision)
        if decision['decision']!='allow':
            store.write('receipt.json',dict(common(job,run,key),state='waiting',reason=decision['reason']));return
        if value['source_snapshot_id']:
            snapshots=module(companion('autonomy-gpu-snapshot.py'),'gpu_snapshot')
            captured=snapshots.context(R,L,value['source_job'],value['source_snapshot_id'])
            identity=captured.read('receipt.json')
            if not identity or identity.get('state')!='ready' or identity.get('owner_job')!=value['source_job'] or identity.get('snapshot_id')!=value['source_snapshot_id'] or identity.get('source_archive_sha256')!=value['source_archive_sha256'] or identity.get('source_tree_sha256')!=value['source_tree_sha256']:raise ValueError('GPU source snapshot binding differs')
            archive=captured.root/'captured/source.tar.gz'
        else:
            archive=R.job_path(value['source_job'])/'artifact.tar.gz'
            if R.archive_identity(value['source_job'])['sha256']!=value['source_archive_sha256']:raise ValueError('retained GPU source archive identity differs')
        if R.digest_file(archive)!=value['source_archive_sha256']:raise ValueError('retained GPU source archive differs')
        if shutil.disk_usage(store.root).free<20*1024**3:raise RuntimeError('GPU staging free-space floor')
        execution={k:value[k] for k in ('source_archive_sha256','runtime_key','profile','script','argv')}
        execution['helpers']={k:qualified['helpers'][k] for k in ('executor_sha256','lease_sha256')}
        guest_info=guest_identity(guest,qualified);boot=guest_info['guest_boot_id']
        if bound and bound['guest_boot_id']!=boot:
            must_cleanup=True;raise Unsupported('GPU guest rebooted after preparation; reconcile this run before a fresh capture')
        proposed={'schema_version':1,'target':L.TARGET,'run_id':run,'generation':1,'request_sha256':L.digest(execution),'guest_boot_id':boot}
        if bound and bound!=proposed:raise ValueError('GPU prepared execution binding changed')
        bound=proposed
        store.write('binding.json',bound);execution['binding']=bound
        prepared=store.read('guest-prepared.json')
        if prepared is None:
            if type(guest_info.get('storage_free_bytes')) is not int or guest_info.get('storage_floor_bytes')!=22*1024**3:raise Unsupported('GPU guest storage metadata is unavailable or incompatible')
            if guest_info['storage_free_bytes']<guest_info['storage_floor_bytes']:raise RuntimeError('GPU guest source staging waits for22 GiB free-space floor')
            guest.prepare(execution,archive);store.write('guest-prepared.json',{'binding':bound})
        elif prepared!={'binding':bound}:raise ValueError('GPU guest preparation changed')
        with fence.acquire(bound):
            reserved=True
            if cancelled[0] or store.read('cancel.json'):raise L.Revoked('GPU authority revoked')
            observed,decision=telemetry(qualified);save_telemetry(store,observed,decision)
            if decision['decision']!='allow':raise RuntimeError('GPU live launch gate: '+decision['reason'])
            if guest_identity(guest,qualified)['guest_boot_id']!=bound['guest_boot_id']:raise Unsupported('GPU guest rebooted before dispatch')
            store.write('dispatch.json',{'binding':bound,'state':'launch_requested'})
            guest.start(bound);store.write('receipt.json',dict(common(job,run,key),state='running',execution_binding=bound))
            deadline=time.monotonic()+620
            while time.monotonic()<deadline:
                result=guest.status(bound)
                if result.get('execution'):break
                observed_unit=result.get('observation',{})
                if observed_unit.get('inactive') is True and observed_unit.get('cgroup_empty') is True:
                    store.write('failure.json',{'reason':'guest unit ended without complete execution receipt; conservative unknown-execution accounting retained'})
                    break
                if cancelled[0] or store.read('cancel.json'):break
                observed,decision=telemetry(qualified,running=True);save_telemetry(store,observed,decision)
                if decision['decision']!='allow':break
                time.sleep(2)
            finish(store,job,run,key,bound,guest,fence)
    except (Unsupported,ValueError) as exc:
        must_cleanup=bound is not None
        store.write('failure.json',{'reason':str(exc)[:1024]})
        if bound is not None:
            store.write('receipt.json',dict(common(job,run,key),state='waiting',reason=str(exc)[:1024]))
        else:
            store.write('receipt.json',dict(common(job,run,key),state='unavailable',reason=str(exc)[:1024],executed=False,charged_ms=0,cleanup_confirmed=True))
    except (L.Busy,L.Revoked,RuntimeError,subprocess.SubprocessError,OSError) as exc:
        store.write('receipt.json',dict(common(job,run,key),state='waiting',reason=str(exc)[:1024]))
    finally:
        if bound is not None and (must_cleanup or reserved or store.read('dispatch.json') or cancelled[0] or store.read('cancel.json')):
            try:finish(store,job,run,key,bound,guest,fence)
            except Exception:pass # Uncertain marker remains; stop/status reconciliation owns retry.

def stop(R,job,run):
    R.job_path(job);store=stage(R,run)
    with store.guard() as guard:
        sealed=store.read('request.json')
        if sealed:
            if sealed['value']['owner_job']!=job:raise ValueError('GPU owner differs')
            key=sealed['key']
        else:
            # Controller may persist a lease before publishing its request. The
            # exact owner/run tombstone remains valid; no request is fabricated.
            try:_,key=seal_request(R,job,run,store)
            except FileNotFoundError:key=None
        previous=store.read('cancel.json')
        if previous and previous['owner_job']!=job:raise ValueError('GPU cancellation owner differs')
        store.write('cancel.json',{'owner_job':job,'run_id':run,'request_sha256':key,'revoked':True})
        response=common(job,run,key)
        subprocess.run(['/usr/bin/systemctl','stop','--no-block',unit(run)],check=False,capture_output=True,timeout=3)
        if R.completion_service_active(unit(run)):return dict(response,state='stopping')
        final=store.read('receipt.json')
        if final and final.get('state') in TERMINAL and final.get('cleanup_confirmed') is True:
            return dict(response,state='stopping' if R.completion_service_active(unit(run,True)) else 'stopped')
        bound=store.read('binding.json')
        if bound:
            if not R.completion_service_active(unit(run,True)):
                directory=freeze_tools(R,store)
                command=['/usr/bin/systemd-run','--quiet','--collect','--unit='+unit(run,True),'--property=RuntimeMaxSec=120','--property=MemoryMax=256M','--property=CPUQuota=100%','--property=TasksMax=32','--property=KillMode=control-group',
                         '/usr/bin/python3','-I',str(directory/'autonomy-gpu-runtime.py'),'cleanup','--job',job,'--run',run]
                subprocess.run(command,check=True,capture_output=True,timeout=5,pass_fds=(guard,))
            return dict(response,state='stopping')
        if key:
            store.write('receipt.json',dict(response,state='cancelled',reason='cancelled before guest dispatch',executed=False,charged_ms=0,cleanup_confirmed=True))
        return dict(response,state='stopped',evidence_scope='bound_request' if key else 'revoked_missing_request')

def output(R,job,run,offset=0):
    receipt=status(R,job,run)
    if receipt.get('state') not in TERMINAL or receipt.get('executed') is not True:raise ValueError('GPU output is not retained yet')
    if type(offset) is not int or offset<0 or offset>16*1024**2:raise ValueError('GPU output offset bound')
    path=stage(R,run).root/'output.log';info=R.regular(path)
    if info.st_uid!=0 or info.st_mode&0o222 or info.st_size>16*1024**2:raise ValueError('unsafe retained GPU output')
    with path.open('rb') as source:source.seek(offset);data=source.read(65536)
    import base64
    return dict(common(job,run,receipt['request_sha256']),state='ready',output_sha256=receipt['output_sha256'],offset=offset,total_bytes=info.st_size,data_base64=base64.b64encode(data).decode())

def cleanup(R,job,run):
    store=stage(R,run);sealed=store.read('request.json');bound=store.read('binding.json')
    if sealed['value']['owner_job']!=job:raise ValueError('GPU owner differs')
    if bound:finish(store,job,run,sealed['key'],bound,GuestTransport(),L.HostFence(L.Store(L.HOST_STORE)))


def main():
    p=argparse.ArgumentParser();p.add_argument('command',choices=['execute','cleanup','snapshot-execute']);p.add_argument('--job',required=True);p.add_argument('--run',required=True);a=p.parse_args()
    if os.geteuid()!=0:raise SystemExit('root GPU supervisor required')
    runner=module(Path(__file__).with_name('autonomy-runner.py'),'gpu_runner')
    if a.command=='snapshot-execute':module(companion('autonomy-gpu-snapshot.py'),'gpu_snapshot').execute(runner,L,a.job,a.run)
    elif a.command=='execute':execute(runner,a.job,a.run)
    else:cleanup(runner,a.job,a.run)
if __name__=='__main__':main()
