#!/usr/bin/python3
"""Trusted bounded probe supervisor. Submitted code executes only in a fresh namespace.

The receipt attests mounted inputs and process observations, never semantic
correctness or that arbitrary submitted code actually loaded the mounted source.
"""
import base64
import ctypes
import datetime
import errno
import fcntl
import hashlib
import json
import os
from pathlib import Path
import pwd
import resource
import selectors
import shutil
import signal
import stat
import subprocess
import time
from types import SimpleNamespace

R=None
PROFILE={'ordinary180':{'seconds':180,'scratch_bytes':512*1024**2,'output_bytes':8*1024**2,'memory_bytes':2*1024**3,'cpu_percent':200,'tasks':128}}
TERMINAL={'exited','timeout','output_limit','cancelled','interrupted','unavailable'}
MAX_REQUEST=1024*1024

def canonical(value):return json.dumps(value,sort_keys=True,separators=(',',':')).encode()
def sha(data):return hashlib.sha256(data).hexdigest()
def now():return datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00','Z')
def hexkey(value):return isinstance(value,str) and len(value)==64 and all(c in '0123456789abcdef' for c in value)
def unique(pairs):
    result={}
    for key,value in pairs:
        if key in result:raise ValueError('duplicate probe JSON key')
        result[key]=value
    return result

def private(path):
    path.mkdir(mode=0o700,exist_ok=True)
    info=path.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid!=os.geteuid() or info.st_mode&0o077:raise ValueError('unsafe probe private directory')
    return path

def paths(job,probe):
    owner=R.job_path(job)
    if not hexkey(probe):raise ValueError('probe ID must be SHA256 hex')
    return owner,owner/'expert-probes'/probe

def unit(job,probe):paths(job,probe);return 'lectern-expert-probe-'+job+'-'+probe+'.service'

def read(path,limit=MAX_REQUEST):
    info=R.regular(path)
    if info.st_uid!=os.geteuid() or info.st_mode&0o022 or info.st_size>limit:raise ValueError('unsafe probe record')
    return path.read_bytes()

def write(path,value):
    R.completion_write(path,value)

def validate_request(job,probe,raw):
    if len(raw)>MAX_REQUEST:raise ValueError('probe request exceeds 1 MiB')
    request=json.loads(raw,object_pairs_hook=unique)
    fields={'schema_version','owner_job','owner_task','role','audit_revision','progress_key','root_task_id','source_job','source_task_id','source_archive_sha256','root_acceptance_sha256','source_acceptance_sha256','profile','script','fixtures','argv','runtime'}
    if set(request)!=fields or request['schema_version']!=1 or request['owner_job']!=job:raise ValueError('probe request shape or owner mismatch')
    if request['role'] not in ('auditor_a','auditor_b') or request['profile'] not in PROFILE:raise ValueError('unsupported probe role/profile')
    for name in ('owner_task','root_task_id','source_task_id'):
        if type(request[name]) is not int or request[name]<=0:raise ValueError('invalid probe task identity')
    if type(request['audit_revision']) is not int or request['audit_revision']<0:raise ValueError('invalid audit revision')
    for name in ('progress_key','source_archive_sha256','root_acceptance_sha256','source_acceptance_sha256'):
        if not hexkey(request[name]):raise ValueError('invalid probe binding')
    R.job_path(request['source_job'])
    if request['source_job']==job:raise ValueError('probe source must be retained independent archive')
    key=sha(raw)
    if sha((request['progress_key']+':'+str(request['owner_task'])+':'+key).encode())!=probe:raise ValueError('probe ID/request digest mismatch')
    script=request['script']
    if not isinstance(script,str) or len(script.encode())>128*1024 or not script.strip() or '\0' in script:raise ValueError('invalid bounded Python script')
    argv=request['argv']
    if not isinstance(argv,list) or len(argv)>32 or any(not isinstance(v,str) or '\0' in v or len(v.encode())>4096 for v in argv) or sum(len(v.encode()) for v in argv)>16384:raise ValueError('invalid probe argv')
    fixtures=request['fixtures'];seen={'main.py'};total=0
    if not isinstance(fixtures,list) or len(fixtures)>64:raise ValueError('invalid probe fixtures')
    for item in fixtures:
        if not isinstance(item,dict) or set(item)!={'path','content'}:raise ValueError('invalid fixture entry')
        name=item['path']
        if not isinstance(name,str) or len(name)>1024 or name.startswith('/') or '\\' in name or '\0' in name or any(v in ('','.','..') for v in name.split('/')) or name in seen:raise ValueError('unsafe fixture path')
        if any(name.startswith(prior+'/') or prior.startswith(name+'/') for prior in seen):raise ValueError('fixture file/directory collision')
        seen.add(name)
        data=base64.b64decode(item['content'],validate=True);total+=len(data)
        if total>512*1024:raise ValueError('fixtures exceed 512 KiB')
    runtime=request['runtime']
    if not isinstance(runtime,dict) or set(runtime)!={'python_bundle_key','python_input_key','browser_key','python_test_key'}:raise ValueError('invalid probe runtime envelope')
    if any(v!='' and not hexkey(v) for v in runtime.values()):raise ValueError('invalid probe runtime identity')
    if bool(runtime['python_bundle_key'])!=bool(runtime['python_input_key']) or (runtime['python_bundle_key'] and runtime['python_test_key']) or (runtime['browser_key'] and not runtime['python_bundle_key']):raise ValueError('conflicting probe runtime selection')
    return request,key

