#!/usr/bin/python3
"""Immutable local-cache browser payload. No upstream checksum provenance is claimed.

Only browser release files are copied. Python packages, profiles and credentials
are never copied. Activation is a separate root operation after verification.
"""
import argparse
import hashlib
import json
import os
import platform
from pathlib import Path
import shutil
import stat
import tempfile

MAX_BYTES=1024**3
MAX_FILES=10000

class BrowserCompatibilityError(ValueError):
    pass

def canonical(value):return json.dumps(value,sort_keys=True,separators=(',',':')).encode()
def sha(path):
    result=hashlib.sha256()
    with path.open('rb') as stream:
        for block in iter(lambda:stream.read(1024*1024),b''):result.update(block)
    return result.hexdigest()
def regular(path):
    info=path.lstat()
    if not stat.S_ISREG(info.st_mode) or info.st_nlink!=1:raise ValueError('browser member must be independent regular file')
    return info

def path_parts(name):
    if not isinstance(name,str) or not name or name.startswith('/') or '\\' in name or any(p in ('','.','..') for p in name.split('/')):raise ValueError('unsafe browser member path')
    return name.split('/')

def inventory(root,immutable=False):
    files=[];total=0
    for path in sorted(root.rglob('*')):
        info=path.lstat()
        if stat.S_ISDIR(info.st_mode):
            if immutable and (info.st_uid!=os.geteuid() or stat.S_IMODE(info.st_mode)!=0o555):raise ValueError('unsafe browser directory')
            continue
        info=regular(path);total+=info.st_size
        if len(files)>=MAX_FILES or total>MAX_BYTES:raise ValueError('browser inventory budget exceeded')
        mode=0o555 if info.st_mode&0o111 else 0o444
        if immutable and (info.st_uid!=os.geteuid() or stat.S_IMODE(info.st_mode)!=mode):raise ValueError('unsafe browser file mode')
        name=path.relative_to(root).as_posix();path_parts(name)
        files.append({'path':name,'size':info.st_size,'sha256':sha(path),'mode':mode})
    if not files:raise ValueError('empty browser payload')
    return files

def verify(bundle,full=True,owner=0):
    bundle=Path(bundle)
    for directory in (bundle,bundle/'browsers'):
        info=directory.lstat()
        if not stat.S_ISDIR(info.st_mode) or info.st_uid!=owner or info.st_mode&0o222:raise ValueError('unsafe browser bundle directory')
    if {item.name for item in bundle.iterdir()}!={'browsers','manifest.json'}:raise ValueError('unlisted browser bundle content')
    path=bundle/'manifest.json';info=regular(path)
    if info.st_uid!=owner or info.st_mode&0o222 or info.st_size>4*1024**2:raise ValueError('unsafe browser manifest')
    manifest=json.loads(path.read_text());body=dict(manifest);key=body.pop('key',None)
    if key!=hashlib.sha256(canonical(body)).hexdigest() or bundle.name!=key or manifest.get('kind')!='browser-runtime' or manifest.get('schema_version')!=1:raise ValueError('browser manifest identity mismatch')
    if manifest.get('engine')!='chromium' or manifest.get('platform')!={'system':platform.system(),'machine':platform.machine()}:raise BrowserCompatibilityError('browser platform mismatch')
    if manifest.get('provenance')!='local-installed-cache-inventory' or not manifest.get('playwright_version') or set(manifest.get('revisions',{}))!={'chromium','chromium-headless-shell','ffmpeg'}:raise ValueError('unsupported browser manifest')
    if full and inventory(bundle/'browsers',immutable=True)!=manifest['files']:raise ValueError('browser payload changed')
    return manifest

OFFLINE_TEST='''#!/usr/bin/env -S /usr/bin/python3 -I -S
# Run ordinary test argv in a fresh socket-free namespace. No host privileges.
import os,sys
if len(sys.argv)<2:raise SystemExit("usage: offline-test COMMAND [ARGS...]")
cmd=['/usr/bin/bwrap','--die-with-parent','--new-session','--unshare-all','--cap-drop','ALL','--clearenv','--tmpfs','/','--ro-bind','/usr','/usr']
for p in ('/lib','/lib64','/bin'):
 if os.path.exists(p):cmd+=['--ro-bind',p,p]
cmd+=['--proc','/proc','--dev','/dev','--tmpfs','/tmp','--dir','/home/agent','--dir','/etc','--bind','/work','/work','--chdir',os.getcwd() if os.getcwd()=='/work' or os.getcwd().startswith('/work/') else '/work','--ro-bind','/opt/browser-runtime','/opt/browser-runtime','--setenv','HOME','/home/agent','--setenv','PATH','/usr/bin:/bin','--setenv','PLAYWRIGHT_BROWSERS_PATH','/opt/browser-runtime/browsers','--setenv','PYTHONDONTWRITEBYTECODE','1','--dir','/home/agent/.cache','--symlink','/opt/browser-runtime/browsers','/home/agent/.cache/ms-playwright']
for prefix in ('python-test','python-project'):
 for path in ('/opt/'+prefix,'/opt/'+prefix+'-runtime.json'):
  if os.path.exists(path):cmd+=['--ro-bind',path,path]
for name in ('PYTHONPATH','LANG','TERM'):
 if name in os.environ:cmd+=['--setenv',name,os.environ[name]]
cmd+=['--',*sys.argv[1:]]
os.execv(cmd[0],cmd)
'''

