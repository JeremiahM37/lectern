#!/usr/bin/python3 -I
"""Fixed isolated guest executor. Inputs are prepared by trusted root transport.
No live device access occurs during preparation or runtime inventory.
"""
import argparse
import ctypes
import hashlib
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import selectors
import shutil
import signal
import stat
import subprocess
import tarfile
import time

lease_helper=Path(__file__).with_name('autonomy-gpu-lease.py')
if not lease_helper.exists():lease_helper=Path(__file__).with_name('lectern-autonomy-gpu-lease.py')
s=importlib.util.spec_from_file_location('gpu_lease',lease_helper)
L=importlib.util.module_from_spec(s);s.loader.exec_module(L)
ROOT=Path('/var/lib/lectern-gpu-lease')
RUNTIMES=Path('/var/lib/lectern-gpu-runtimes')
GPU_DISCOVERY=('/sys/devices/virtual/kfd/kfd/topology',
               '/sys/devices/system/cpu', '/sys/devices/system/node')
GPU_PCI='/sys/devices/pci0000:00/0000:00:08.1/0000:f4:00.0'
PROFILES={'gpu-screen600':{'seconds':600,'memory':8*1024**3,'cpu':400,'tasks':256,'scratch':2*1024**3,'output':16*1024**2,'devices':True},
          'device-free30':{'seconds':30,'memory':512*1024**2,'cpu':100,'tasks':64,'scratch':64*1024**2,'output':1024**2,'devices':False}}
MAX_SOURCE=512*1024**2

def sha(path):
    h=hashlib.sha256()
    with Path(path).open('rb') as f:
        for chunk in iter(lambda:f.read(1024**2),b''):h.update(chunk)
    return h.hexdigest()

def regular(path,owner=0):
    st=Path(path).lstat()
    if not stat.S_ISREG(st.st_mode) or st.st_nlink!=1 or st.st_uid!=owner or st.st_mode&0o022:
        raise ValueError('unsafe sealed input')
    return st

def internal_target(root,path):
    """Resolve component by component without ever stepping outside root."""
    root=Path(root);pending=list(Path(path).relative_to(root).parts);resolved=[];links=0
    while pending:
        part=pending.pop(0)
        if part in ('','.'):continue
        if part=='..':
            if not resolved:raise ValueError('runtime link escapes root')
            resolved.pop();continue
        current=root.joinpath(*resolved,part)
        try:info=current.lstat()
        except (FileNotFoundError,NotADirectoryError) as exc:raise ValueError('dangling runtime link') from exc
        if stat.S_ISLNK(info.st_mode):
            target=os.readlink(current);links+=1
            if not target or target.startswith('/') or links>64:raise ValueError('absolute or cyclic runtime link')
            pending=list(PurePosixPath(target).parts)+pending
        else:
            if pending and not stat.S_ISDIR(info.st_mode):raise ValueError('runtime link crosses non-directory')
            resolved.append(part)
    final=root.joinpath(*resolved)
    if not (final.is_dir() or final.is_file()):raise ValueError('runtime link target is not regular data')
    return final


def runtime_paths(root):
    """No directory-link traversal; reject cycles in the complete directory graph."""
    root=Path(root)
    if not stat.S_ISDIR(root.lstat().st_mode):raise ValueError('runtime root is not a directory')
    paths=[root];queue=[root]
    while queue:
        directory=queue.pop()
        for p in sorted(directory.iterdir()):
            info=p.lstat();paths.append(p)
            if len(paths)>200000:raise ValueError('runtime inventory count limit')
            if stat.S_ISDIR(info.st_mode):queue.append(p)
    edges={p:[] for p in paths if stat.S_ISDIR(p.lstat().st_mode)}
    for p in paths[1:]:
        info=p.lstat()
        if stat.S_ISDIR(info.st_mode):edges[p.parent].append(p)
        elif stat.S_ISLNK(info.st_mode):
            target=internal_target(root,p)
            if target in edges:edges[p.parent].append(target)
    # Iterative DFS avoids Python recursion limits on deep administrator trees.
    colors={}
    for first in edges:
        stack=[(first,False)]
        while stack:
            node,leaving=stack.pop()
            if leaving:colors[node]=2;continue
            if colors.get(node)==1:raise ValueError('cyclic runtime directory links')
            if colors.get(node)==2:continue
            colors[node]=1;stack.append((node,True))
            stack.extend((child,False) for child in reversed(edges[node]))
    return sorted(paths)