def request_for(job,probe):
    owner,stage=paths(job,probe)
    directory=owner/'expert-probe-requests';info=directory.lstat()
    if not stat.S_ISDIR(info.st_mode) or info.st_uid not in (0,pwd.getpwnam('admin').pw_uid) or info.st_mode&0o022:raise ValueError('unsafe probe request directory')
    path=directory/(probe+'.json');info=R.regular(path)
    if info.st_uid not in (0,pwd.getpwnam('admin').pw_uid) or info.st_mode&0o077 or info.st_size>MAX_REQUEST:raise ValueError('unsafe probe request file')
    raw=path.read_bytes();request,key=validate_request(job,probe,raw)
    if R.archive_identity(request['source_job'])['sha256']!=request['source_archive_sha256']:raise ValueError('probe source archive binding mismatch')
    private(owner/'expert-probes');private(stage)
    sealed=stage/'request.json'
    if sealed.exists():
        if read(sealed)!=raw:raise ValueError('probe request changed after sealing')
    else:
        fd=os.open(sealed,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o600)
        with os.fdopen(fd,'wb') as output:output.write(raw);output.flush();os.fsync(output.fileno())
    return stage,request,key

def base_receipt(job,probe,request,key):
    names=('progress_key','source_archive_sha256','root_acceptance_sha256','source_acceptance_sha256','owner_job','owner_task','role','audit_revision','source_job','source_task_id','root_task_id','profile')
    return dict({name:request[name] for name in names},schema_version=1,probe_id=probe,request_key=key,limits=PROFILE[request['profile']],projection_status='mounted_not_proven_loaded',executed=False)

def receipt(stage):
    path=stage/'receipt.json'
    if not path.exists():return None
    value=json.loads(read(path,256*1024))
    if value.get('state') in TERMINAL:
        body=dict(value);digest=body.pop('receipt_sha256',None)
        if digest!=sha(canonical(body)):raise ValueError('sealed probe receipt checksum mismatch')
    return value

def seal_receipt(stage,value):
    value=dict(value);value.pop('receipt_sha256',None);value['receipt_sha256']=sha(canonical(value));write(stage/'receipt.json',value)
    return value

def interrupted(stage,value,reason):
    value=dict(value,state='interrupted',reason=reason,ended_at=now(),exit_code=None,charged_ms=value['limits']['seconds']*1000,accounting='conservative_reserved_limit_after_unobserved_end')
    return seal_receipt(stage,value)