def provision(cache,driver_json,version,destination,stage=False):
    cache=Path(cache);destination=Path(destination)
    if not stage and os.geteuid()!=0:raise ValueError('production provisioning requires root')
    for root in (cache,Path(driver_json).parent,destination):
        for path in (root,*root.parents):
            if path.is_symlink():raise ValueError('linked browser source or destination ancestor')
    destination.mkdir(parents=True,exist_ok=True)
    if not stage:
        # Dedicated dependency boundary; upper /mnt/bulk may be admin owned.
        for path in (destination,destination.parent,destination.parent.parent):
            info=path.lstat()
            if info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe browser production boundary')
        if shutil.disk_usage(destination).free<22*1024**3:raise ValueError('browser provisioning requires storage headroom')
    info=regular(Path(driver_json))
    if info.st_size>1024**2:raise ValueError('oversized browser driver declaration')
    data=json.loads(Path(driver_json).read_text());selected={row['name']:row for row in data['browsers'] if row['name'] in ('chromium','chromium-headless-shell','ffmpeg')}
    if set(selected)!={'chromium','chromium-headless-shell','ffmpeg'}:raise ValueError('incomplete browser revisions')
    if not isinstance(version,str) or not version or len(version)>64 or any(c not in '0123456789.abrcdev+-' for c in version):raise ValueError('invalid Playwright version')
    with tempfile.TemporaryDirectory(prefix='.browser-stage-',dir=destination) as temp:
        pending=Path(temp);browsers=pending/'browsers';browsers.mkdir()
        for name,row in selected.items():
            revision=row['revision']
            if not isinstance(revision,str) or not revision.isdigit():raise ValueError('invalid browser revision')
            dirname=name.replace('-','_')+'-'+revision;source=cache/dirname
            if not source.is_dir() or source.is_symlink():raise ValueError('required browser revision unavailable')
            inventory(source) # Reject links/specials before copying, then compare inventories.
            shutil.copytree(source,browsers/dirname,symlinks=True)
            if inventory(source)!=inventory(browsers/dirname):raise ValueError('browser source changed while copying')
        (browsers/'offline-test').write_text(OFFLINE_TEST);(browsers/'offline-test').chmod(0o555)
        for item in sorted(browsers.rglob('*'),reverse=True):item.chmod(0o555 if item.is_dir() or item.stat().st_mode&0o111 else 0o444)
        browsers.chmod(0o555)
        manifest={'schema_version':1,'kind':'browser-runtime','engine':'chromium','platform':{'system':platform.system(),'machine':platform.machine()},'provenance':'local-installed-cache-inventory','playwright_version':version,'driver_declaration_sha256':sha(Path(driver_json)),'revisions':{name:row['revision'] for name,row in selected.items()},'browser_version':selected['chromium'].get('browserVersion'),'files':inventory(browsers)}
        key=hashlib.sha256(canonical(manifest)).hexdigest();manifest['key']=key
        (pending/'manifest.json').write_bytes(canonical(manifest));(pending/'manifest.json').chmod(0o444)
        target=destination/key
        if target.exists():verify(target,owner=os.geteuid())
        else:
            pending.chmod(0o555);os.rename(pending,target)
        return target,manifest

def main():
    parser=argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cache',type=Path,required=True)
    parser.add_argument('--driver-json',type=Path,required=True)
    parser.add_argument('--playwright-version',required=True)
    parser.add_argument('--destination',type=Path,required=True)
    parser.add_argument('--stage',action='store_true')
    args=parser.parse_args();path,manifest=provision(args.cache,args.driver_json,args.playwright_version,args.destination,args.stage)
    print(json.dumps({'path':str(path),'key':manifest['key'],'provenance':manifest['provenance'],'state':'staged_requires_offline_probe'}))
if __name__=='__main__':main()