def inventory(root,owner=0):
    root=Path(root);rows=[];total=0
    for path in runtime_paths(root):
        st=path.lstat();relative=path.relative_to(root).as_posix()
        if st.st_uid!=owner:raise ValueError('runtime must be trusted')
        if stat.S_ISLNK(st.st_mode):
            rows.append({'path':relative,'kind':'symlink','target':os.readlink(path)});continue
        if st.st_mode&0o222:raise ValueError('runtime must be immutable')
        if stat.S_ISDIR(st.st_mode):
            if stat.S_IMODE(st.st_mode)!=0o555:raise ValueError('immutable directory mode differs')
            rows.append({'path':relative,'kind':'directory','mode':0o555});continue
        regular(path,owner);total+=st.st_size
        if total>24*1024**3 or len(rows)>=200000:raise ValueError('runtime inventory limit')
        rows.append({'path':relative,'sha256':sha(path),'size':st.st_size,'mode':stat.S_IMODE(st.st_mode)})
    return rows

def sync_tree(root):
    root=Path(root)
    paths=runtime_paths(root)
    for p in paths:
        if stat.S_ISREG(p.lstat().st_mode):
            with p.open('rb') as f:os.fsync(f.fileno())
    directories=[p for p in paths if stat.S_ISDIR(p.lstat().st_mode)]
    for p in reversed(directories):
        fd=os.open(p,os.O_DIRECTORY|os.O_NOFOLLOW)
        try:os.fsync(fd)
        finally:os.close(fd)


def register_runtime(source, destination=RUNTIMES, owner=0):
    if os.geteuid()!=owner:raise ValueError('administrator runtime registration required')
    source=Path(source);destination=Path(destination)
    destination.mkdir(mode=0o755,parents=True,exist_ok=True)
    if destination.is_symlink() or destination.stat().st_uid!=owner or destination.stat().st_mode&0o022:raise ValueError('unsafe runtime registry')
    # Source is an administrator-curated rootfs, never a worker archive. No code
    # is executed to identify it. Validate link graph before preserving link text.
    for p in runtime_paths(source):
        st=p.lstat()
        if not (stat.S_ISDIR(st.st_mode) or stat.S_ISREG(st.st_mode) or stat.S_ISLNK(st.st_mode)) or (stat.S_ISREG(st.st_mode) and st.st_nlink!=1):raise ValueError('unsupported runtime source object')
    pending=destination/('.pending-'+L.uuid.uuid4().hex);pending.mkdir(mode=0o700)
    try:
        shutil.copytree(source,pending/'rootfs',symlinks=True)
        # Bind destinations must exist before the rootfs becomes immutable.
        # Their directories are part of the content-addressed inventory.
        for path in GPU_DISCOVERY:
            (pending/'rootfs'/path.lstrip('/')).mkdir(parents=True,exist_ok=True)
        freeze(pending/'rootfs',runtime_links=True)
        rows=inventory(pending/'rootfs',owner)
        data={'schema_version':1,'kind':'gpu-runtime-rootfs-v1','files':rows}
        key=L.digest(data);data['key']=key
        (pending/'manifest.json').write_bytes(L.canonical(data));(pending/'manifest.json').chmod(0o444)
        pending.chmod(0o555)
        final=destination/key
        if final.exists():runtime(key,destination,owner);shutil.rmtree(pending)
        else:
            sync_tree(pending);os.rename(pending,final)
            fd=os.open(destination,os.O_DIRECTORY|os.O_NOFOLLOW)
            try:os.fsync(fd)
            finally:os.close(fd)
        return data
    except BaseException:
        shutil.rmtree(pending,ignore_errors=True);raise


def runtime(key,root=RUNTIMES,owner=0):
    if not L.HEX.fullmatch(key):raise ValueError('invalid runtime key')
    root=Path(root)
    for directory in (root.parent,root,root/key,root/key/'rootfs'):
        info=directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid!=owner or info.st_mode&0o022:raise ValueError('unsafe runtime registry directory')
    base=root/key;manifest=base/'manifest.json';regular(manifest,owner)
    if manifest.stat().st_mode&0o222:raise ValueError('runtime manifest is writable')
    if manifest.stat().st_size>32*1024**2:raise ValueError('manifest bound')
    data=json.loads(manifest.read_bytes());claimed=data.pop('key',None)
    if claimed!=key or L.digest(data)!=key or data.get('kind')!='gpu-runtime-rootfs-v1':raise ValueError('runtime identity differs')
    if inventory(base/'rootfs',owner)!=data['files']:raise ValueError('runtime content differs')
    interpreter=regular(internal_target(base/'rootfs',base/'rootfs/usr/bin/python3'),owner)
    if interpreter.st_mode&0o111!=0o111:raise ValueError('runtime interpreter is not executable')
    return base/'rootfs',dict(data,key=key)

