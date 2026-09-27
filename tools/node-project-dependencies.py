#!/usr/bin/python3
"""Fixed npm provisioner; package execution occurs only in offline namespaces."""
import hashlib
import importlib.util
import json
import os
from pathlib import Path, PurePosixPath
import re
import shutil
import stat
import subprocess
import sys
import tarfile
import tempfile
import http.client
import http.server
import socket
import urllib.request
import urllib.error

spec=importlib.util.spec_from_file_location('node_validator',Path(__file__).with_name('node-dependencies.py'))
V=importlib.util.module_from_spec(spec);spec.loader.exec_module(V)
POLICY='npm-locked-offline-v1'
MAX_BYTES=1024**3
MAX_FILES=100000
BROKER='http://127.0.0.1:18080/npm/'
class Unsupported(ValueError): pass

def canonical(x):return json.dumps(x,sort_keys=True,separators=(',',':'),ensure_ascii=True).encode()
def sha(x):return hashlib.sha256(x).hexdigest()
def file_sha(p):
 h=hashlib.sha256()
 with Path(p).open('rb') as f:
  for b in iter(lambda:f.read(1024**2),b''):h.update(b)
 return h.hexdigest()
def relative(s):
 if not isinstance(s,str) or not s or len(s)>512 or str(PurePosixPath(s))!=s or s.startswith('/') or '\\' in s or any(x in ('.','..') for x in s.split('/')):raise Unsupported('noncanonical relative path')
 return s

def normalize_lock(raw,to_broker=False):
 lock=V.read_json(raw)
 for path,row in lock.get('packages',{}).items():
  if not path:continue
  name=V.package_path(path);version=row.get('version','')
  canonical_url='https://registry.npmjs.org/'+name+'/-/'+name.split('/')[-1]+'-'+version+'.tgz'
  mirror=BROKER+canonical_url.removeprefix('https://registry.npmjs.org/')
  if row.get('resolved') not in (canonical_url,mirror):raise Unsupported('lock contains nonregistry or noncanonical dependency')
  V.registry_tarball(canonical_url,name,version)
  row['resolved']=mirror if to_broker else canonical_url
 return canonical(lock)

def manifest_safe(raw):
 m=V.read_json(raw)
 if not isinstance(m,dict) or m.get('workspaces') or m.get('bundledDependencies') or m.get('bundleDependencies'):raise Unsupported('workspace or bundled project requires additional capability')
 for section in ('dependencies','devDependencies','optionalDependencies','peerDependencies'):
  for name,value in m.get(section,{}).items():
   if not V.NAME.fullmatch(name) or not isinstance(value,str) or any(x in value for x in (':','/','\\','\x00','\n')):raise Unsupported('only public registry dependency ranges supported')
 return m

def inventory(root):
 root=Path(root);rows=[];total=0
 for p in sorted(root.rglob('*')):
  st=p.lstat();rel=p.relative_to(root).as_posix()
  if len(rows)>MAX_FILES:raise Unsupported('file inventory bound exceeded')
  if stat.S_ISDIR(st.st_mode):continue
  if stat.S_ISLNK(st.st_mode):
   target=os.readlink(p)
   if target.startswith('/') or not p.resolve().is_relative_to(root.resolve()) or not p.resolve().is_file():raise Unsupported('escaping or dangling installed symlink')
   rows.append(dict(path=rel,link=target));continue
  if not stat.S_ISREG(st.st_mode) or st.st_nlink!=1:raise Unsupported('unsupported installed filesystem object')
  total+=st.st_size
  if total>MAX_BYTES or st.st_size>256*1024**2:raise Unsupported('installation byte bound exceeded')
  rows.append(dict(path=rel,size=st.st_size,sha256=file_sha(p),mode=0o555 if st.st_mode&0o111 else 0o444))
 return rows

def extract_project(archive,prefix,destination):
 prefix=PurePosixPath('work')/prefix;count=total=0;seen=set()
 with tarfile.open(archive,'r|gz') as source:
  for member in source:
   name=member.name.removeprefix('./').rstrip('/')
   if not name:continue
   relative(name)
   p=PurePosixPath(name)
   if not p.is_relative_to(prefix):continue
   rel=p.relative_to(prefix)
   if str(rel)=='.':continue
   if '.git' in rel.parts or 'node_modules' in rel.parts:continue
   if str(rel) in seen:raise Unsupported('duplicate source path')
   seen.add(str(rel));count+=1;total+=member.size
   if count>MAX_FILES or total>MAX_BYTES:raise Unsupported('source extraction limit')
   out=destination/str(rel)
   if member.isdir():out.mkdir(parents=True,exist_ok=True);continue
   if not member.isfile():raise Unsupported('source links require explicit project support')
   out.parent.mkdir(parents=True,exist_ok=True)
   with out.open('xb') as f:shutil.copyfileobj(source.extractfile(member),f,1024**2)
   out.chmod(0o755 if member.mode&0o111 else 0o644)