def freeze_launcher(stage):
    runner=stage/'runner.py';entry=stage/'entry.py'
    if not runner.exists():
        source=Path(R.__file__);info=R.regular(source)
        if info.st_uid!=os.geteuid() or info.st_mode&0o022 or info.st_size>512*1024:raise ValueError('unsafe probe runner source')
        temporary=stage/'runner.pending'
        if temporary.exists():temporary.unlink()
        with temporary.open('xb') as output:output.write(source.read_bytes());output.flush();os.fsync(output.fileno())
        temporary.chmod(0o400);os.replace(temporary,runner)
    read(runner,512*1024)
    if not entry.exists():
        # Freeze only trusted launcher configuration, never submitted host paths.
        config={name:str(getattr(R,name)) for name in ('ROOT','INSTALL','DEPENDENCIES','ARTIFACT_LOCK','EXPERT_PROBE_HELPER','PYTHON_HELPER','BROWSER_HELPER')}
        source="import importlib.util,sys\nfrom pathlib import Path\np=Path(__file__).parent\ns=importlib.util.spec_from_file_location('frozen_runner',p/'runner.py');r=importlib.util.module_from_spec(s);s.loader.exec_module(r)\n"
        for name,value in config.items():source+='r.'+name+'='+ (repr(value) if name=='INSTALL' else 'Path('+repr(value)+')')+'\n'
        source+='sys.exit(r.main())\n'
        temporary=stage/'entry.pending'
        if temporary.exists():temporary.unlink()
        with temporary.open('x') as output:output.write(source);output.flush();os.fsync(output.fileno())
        temporary.chmod(0o400);os.replace(temporary,entry)
    read(entry)
    return entry


def start(job,probe):
    stage,request,key=request_for(job,probe);value=base_receipt(job,probe,request,key)
    cancellation=(stage/'cancel.json').read_bytes() if (stage/'cancel.json').exists() else b''
    with (stage/'guard').open('a') as guard:
        try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:return dict(value,state='preparing')
        old=receipt(stage)
        if old and (old.get('request_key')!=key or old.get('probe_id')!=probe):raise ValueError('probe receipt binding changed')
        if old and old['state'] in TERMINAL:return old
        if cancellation:
            if old and old.get('started_at'):
                if R.completion_service_active(unit(job,probe)):return dict(old,reason='probe cancellation in progress')
                old=receipt(stage) or old
                if old['state'] in TERMINAL:return old
                return interrupted(stage,old,'cancelled execution ended without final observation')
            return seal_receipt(stage,dict(value,state='cancelled',reason='probe reservation cancelled before execution',charged_ms=0))
        if cancellation!=((stage/'cancel.json').read_bytes() if (stage/'cancel.json').exists() else b''):return dict(value,state='waiting',reason='probe start cancelled')
        if R.completion_service_active(unit(job,probe)):return old or dict(value,state='preparing')
        old=receipt(stage)
        if old and old['state'] in TERMINAL:return old
        if old and old.get('started_at'):return interrupted(stage,old,'probe process ended without final observation; execution will not be silently repeated')
        if old and old.get('retry_at',0)>time.time():return old
        if R.status(job)['state']!='running':return dict(value,state='waiting',reason='admitted auditor is not running')
        if shutil.disk_usage(R.ROOT).free<24*1024**3:return dict(value,state='waiting',reason='probe requires storage headroom')
        helper=R.python_freeze_helper(stage,R.EXPERT_PROBE_HELPER)
        launcher=freeze_launcher(stage)
        value.update(state='preparing');write(stage/'receipt.json',value)
        profile=PROFILE[request['profile']]
        command=['/usr/bin/systemd-run','--quiet','--collect','--unit='+unit(job,probe),
                 '--property=RuntimeMaxSec='+str(profile['seconds']+120),'--property=MemoryMax='+str(profile['memory_bytes']),
                 '--property=MemorySwapMax=0','--property=CPUQuota=200%','--property=TasksMax=128',
                 '--property=KillMode=control-group','--property=TimeoutStopSec=10','--property=PrivateMounts=yes',
                 '--property=NoNewPrivileges=yes','--property=ProtectControlGroups=yes','--property=UMask=0077',
                 '--property=LimitFSIZE='+str(2*1024**3),'/usr/bin/python3','-I','-S',str(launcher),'_expert-probe','--job',job,'--probe-id',probe]
        R.run(command,pass_fds=(guard.fileno(),));return value