def freeze(root,runtime_links=False):
    for path in runtime_paths(root):
        if path.is_symlink():
            if not runtime_links:raise ValueError('source links unsupported')
            os.lchown(path,os.geteuid(),os.getegid());continue
        st=path.stat();path.chmod(0o555 if path.is_dir() or st.st_mode&0o111 else 0o444)

def extract(archive,dest):
    dest=Path(dest);dest.mkdir(mode=0o700);seen=set();count=total=0
    with tarfile.open(archive,'r|gz') as tf:
        for member in tf:
            name=member.name.removeprefix('./').rstrip('/')
            if name in ('','work'):continue
            p=PurePosixPath(name)
            if p.is_absolute() or '..' in p.parts or '\\' in name or not p.parts or p.parts[0]!='work':raise ValueError('invalid source archive path')
            rel=PurePosixPath(*p.parts[1:])
            if str(rel) in seen:raise ValueError('duplicate source member')
            seen.add(str(rel));count+=1;total+=member.size
            if count>50000 or total>MAX_SOURCE or member.size<0:raise ValueError('source extraction limit')
            out=dest/str(rel)
            if not member.isdir() and not member.isfile():raise ValueError('source links/special members unsupported')
            out.parent.mkdir(parents=True,exist_ok=True)
            if member.isdir():out.mkdir(exist_ok=True)
            else:
                with tf.extractfile(member) as src,out.open('xb') as target:shutil.copyfileobj(src,target,1024**2)
                out.chmod(0o555 if member.mode&0o111 else 0o444)
    freeze(dest)

def prepare(request,archive,root=ROOT,runtimes=RUNTIMES,owner=0):
    required={'binding','source_archive_sha256','runtime_key','profile','script','argv','helpers'}
    if set(request)!=required:raise ValueError('invalid execution envelope')
    bound=L.binding(request['binding'])
    if request['profile'] not in PROFILES:raise ValueError('unsupported finite profile')
    script=request['script'];argv=request['argv']
    if not isinstance(script,str) or not script.strip() or len(script.encode())>128*1024 or '\0' in script:raise ValueError('invalid Python entrypoint')
    if not isinstance(argv,list) or len(argv)>32 or any(not isinstance(a,str) or '\0' in a or len(a.encode())>4096 for a in argv):raise ValueError('invalid bounded argv')
    unsigned={k:v for k,v in request.items() if k!='binding'}
    if L.digest(unsigned)!=bound['request_sha256']:raise ValueError('execution request binding differs')
    regular(archive,owner)
    if Path(archive).stat().st_size>MAX_SOURCE or sha(archive)!=request['source_archive_sha256']:raise ValueError('source archive differs')
    runtime(request['runtime_key'],runtimes,owner)
    run=Path(root)/'runs'/bound['run_id']
    if run.exists():
        record=L.Store(run,owner).read('request.json')
        if record!=request:raise ValueError('existing run preparation differs')
        if L.helper_identity(run/'helpers',owner)!=request['helpers']:
            raise ValueError('prepared GPU helper identity differs')
        return run
    if shutil.disk_usage(Path(root).parent).free<L.STORAGE_FLOOR:
        raise ValueError('insufficient guest research storage headroom')
    selected_helpers=L.helper_sources()
    if request['helpers']!={name+'_sha256':hashlib.sha256(raw).hexdigest() for name,raw in selected_helpers.items()}:
        raise ValueError('qualified GPU helper identity differs')
    run.parent.mkdir(mode=0o700,parents=True,exist_ok=True)
    pending=run.with_name('.pending-'+bound['run_id']);pending.mkdir(mode=0o700)
    try:
        helpers=pending/'helpers';helpers.mkdir(mode=0o700)
        for name,raw in selected_helpers.items():
            helper=helpers/('lectern-autonomy-gpu-'+name+'.py')
            helper.write_bytes(raw);helper.chmod(0o555)
        helpers.chmod(0o555)
        extract(archive,pending/'source');(pending/'main.py').write_text(script);(pending/'main.py').chmod(0o444)
        store=L.Store(pending,owner);store.write('request.json',request)
        source_rows=inventory(pending/'source',owner)
        store.write('source.json',{'archive_sha256':request['source_archive_sha256'],'tree_sha256':L.digest(source_rows),'files':source_rows})
        (pending/'scratch').mkdir(mode=0o700)
        if sha(archive)!=request['source_archive_sha256']:raise ValueError('source changed during preparation')
        sync_tree(pending)
        os.rename(pending,run);store=L.Store(run.parent,owner);store.sync()
    except BaseException:
        shutil.rmtree(pending,ignore_errors=True);raise
    return run

