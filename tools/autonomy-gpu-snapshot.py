#!/usr/bin/python3 -I
"""Bounded data-only capture of a running worker's source; no symlink traversal.
A capture describes bytes observed during its bracket, never an atomic filesystem
snapshot or a claim that the worker stopped modifying its workspace.
"""
import hashlib,json,os,stat,tarfile,time
from pathlib import Path
MAX_BYTES=512*1024**2
MAX_ENTRIES=50000

def canonical(value):return json.dumps(value,sort_keys=True,separators=(',',':')).encode()
def selection(paths):
    paths=['.'] if not paths else paths
    if not isinstance(paths,list) or len(paths)>64 or any(not isinstance(p,str) or not p or len(p)>1024 or p.startswith('/') or '\\' in p or any(c in p for c in '\0\n\r') or (p!='.' and any(v in ('','.','..') for v in p.split('/'))) for p in paths):raise ValueError('GPU source selection requires at most64 canonical relative paths')
    paths=sorted(paths)
    for i,p in enumerate(paths):
        if any(p==old or old=='.' or p.startswith(old+'/') for old in paths[:i]):raise ValueError('GPU source selections overlap')
    return paths

def scan(source,destination=None,source_paths=None,excluded=None):
    source_paths=selection(source_paths);rows=[];total=0;visited=0;found=set()
    def included(path):return source_paths==['.'] or any(path==p or path.startswith(p+'/') or p.startswith(path+'/') for p in source_paths)
    root=os.open(source,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW)
    def descend(fd,relative):
        nonlocal total,visited
        info=os.fstat(fd)
        if info.st_mode&0o7000:raise ValueError('GPU source special directory mode')
        rows.append({'path':relative or '.','type':'directory','mode':stat.S_IMODE(info.st_mode)})
        names=os.listdir(fd)
        visited+=len(names)
        if visited>MAX_ENTRIES:raise ValueError('GPU source inspected entry limit')
        for name in sorted(names):
            if name in ('.','..') or '/' in name or '\0' in name:raise ValueError('GPU source entry path')
            if len(rows)>=MAX_ENTRIES:raise ValueError('GPU source entry limit')
            rel=(relative+'/' if relative else '')+name
            if len(rel.encode())>1024:raise ValueError('GPU source path length bound')
            if not included(rel):
                if excluded is not None:excluded.append(rel)
                continue
            if rel in source_paths:found.add(rel)
            before=os.stat(name,dir_fd=fd,follow_symlinks=False)
            if before.st_mode&0o7000:raise ValueError('GPU source special mode')
            if stat.S_ISDIR(before.st_mode):
                child=os.open(name,os.O_RDONLY|os.O_DIRECTORY|os.O_NOFOLLOW,dir_fd=fd)
                try:
                    if destination is not None:(destination/rel).mkdir(mode=0o700)
                    descend(child,rel)
                finally:os.close(child)
            elif stat.S_ISREG(before.st_mode) and before.st_nlink==1:
                total+=before.st_size
                if total>MAX_BYTES:raise ValueError('GPU source byte limit')
                child=os.open(name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK,dir_fd=fd)
                output=None
                try:
                    opened=os.fstat(child)
                    if (opened.st_dev,opened.st_ino,opened.st_mode,opened.st_nlink)!=(before.st_dev,before.st_ino,before.st_mode,1):raise ValueError('GPU source changed while opening')
                    if destination is not None:output=(destination/rel).open('xb')
                    digest=hashlib.sha256();size=0
                    while True:
                        block=os.read(child,min(1024**2,MAX_BYTES-size+1))
                        if not block:break
                        size+=len(block)
                        if size>before.st_size:raise ValueError('GPU source grew during capture')
                        digest.update(block)
                        if output is not None:output.write(block)
                    after=os.fstat(child)
                    if size!=before.st_size or (opened.st_mtime_ns,opened.st_ctime_ns,opened.st_size)!=(after.st_mtime_ns,after.st_ctime_ns,after.st_size):raise ValueError('GPU source changed during capture')
                    rows.append({'path':rel,'type':'file','mode':stat.S_IMODE(before.st_mode),'bytes':size,'sha256':digest.hexdigest()})
                finally:
                    os.close(child)
                    if output is not None:output.flush();os.fsync(output.fileno());output.close()
            else:raise ValueError('GPU source links and special files unsupported at '+rel+'; select source_paths that exclude dependency/runtime trees explicitly')
    try:descend(root,'')
    finally:os.close(root)
    if source_paths!=['.'] and found!=set(source_paths):raise ValueError('selected GPU source path is missing')
    return rows