def status(job,probe):
    stage,request,key=request_for(job,probe)
    value=receipt(stage) or dict(base_receipt(job,probe,request,key),state='waiting')
    active=R.completion_service_active(unit(job,probe))
    if value['state'] in TERMINAL:return value
    if not active:
        value=receipt(stage) or value
        if value['state'] in TERMINAL:return value
    if not active and value.get('started_at'):return interrupted(stage,value,'execution ended without finalized receipt')
    if not active and value['state']=='preparing':value=dict(value,state='waiting',reason='preparation interrupted; no execution observed')
    return dict(value,unit_active=active)


def stop(job,probe):
    owner,stage=paths(job,probe)
    private(owner/'expert-probes');private(stage)
    write(stage/'cancel.json',{'at':now()})
    with (stage/'guard').open('a') as guard:
        try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
        except BlockingIOError:return {'state':'stopping','probe_id':probe}
        if R.completion_service_active(unit(job,probe)):
            R.run(['/usr/bin/systemctl','stop','--no-block',unit(job,probe)],pass_fds=(guard.fileno(),),timeout=3)
        if R.completion_service_active(unit(job,probe)):return {'state':'stopping','probe_id':probe}
        old=receipt(stage)
        if old and old['state'] not in TERMINAL:
            if old.get('started_at'):interrupted(stage,old,'probe cancelled before supervisor finalized its observation')
            else:seal_receipt(stage,dict(old,state='cancelled',executed=False,reason='probe preparation stopped',charged_ms=0))
    return {'state':'stopped','probe_id':probe}

def runtime_for(job,request):
    desired=request['runtime'];bundle=browser=None
    if desired['python_bundle_key']:
        bundle=R.python_project_bundle(job)
        if bundle is None:raise ValueError('requested project runtime not assigned to auditor')
        manifest=json.loads((bundle/'manifest.json').read_text())
        if manifest['key']!=desired['python_bundle_key'] or manifest['input_key']!=desired['python_input_key'] or manifest.get('browser_key','')!=desired['browser_key']:raise ValueError('probe project runtime mismatch')
        if desired['browser_key']:browser=R.browser_runtime(desired['browser_key'],manifest['packages']['playwright'])
    elif desired['python_test_key']:
        bundle=R.python_test_bundle(desired['python_test_key'])
    identity=dict(desired,python_sha256=R.digest_file(Path('/usr/bin/python3').resolve()),python_version=R.platform.python_version())
    return bundle,browser,sha(canonical(identity))

def prepare(stage,request):
    source=stage/'source';inputs=stage/'inputs'
    with R.ARTIFACT_LOCK.open('a') as guard:
        fcntl.flock(guard,fcntl.LOCK_EX)
        R.completion_capacity()
        if source.exists():shutil.rmtree(source)
        if inputs.exists():shutil.rmtree(inputs)
        if R.evidence_extract_archive(request['source_job'],source)!=request['source_archive_sha256']:raise ValueError('extracted probe source differs from admitted archive')
        _,tree,_=R.evidence_tree_identity(source,scheme='general-evidence-v1')
        for path in (source,*source.rglob('*')):os.chown(path,R.UID,R.GID,follow_symlinks=False)
        inputs.mkdir(mode=0o755);(inputs/'main.py').write_text(request['script']);(inputs/'main.py').chmod(0o444)
        rows=[]
        for fixture in request['fixtures']:
            path=inputs/fixture['path'];path.parent.mkdir(parents=True,exist_ok=True);data=base64.b64decode(fixture['content'],validate=True);path.write_bytes(data);path.chmod(0o444)
            rows.append({'path':fixture['path'],'sha256':sha(data),'bytes':len(data)})
        for path in inputs.rglob('*'):
            if path.is_dir():path.chmod(0o555)
        inputs.chmod(0o555)
    return tree,sha(request['script'].encode()),sha(canonical(sorted(rows,key=lambda item:item['path']))),sha(canonical(request['argv']))