def sandbox(run,request,rootfs,scratch):
    profile=PROFILES[request['profile']]
    command=['/usr/bin/bwrap','--die-with-parent','--new-session','--unshare-all','--cap-drop','ALL','--uid','65534','--gid','65534',
             '--ro-bind',str(rootfs),'/','--proc','/proc','--dev','/dev','--bind',str(scratch),'/tmp',
             '--ro-bind',str(run/'source'),'/source','--ro-bind',str(run/'main.py'),'/entry.py',
             '--bind',str(scratch),'/scratch','--clearenv','--setenv','HOME','/scratch','--setenv','TMPDIR','/scratch',
             '--setenv','PATH','/usr/bin:/bin','--setenv','PYTHONNOUSERSITE','1','--setenv','PYTHONDONTWRITEBYTECODE','1',
             '--setenv','TRITON_CACHE_DIR','/scratch/triton','--setenv','XDG_CACHE_HOME','/scratch/cache','--chdir','/scratch']
    if profile['devices']:
        # ROCr discovers agents and CPU/NUMA topology through sysfs before
        # opening the render device. Expose only these read-only discovery
        # trees; KFD process state and writable driver controls stay absent.
        # Build a minimal sysfs view on the registered empty /sys mountpoint.
        # libdrm needs canonical PCI/render links as well as ROCr topology.
        command+=['--tmpfs','/sys']
        for path in GPU_DISCOVERY:
            if not (rootfs/path.lstrip('/')).is_dir():
                raise ValueError('registered runtime lacks GPU discovery mountpoints')
            command+=['--ro-bind',path,path]
        render=GPU_PCI+'/drm/renderD128'
        if os.path.realpath('/sys/dev/char/226:128')!=render:
            raise ValueError('registered render PCI mapping differs')
        for directory in ('/sys/bus/pci','/sys/dev/char','/sys/class/drm',render):
            command+=['--dir',directory]
        for leaf in ('vendor','device','revision','subsystem_vendor','subsystem_device','uevent'):
            path=GPU_PCI+'/'+leaf;command+=['--ro-bind',path,path]
        for leaf in ('dev','uevent'):
            path=render+'/'+leaf;command+=['--ro-bind',path,path]
        for target,path in (('../..',render+'/device'),('/sys/bus/pci',GPU_PCI+'/subsystem'),
                            (render,'/sys/dev/char/226:128'),(render,'/sys/class/drm/renderD128')):
            command+=['--symlink',target,path]
        command+=['--remount-ro','/sys']
        for path,major,minor in [('/dev/kfd',234,0),('/dev/dri/renderD128',226,128)]:
            info=os.stat(path)
            if not stat.S_ISCHR(info.st_mode) or os.major(info.st_rdev)!=major or os.minor(info.st_rdev)!=minor:raise ValueError('registered device identity differs')
            command+=['--dev-bind',path,path]
    return command+['/usr/bin/python3','-I','/entry.py',*request['argv']]

def kill(child):
    try:os.killpg(child.pid,signal.SIGKILL)
    except ProcessLookupError:pass