def proxy():
 class UnixHTTP(http.client.HTTPConnection):
  def connect(self):
   self.sock=socket.socket(socket.AF_UNIX,socket.SOCK_STREAM);self.sock.settimeout(45);self.sock.connect('/dependency.sock')
 class Handler(http.server.BaseHTTPRequestHandler):
  def log_message(self,*a):pass
  def do_GET(self):
   event={'status':502,'marker':'transport'}
   try:
    if not self.path.startswith('/npm/') or len(self.path)>1024:raise ValueError('invalid fixed registry route')
    c=UnixHTTP('localhost');c.request('GET',self.path,headers={'Accept':self.headers.get('Accept','application/vnd.npm.install-v1+json')});r=c.getresponse()
    event={'status':r.status,'marker':r.getheader('X-Lectern-Node-Registry',''),'path':self.path}
    self.send_response(r.status)
    for key in ('Content-Type','Content-Length','X-Lectern-Node-Registry'):
     if r.getheader(key):self.send_header(key,r.getheader(key))
    self.end_headers()
    while data:=r.read(65536):self.wfile.write(data)
   except Exception:
    event={'status':502,'marker':'transport'}
    try:self.send_error(502)
    except OSError:pass
   finally:
    with open('/fetch/transport-events.jsonl','ab') as f:f.write(canonical(event)+b'\n')
 server=http.server.ThreadingHTTPServer(('127.0.0.1',18080),Handler);server.serve_forever()

def npm(args,cwd,offline=False):
 env=dict(os.environ,NPM_CONFIG_CACHE='/fetch/cache',NPM_CONFIG_USERCONFIG='/tmp/user.npmrc',NPM_CONFIG_GLOBALCONFIG='/tmp/global.npmrc',NPM_CONFIG_REGISTRY=BROKER,NPM_CONFIG_FETCH_RETRIES='0',NPM_CONFIG_AUDIT='false',NPM_CONFIG_FUND='false',NPM_CONFIG_UPDATE_NOTIFIER='false',NPM_CONFIG_ENGINE_STRICT='true')
 if offline:env['NPM_CONFIG_OFFLINE']='true'
 telemetry=Path('/fetch/transport-events.jsonl')
 offset=telemetry.stat().st_size if telemetry.exists() else 0
 with tempfile.TemporaryFile() as output,tempfile.TemporaryFile() as errors:
  p=subprocess.run(['/opt/node/bin/node','/opt/node/lib/node_modules/npm/bin/npm-cli.js','--json',*args],cwd=cwd,env=env,stdout=output,stderr=errors)
  output.seek(0,2);size=output.tell();output.seek(max(0,size-65536));raw=output.read(65536);sys.stdout.buffer.write(raw[-16384:])
  errors.seek(0,2);size=errors.tell();errors.seek(max(0,size-16384));sys.stderr.buffer.write(errors.read(16384))
 if p.returncode:
  if offline:raise Unsupported('offline npm install/hook failed; see isolated diagnostic')
  events=[]
  if telemetry.exists():
   with telemetry.open('rb') as f:f.seek(offset);events=[json.loads(line) for line in f.read(1024**2).splitlines()]
  transient=any(e['status']>=500 or (e['status']>=400 and (e['status'],e['marker']) not in ((404,'package-not-found'),(422,'unsupported'))) for e in events)
  try:code=json.loads(raw).get('error',{}).get('code','')
  except ValueError:code=''
  if not transient and (any(e['marker'] in ('package-not-found','unsupported') for e in events) or code in ('ETARGET','ERESOLVE','EBADENGINE','EBADPLATFORM','EUNSUPPORTEDPROTOCOL')):
   raise Unsupported('trusted npm resolver found no supported locked environment')
  raise RuntimeError('npm registry resolution failed')
 return p

def resolve():
 f=Path('/fetch');req=json.loads((f/'input.json').read_text());project=f/'project'
 if project.exists():shutil.rmtree(project)
 project.mkdir()
 if not (project/'package.json').exists():
  if req.get('package_json'):
   extract_project('/source.tar.gz',str(PurePosixPath(req['package_json']).parent),project)
   (f/'source-lock.json').write_bytes((project/'package-lock.json').read_bytes())
  else:
   dependencies={}
   for pin in req['requirements']:
    name,version=pin.rsplit('@',1)
    if not V.NAME.fullmatch(name) or not V.VERSION.fullmatch(version):raise Unsupported('exact npm name@version required')
    if name in dependencies and dependencies[name]!=version:raise Unsupported('conflicting direct npm pins')
    dependencies[name]=version
   (project/'package.json').write_bytes(canonical(dict(name='lectern-offline-runtime',version='1.0.0',private=True,dependencies=dependencies)))
 manifest=(project/'package.json').read_bytes();manifest_safe(manifest)
 if (project/'.npmrc').exists():(project/'.npmrc').unlink()
 if (f/'sealed-lock.json').exists():
  raw=(f/'sealed-lock.json').read_bytes()
 elif req.get('package_lock'):
  raw=normalize_lock((project/'package-lock.json').read_bytes())
 else:
  npm(['install','--package-lock-only','--ignore-scripts','--include=dev','--include=optional','--include=peer'],project)
  raw=normalize_lock((project/'package-lock.json').read_bytes())
 V.validate_lock(manifest,raw)
 (project/'package-lock.json').write_bytes(raw)
 (f/'resolved-lock.json').write_bytes(raw)
 (f/'resolved-manifest.json').write_bytes(manifest)