def capture(source,destination,source_paths=None):
    destination=Path(destination);destination.mkdir(mode=0o700)
    work=destination/'work';work.mkdir(mode=0o700)
    source_paths=selection(source_paths);excluded=[]
    started=time.time_ns();rows=scan(source,work,source_paths,excluded)
    if scan(source,source_paths=source_paths)!=rows:raise ValueError('GPU source changed across capture bracket; request a new snapshot')
    for row in reversed(rows):
        path=work if row['path']=='.' else work/row['path']
        path.chmod(row['mode'])
    archive=destination/'source.tar.gz'
    with tarfile.open(archive,'w:gz',format=tarfile.PAX_FORMAT) as output:
        output.add(work,arcname='work',recursive=True)
    if archive.stat().st_size>MAX_BYTES:raise ValueError('GPU compressed source bound')
    archive.chmod(0o400)
    with archive.open('rb') as source:os.fsync(source.fileno());digest=hashlib.file_digest(source,'sha256').hexdigest()
    manifest={'selected_paths':source_paths,'excluded_paths':excluded,'entries':rows,'limits':{'source_bytes':MAX_BYTES,'entries':MAX_ENTRIES}}
    manifest_raw=canonical(manifest)
    if len(manifest_raw)>32*1024**2:raise ValueError('GPU source manifest byte limit')
    manifest_path=destination/'manifest.json';manifest_path.write_bytes(manifest_raw);manifest_path.chmod(0o400)
    with manifest_path.open('rb') as stream:os.fsync(stream.fileno())
    result={'selected_paths':source_paths,'excluded_paths':excluded[:128],'excluded_path_count':len(excluded),'excluded_paths_truncated':len(excluded)>128,'manifest_sha256':hashlib.sha256(manifest_raw).hexdigest(),'limits':manifest['limits'],'state':'ready','source_archive_sha256':digest,'source_tree_sha256':hashlib.sha256(canonical(rows)).hexdigest(),'capture_started_unix_ns':started,'capture_finished_unix_ns':time.time_ns(),'scope':'Immutable captured bytes; matching source inventories before and after copying; not an atomic filesystem snapshot','entries':len(rows),'bytes':sum(r.get('bytes',0) for r in rows)}
    return result

# The async transport owns cancellation and source capture, independently of GPU
# lease admission. No physical lease is acquired and no guest command is sent.
def context(R,L,job,key):
    R.job_path(job)
    if not L.HEX.fullmatch(key):raise ValueError('invalid GPU snapshot ID')
    return L.Store(R.ROOT.parent/'gpu-snapshots'/key)

def snapshot_unit(key):return 'lectern-gpu-snapshot-'+key+'.service'

def admitted(R,L,job,key,store):
    old=store.read('request.json')
    if old:
        if old['owner_job']!=job:raise ValueError('GPU snapshot owner differs')
        return old
    path=R.job_path(job)/'gpu-snapshot-requests'/(key+'.json');info=R.regular(path)
    import pwd
    if info.st_uid not in (0,pwd.getpwnam('admin').pw_uid) or info.st_mode&0o077 or info.st_size>2*1024**2:raise ValueError('unsafe GPU snapshot request')
    raw=path.read_bytes();value=json.loads(raw)
    if set(value)!={'schema_version','owner_job','requested_at','input_sha256','source_paths'} or value['schema_version']!=1 or value['owner_job']!=job or not isinstance(value['requested_at'],str) or len(value['requested_at'])>64 or not L.HEX.fullmatch(value['input_sha256']) or hashlib.sha256(raw).hexdigest()!=key:raise ValueError('GPU snapshot request binding differs')
    if selection(value['source_paths'])!=value['source_paths']:raise ValueError('GPU source selection is not normalized')
    store.write('request.json',value);return value

def status(R,L,job,key):
    store=context(R,L,job,key);request=store.read('request.json')
    if request and request['owner_job']!=job:raise ValueError('GPU snapshot owner differs')
    receipt=store.read('receipt.json')
    if receipt:
        if receipt.get('owner_job')!=job or receipt.get('snapshot_id')!=key:raise ValueError('GPU snapshot receipt owner differs')
        return dict(receipt,receipt_sha256=L.digest(receipt))
    cancellation=store.read('cancel.json')
    if cancellation and cancellation['owner_job']!=job:raise ValueError('GPU snapshot owner differs')
    if request and store.read('started.json') and not R.completion_service_active(snapshot_unit(key)):
        receipt={'snapshot_id':key,'owner_job':job,'state':'cancelled' if cancellation else 'interrupted','executed':False,'reason':'source capture ended without immutable source receipt'}
        store.write('receipt.json',receipt);return dict(receipt,receipt_sha256=L.digest(receipt))
    return {'snapshot_id':key,'owner_job':job,'state':'stopping' if cancellation else 'preparing'}