def execute(run_id,root=ROOT,runtimes=RUNTIMES,owner=0,require_unit=True):
    if not L.HEX.fullmatch(run_id):raise ValueError('invalid run ID')
    run=Path(root)/'runs'/run_id;store=L.Store(run,owner);request=store.read('request.json');bound=L.binding(request['binding'])
    if bound['run_id']!=run_id:raise ValueError('execution run differs')
    if require_unit and not Path('/proc/self/cgroup').read_text().strip().endswith('/'+L.unit(bound)):raise ValueError('execution outside owned unit')
    old=store.read('execution.json')
    if old:return old
    if store.read('execution-started.json') is not None:raise ValueError('execution already attempted; reconcile before a new run')
    source=store.read('source.json')
    if inventory(run/'source',owner)!=source['files'] or sha(run/'main.py')!=hashlib.sha256(request['script'].encode()).hexdigest():raise ValueError('sealed source/script changed')
    rootfs,manifest=runtime(request['runtime_key'],runtimes,owner);profile=PROFILES[request['profile']]
    if request.get('helpers')!=L.helper_identity():raise ValueError('qualified GPU helper identity differs')
    scratch=run/'scratch'
    # systemd TemporaryFileSystem provides the finite scratch mount; never use
    # an ordinary unbounded directory after misconfigured launch.
    if require_unit and not os.path.ismount(scratch):raise ValueError('bounded scratch mount missing')
    if not require_unit and profile['devices']:raise ValueError('GPU execution requires owned unit')
    command=sandbox(run,request,rootfs,scratch);start=time.monotonic();state='exited';total=0
    cancelled=[False]
    signal.signal(signal.SIGTERM,lambda *_:cancelled.__setitem__(0,True))
    store.write('execution-started.json',{'binding':bound,'state':'launch_requested','monotonic_ns':time.monotonic_ns()})
    # Reuse the expert-probe private-mount handoff: no world traversal of the
    # private run directory and no privileged bwrap workload identity.
    if ctypes.CDLL(None,use_errno=True).unshare(0x00020000)!=0:raise OSError(ctypes.get_errno(),'private mount namespace unavailable')
    subprocess.run(['/usr/bin/mount','--make-rprivate','/'],check=True)
    subprocess.run(['/usr/bin/mount','-t','tmpfs','-o','mode=0755,size=1m,nosuid,nodev','gpu-stage','/tmp'],check=True)
    for index,mount_source in enumerate((rootfs,run/'source',run/'main.py',scratch)):
        target=Path('/tmp')/('gpu-input-'+str(index))
        if mount_source.is_dir():target.mkdir(mode=0o755)
        else:target.touch(mode=0o444)
        subprocess.run(['/usr/bin/mount','--bind',str(mount_source),str(target)],check=True)
        command=[str(target) if arg==str(mount_source) else arg for arg in command]
    os.chown(scratch,65534,65534)
    def drop():
        os.setgroups([]);os.setgid(65534);os.setuid(65534)
    child=subprocess.Popen(command,stdout=subprocess.PIPE,stderr=subprocess.STDOUT,start_new_session=True,preexec_fn=drop)
    selector=selectors.DefaultSelector();selector.register(child.stdout,selectors.EVENT_READ)
    with (run/'output.log').open('xb') as output:
        while selector.get_map() or child.poll() is None:
            if cancelled[0]:state='cancelled';kill(child)
            if time.monotonic()-start>profile['seconds']:state='timeout';kill(child)
            if not selector.get_map():time.sleep(.05)
            for key,_ in selector.select(.1):
                block=os.read(key.fileobj.fileno(),65536)
                if not block:selector.unregister(key.fileobj);continue
                remaining=profile['output']-total;output.write(block[:max(0,remaining)]);total+=min(len(block),max(0,remaining));output.flush()
                if len(block)>remaining:state='output_limit';kill(child)
        output.flush();os.fsync(output.fileno())
    exitcode=child.wait(timeout=5)
    if cancelled[0]:state='cancelled'
    (run/'output.log').chmod(0o444)
    result={'binding':bound,'state':state,'executed':True,'exit_code':exitcode,'elapsed_ms':int((time.monotonic()-start)*1000),
            'runtime_key':manifest['key'],'source_tree_sha256':source['tree_sha256'],'source_archive_sha256':source['archive_sha256'],
            'output_bytes':total,'output_sha256':sha(run/'output.log'),'profile':request['profile'],'limits':profile,'gpu_access':profile['devices'],
            'helpers':request['helpers']}
    store.write('execution.json',result);return result

def main():
    p=argparse.ArgumentParser();p.add_argument('command',choices=['execute']);p.add_argument('--run',required=True);a=p.parse_args()
    if os.geteuid()!=0:raise SystemExit('root executor required')
    print(json.dumps(execute(a.run)))
if __name__=='__main__':main()