def sandbox_command(request,bundle=False,browser=False):
    cmd=['/usr/bin/bwrap','--die-with-parent','--new-session','--unshare-all','--cap-drop','ALL','--clearenv','--tmpfs','/','--ro-bind','/usr','/usr']
    for path in ('/lib','/lib64','/bin'):
        if Path(path).exists():cmd+=['--ro-bind',path,path]
    cmd+=['--proc','/proc','--dev','/dev','--bind','/tmp/expert-scratch/tmp','/dev/shm','--dir','/etc','--dir','/home',
          '--ro-bind','/tmp/expert-source','/source','--ro-bind','/tmp/expert-inputs','/probe',
          '--bind','/tmp/expert-scratch','/scratch','--symlink','/scratch/tmp','/tmp','--symlink','/scratch/home','/home/agent',
          '--symlink','/source','/work','--chdir','/scratch','--setenv','HOME','/home/agent','--setenv','PATH','/usr/bin:/bin',
          '--setenv','TMPDIR','/tmp','--setenv','PYTHONPATH','/source','--setenv','PYTHONDONTWRITEBYTECODE','1',
          '--setenv','LANG','C.UTF-8']
    if bundle:cmd+=R.python_project_mount(Path('/tmp/expert-runtime'))
    if browser:cmd+=R.browser_runtime_mount(Path('/tmp/expert-browser'))
    # Normal site initialization is deliberately AFTER isolation, matching the
    # verified runtime. Source precedes site; absolute entrypoint is frozen.
    return cmd+['--remount-ro','/','--','/usr/bin/python3','/probe/main.py',*request['argv']]

def mount_inputs(stage,request,bundle,browser):
    if ctypes.CDLL(None,use_errno=True).unshare(0x00020000)!=0:raise OSError(ctypes.get_errno(),'probe mount namespace unavailable')
    R.run(['/usr/bin/mount','--make-rprivate','/'])
    R.run(['/usr/bin/mount','-t','tmpfs','-o','mode=0755,size=16m,nosuid,nodev','expert-stage','/tmp'])
    sources={'expert-source':stage/'source','expert-inputs':stage/'inputs'}
    if bundle:sources['expert-runtime']=bundle
    if browser:sources['expert-browser']=browser
    for name,source in sources.items():
        target=Path('/tmp')/name;target.mkdir();R.run(['/usr/bin/mount','--bind',str(source),str(target)])
    scratch=Path('/tmp/expert-scratch');scratch.mkdir()
    R.run(['/usr/bin/mount','-t','tmpfs','-o','mode=0700,size='+str(PROFILE[request['profile']]['scratch_bytes'])+',nosuid,nodev','expert-scratch',str(scratch)])
    os.chown(scratch,R.UID,R.GID)
    for name in ('tmp','home'):
        directory=scratch/name;directory.mkdir(mode=0o700);os.chown(directory,R.UID,R.GID)

def members(job,probe):
    path=Path('/sys/fs/cgroup/system.slice')/unit(job,probe)/'cgroup.procs'
    return [int(pid) for pid in path.read_text().split()]

def kill_children(job,probe):
    # Fixed unit cgroup only. Namespace PID1 termination also kills detached
    # grandchildren; no submitted PID or process name influences this operation.
    for _ in range(3):
        others=[pid for pid in members(job,probe) if pid!=os.getpid()]
        if not others:return
        for pid in others:
            try:os.kill(pid,signal.SIGKILL)
            except ProcessLookupError:pass
        time.sleep(.02)