def fetch():
 f=Path('/fetch');manifest=(f/'resolved-manifest.json').read_bytes();raw=(f/'sealed-lock.json').read_bytes();proof=V.validate_lock(manifest,raw)
 total=unpacked=0;records=[];tarballs=f/'tarballs';tarballs.mkdir(exist_ok=True)
 for row in proof['packages']:
  path=tarballs/(sha(row['resolved'].encode())+'.tgz')
  if not path.exists():
   url=BROKER+row['resolved'].removeprefix('https://registry.npmjs.org/')
   try:
    with urllib.request.urlopen(urllib.request.Request(url,headers={'Accept':'application/octet-stream'}),timeout=40) as response:data=response.read(V.MAX_TARBALL+1)
   except urllib.error.HTTPError as e:
    if e.code in (404,422) and e.headers.get('X-Lectern-Node-Registry') in ('package-not-found','unsupported'):raise Unsupported('registry cannot deliver locked dependency') from e
    raise
   V.validate_tarball(data,row);path.write_bytes(data)
  data=path.read_bytes();record=V.validate_tarball(data,row);total+=len(data);unpacked+=record['unpacked_bytes']
  if total>512*1024**2 or unpacked>MAX_BYTES:raise Unsupported('aggregate dependency storage bound')
  records.append(dict(row,tarball_sha256=record['tarball_sha256'],lifecycle_scripts=record['lifecycle_scripts'],implicit_node_gyp=record['implicit_node_gyp']))
  npm(['cache','add',str(path),'--ignore-scripts','--offline'],f,True)
 (f/'transport.json').write_bytes(canonical(records))

def install():
 f=Path('/fetch');p=f/'project';raw=(f/'sealed-lock.json').read_bytes();manifest=(f/'resolved-manifest.json').read_bytes()
 (p/'package.json').write_bytes(manifest);(p/'package-lock.json').write_bytes(raw)
 npm(['ci','--offline','--ignore-scripts=false','--include=dev','--include=optional','--include=peer','--foreground-scripts'],p,True)
 if (p/'package-lock.json').read_bytes()!=raw or (p/'package.json').read_bytes()!=manifest:raise Unsupported('installation changed admitted dependency metadata')
 # npm ci enforces peer/platform/engine/tree semantics; npm ls catches missing
 # required packages without interpreting optional platform exclusions as loss.
 npm(['ls','--all','--json','--offline'],p,True)
 (p/'node_modules').mkdir(exist_ok=True)
 inventory(p);inventory(f/'cache')

def probe():
 req=json.loads(Path('/fetch/input.json').read_text());project=Path('/fetch/project')
 script="const m=process.argv[1];const u=import.meta.resolve(m);if(!u.startsWith('file:///fetch/project/node_modules/'))throw Error('module outside admitted packages');await import(u);"
 for module in req.get('modules',[]):
  p=subprocess.run(['/opt/node/bin/node','--input-type=module','-e',script,module],cwd=project,timeout=30)
  if p.returncode:raise Unsupported('requested Node module failed offline import')
 for name in req.get('binaries',[]):
  binary=project/'node_modules/.bin'/name
  if not binary.is_file() or not binary.resolve().is_relative_to((project/'node_modules').resolve()):raise Unsupported('requested npm binary unavailable')
  # Fixed version probe executes the real binary, never shell-selected argv.
  if subprocess.run([str(binary),'--version'],cwd='/tmp',timeout=30).returncode:raise Unsupported('requested npm binary failed offline version probe')
 Path('/proof/probe.json').write_bytes(canonical(dict(modules=req.get('modules',[]),binaries=req.get('binaries',[]),network='unshared-no-socket',input_key=req['input_key'])))

if __name__=='__main__':
 try:
  {'proxy':proxy,'resolve':resolve,'fetch':fetch,'install':install,'probe':probe}[sys.argv[1]]()
 except (Unsupported,ValueError) as e:
  print(str(e),file=sys.stderr);sys.exit(2)