def stop(R,L,job,key):
    import subprocess
    store=context(R,L,job,key)
    with store.guard():
        request=store.read('request.json');old=store.read('cancel.json')
        if (request and request['owner_job']!=job) or (old and old['owner_job']!=job):raise ValueError('GPU snapshot owner differs')
        store.write('cancel.json',{'owner_job':job})
        subprocess.run(['/usr/bin/systemctl','stop','--no-block',snapshot_unit(key)],capture_output=True,timeout=3)
        active=R.completion_service_active(snapshot_unit(key))
        if not active and not store.read('receipt.json'):store.write('receipt.json',{'snapshot_id':key,'owner_job':job,'state':'cancelled','executed':False})
        return {'snapshot_id':key,'owner_job':job,'state':'stopping' if active else 'stopped'}

def start(R,L,job,key,freeze):
    import subprocess
    store=context(R,L,job,key)
    with store.guard() as guard:
        admitted(R,L,job,key,store)
        if store.read('receipt.json') or store.read('cancel.json'):return status(R,L,job,key)
        if R.completion_service_active(snapshot_unit(key)):return status(R,L,job,key)
        if store.read('started.json'):
            store.write('receipt.json',{'snapshot_id':key,'owner_job':job,'state':'interrupted','executed':False,'reason':'capture supervisor ended without sealed source; request a fresh capture'})
            return status(R,L,job,key)
        directory=freeze(R,store)
        subprocess.run(['/usr/bin/systemd-run','--quiet','--collect','--unit='+snapshot_unit(key),'--property=RuntimeMaxSec=180','--property=TimeoutStopSec=5','--property=MemoryMax=1G','--property=CPUQuota=100%','--property=TasksMax=32','--property=KillMode=control-group','--property=UMask=0077','/usr/bin/python3','-I',str(directory/'autonomy-gpu-runtime.py'),'snapshot-execute','--job',job,'--run',key],check=True,capture_output=True,timeout=5,pass_fds=(guard,))
        return status(R,L,job,key)

def execute(R,L,job,key):
    import shutil,fcntl
    store=context(R,L,job,key)
    with store.guard():
        request=admitted(R,L,job,key,store)
        if store.read('receipt.json') or store.read('cancel.json'):return
        if store.read('started.json'):raise ValueError('GPU capture cannot replay')
        store.write('started.json',{'owner_job':job})
    try:
        with R.ARTIFACT_LOCK.open('a') as allocation:
            fcntl.flock(allocation,fcntl.LOCK_EX)
            capacity=R.storage_status()
            if not capacity['ready'] or capacity['free_bytes']<22*1024**3 or capacity['allocated_bytes']>R.STORAGE_LIMIT-2*1024**3:raise ValueError('GPU source snapshot needs2 GiB retention headroom above20 GiB floor')
            if store.read('cancel.json'):raise ValueError('GPU capture cancelled before allocation')
            result=capture(R.job_path(job)/'work',store.root/'captured',request['source_paths'])
        result.update(snapshot_id=key,owner_job=job,input_sha256=request['input_sha256'])
    except (OSError,ValueError) as exc:result={'snapshot_id':key,'owner_job':job,'state':'unavailable','reason':str(exc)[:1024]}
    with store.guard():
        if store.read('cancel.json'):result={'snapshot_id':key,'owner_job':job,'state':'cancelled','executed':False}
        store.write('receipt.json',result)

def manifest(R,L,job,key,offset=0):
    import base64
    receipt=status(R,L,job,key)
    if receipt.get('state')!='ready':raise ValueError('GPU source manifest is not ready')
    if type(offset) is not int or offset<0 or offset>32*1024**2:raise ValueError('GPU manifest offset bound')
    path=context(R,L,job,key).root/'captured/manifest.json';info=R.regular(path)
    if info.st_uid!=0 or info.st_mode&0o222 or info.st_size>32*1024**2:raise ValueError('unsafe GPU source manifest')
    with path.open('rb') as source:source.seek(offset);raw=source.read(65536)
    return {'snapshot_id':key,'owner_job':job,'state':'ready','manifest_sha256':receipt['manifest_sha256'],'offset':offset,'total_bytes':info.st_size,'data_base64':base64.b64encode(raw).decode()}