def fresh_owner(job):
    try:return 0<=time.time()-R.regular(R.job_path(job)/'heartbeat').st_mtime<60
    except OSError:return False

def capture(job,probe,stage,request,bundle,browser,value):
    limits=PROFILE[request['profile']];cancelled=[False]
    previous=signal.signal(signal.SIGTERM,lambda *_:cancelled.__setitem__(0,True))
    def drop():
        resource.setrlimit(resource.RLIMIT_FSIZE,(limits['scratch_bytes'],limits['scratch_bytes']))
        os.setgroups([]);os.setgid(R.GID);os.setuid(R.UID)
    if (stage/'cancel.json').exists():return seal_receipt(stage,dict(value,state='cancelled',reason='cancelled before process launch',executed=False,charged_ms=0))
    value=dict(value,state='running',executed=True,started_at=now());write(stage/'receipt.json',value)
    started=time.monotonic();process=None;streams={};state='exited';reason='process exited';total=0;truncated=False
    try:
        process=subprocess.Popen(sandbox_command(request,bundle is not None,browser is not None),stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,preexec_fn=drop,close_fds=True,start_new_session=True)
        selector=selectors.DefaultSelector()
        for name,pipe in (('stdout',process.stdout),('stderr',process.stderr)):
            log=(stage/(name+'.log')).open('wb');os.set_blocking(pipe.fileno(),False);selector.register(pipe,selectors.EVENT_READ,name)
            streams[name]={'file':log,'hash':hashlib.sha256(),'bytes':0,'head':b'','tail':b''}
        ended_process=None
        while selector.get_map() or process.poll() is None:
            elapsed=time.monotonic()-started
            if state=='exited':
                if cancelled[0] or (stage/'cancel.json').exists() or not fresh_owner(job):state='cancelled';reason='controller stopped probe or owner heartbeat expired'
                elif elapsed>=limits['seconds']:state='timeout';reason='probe execution time limit reached'
                if state!='exited':kill_children(job,probe)
            for key,_ in selector.select(.05):
                data=os.read(key.fileobj.fileno(),65536)
                if not data:selector.unregister(key.fileobj);key.fileobj.close();continue
                remaining=max(0,limits['output_bytes']-total);retained=data[:remaining];total+=len(retained)
                row=streams[key.data];row['file'].write(retained);row['hash'].update(retained);row['bytes']+=len(retained)
                row['head']=(row['head']+retained)[:8192];row['tail']=(row['tail']+retained)[-8192:]
                if len(retained)!=len(data) and state=='exited':state='output_limit';reason='combined stdout/stderr retention limit exceeded';truncated=True;kill_children(job,probe)
            if process.poll() is not None:
                ended_process=ended_process or time.monotonic()
                if time.monotonic()-ended_process>2:kill_children(job,probe);break
            if elapsed>limits['seconds']+5:kill_children(job,probe);break
        process.wait(timeout=3)
        if state=='exited':
            if cancelled[0] or (stage/'cancel.json').exists():state='cancelled';reason='controller stopped probe'
            elif process.returncode<0:state='interrupted';reason='probe process terminated by signal'
        kill_children(job,probe)
        result={}
        for name,row in streams.items():
            row['file'].flush();os.fsync(row['file'].fileno());row['file'].close();(stage/(name+'.log')).chmod(0o400)
            preview=row['head'] if row['bytes']<=8192 else row['head']+b'\n[...bounded preview...]\n'+row['tail']
            result[name]={'sha256':row['hash'].hexdigest(),'bytes':row['bytes'],'preview':preview.decode('utf-8',errors='replace'),'preview_truncated':row['bytes']>16384}
        observed_ms=min(int((time.monotonic()-started)*1000),limits['seconds']*1000)
        value.update(state=state,reason=reason,ended_at=now(),exit_code=process.returncode,charged_ms=observed_ms,truncated=truncated,**result)
        value['output_manifest_sha256']=sha(canonical({name:{k:row[k] for k in ('sha256','bytes')} for name,row in result.items()}))
        return seal_receipt(stage,value)
    finally:
        if process is not None:
            if process.poll() is None:kill_children(job,probe)
            for row in streams.values():
                if not row['file'].closed:row['file'].close()
        signal.signal(signal.SIGTERM,previous)

def execute(job,probe):
    current=Path('/proc/self/cgroup').read_text().strip().split('::')[-1]
    if not current.endswith('/'+unit(job,probe)):raise ValueError('probe outside matching service refused')
    stage,request,key=request_for(job,probe);value=base_receipt(job,probe,request,key)
    old=receipt(stage)
    if old and old['state'] in TERMINAL:return 0
    if old and old.get('started_at'):interrupted(stage,old,'previous execution cannot be replayed');return 1
    try:
        if not fresh_owner(job):raise RuntimeError('auditor heartbeat is not fresh')
        tree,script,fixtures,argv=prepare(stage,request)
        bundle,browser,runtime=runtime_for(job,request)
        policy=sha(canonical({'schema_version':1,'helper_sha256':R.digest_file(stage/'helper.py'),'runner_sha256':R.digest_file(Path(R.__file__)),'launcher_sha256':R.digest_file(stage/'entry.py'),'profile':PROFILE[request['profile']],'entrypoint':'python3 /probe/main.py','source_mount':'readonly /source; /work alias','network':'unshared-no-socket'}))
        value.update(source_tree_sha256=tree,script_sha256=script,fixtures_sha256=fixtures,argv_sha256=argv,runtime_digest=runtime,launch_policy_sha256=policy,runtime=request['runtime'])
        mount_inputs(stage,request,bundle,browser)
        result=capture(job,probe,stage,request,bundle,browser,value)
        return 0 if result['state']=='exited' else 1
    except (ValueError,FileNotFoundError,R.PythonUnsupported) as exc:
        previous=receipt(stage)
        if previous and previous.get('started_at'):
            interrupted(stage,previous,'execution observation unavailable: '+str(exc)[:200]);return 1
        value.update(state='unavailable',reason=str(exc)[:400],charged_ms=0)
        seal_receipt(stage,value);return 1
    except Exception as exc:
        old=receipt(stage)
        if old and old.get('started_at'):interrupted(stage,old,'execution observation interrupted: '+str(exc)[:200])
        else:write(stage/'receipt.json',dict(value,state='waiting',reason=str(exc)[:400],retry_at=time.time()+60))
        return 1

def output(job,probe,stream,offset):
    if stream not in ('stdout','stderr') or type(offset) is not int or offset<0:raise ValueError('invalid probe output selection')
    stage,request,key=request_for(job,probe);value=receipt(stage)
    if not value or value['state'] not in TERMINAL or stream not in value:raise ValueError('probe output is not finalized')
    path=stage/(stream+'.log');info=R.regular(path)
    if info.st_uid!=os.geteuid() or info.st_mode&0o222 or info.st_size!=value[stream]['bytes']:raise ValueError('unsafe probe output log')
    with path.open('rb') as log:log.seek(offset);data=log.read(32768)
    return {'probe_id':probe,'stream':stream,'offset':offset,'total':info.st_size,'sha256':value[stream]['sha256'],'data_base64':base64.b64encode(data).decode()}

def dispatch(runtime,command,job,probe,stream=None,offset=0):
    global R
    R=SimpleNamespace(**runtime)
    if command=='expert-probe':return start(job,probe)
    if command=='expert-probe-status':return status(job,probe)
    if command=='expert-probe-stop':return stop(job,probe)
    if command=='expert-probe-output':return output(job,probe,stream,offset)
    if command=='_expert-probe':return execute(job,probe)
    raise ValueError('unsupported expert probe command')
