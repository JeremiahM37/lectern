#!/usr/bin/python3
"""Root lifecycle for the fixed, isolated npm provisioner. Imported by runner."""
import fcntl,hashlib,importlib.util,json,os,platform,pwd,re,resource,shutil,stat,subprocess,sys,time,uuid
from pathlib import Path, PurePosixPath
FILES=('autonomy-node-runtime.py','node-project-dependencies.py','node-dependencies.py','autonomy-runner.py')
HEX=re.compile('[0-9a-f]{64}\\Z')
class Unsupported(ValueError):pass

def helper(stage):
 path=stage/'tools/node-project-dependencies.py'
 spec=importlib.util.spec_from_file_location('node_project',path);m=importlib.util.module_from_spec(spec)
 previous=sys.dont_write_bytecode;sys.dont_write_bytecode=True
 try:spec.loader.exec_module(m)
 finally:sys.dont_write_bytecode=previous
 return m

def safe_dir(path):
 path.mkdir(mode=0o700,parents=True,exist_ok=True);st=path.lstat()
 if not stat.S_ISDIR(st.st_mode) or st.st_uid!=0 or st.st_mode&0o022:raise ValueError('unsafe Node root state')
 return path

def request(R,job):
 p=R.job_path(job);path=p/'node-requirement.json';st=R.regular(path)
 if st.st_uid not in (0,pwd.getpwnam('admin').pw_uid) or st.st_mode&0o022 or st.st_size>16384:raise ValueError('unsafe Node requirement envelope')
 q=json.loads(path.read_text());required={'schema_version','kind','source_job','source_archive_sha256','admission_sha256'}
 optional={'requirements','modules','binaries','package_json','package_lock','expected_input_key','expected_bundle_key','expected_lock_sha256'}
 if not required<=q.keys() or q.keys()-required-optional or q['schema_version']!=1 or q['kind']!='node_packages':raise ValueError('invalid Node envelope schema')
 R.job_path(q['source_job'])
 for k in ('source_archive_sha256','admission_sha256'):
  if not isinstance(q[k],str) or not HEX.fullmatch(q[k]):raise ValueError('invalid Node binding')
 expected=[q.get(k,'') for k in ('expected_input_key','expected_bundle_key','expected_lock_sha256')]
 if any(expected) and not all(isinstance(x,str) and HEX.fullmatch(x) for x in expected):raise ValueError('incomplete inherited Node identity')
 pins=q.get('requirements',[])
 if bool(pins)==bool(q.get('package_json')):raise ValueError('Node pins and locked project are exclusive')
 for key in ('requirements','modules','binaries'):
  values=q.get(key,[])
  if not isinstance(values,list) or len(values)>32 or any(not isinstance(x,str) or not x or len(x)>512 for x in values):raise ValueError('invalid Node selector list')
  q[key]=sorted(set(values))
 for pin in pins:
  name,sep,version=pin.rpartition('@')
  if not sep or not re.fullmatch(r'(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*',name) or not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?',version):raise ValueError('exact npm pins required')
 for value in q['modules']:
  if len(value)>256 or not re.fullmatch(r'(?:@[a-z0-9][a-z0-9._-]*/)?[a-z0-9][a-z0-9._-]*(?:/[A-Za-z0-9_.-]+)*',value) or any(x in ('.','..') for x in value.split('/')):raise ValueError('invalid module selector')
 for value in q['binaries']:
  if len(value)>128 or not re.fullmatch('[A-Za-z0-9][A-Za-z0-9_.-]*',value):raise ValueError('invalid binary selector')
 if q.get('package_json'):
  for key,base in (('package_json','package.json'),('package_lock','package-lock.json')):
   value=q.get(key,'');parts=value.split('/')
   if len(value)>512 or str(PurePosixPath(value))!=value or value.startswith('/') or '\\' in value or any(x in ('.','..') for x in parts) or parts[-1]!=base:raise ValueError('invalid project manifest path')
  if PurePosixPath(q['package_json']).parent!=PurePosixPath(q['package_lock']).parent:raise ValueError('project manifest and lock directory differ')
 elif q.get('package_lock'):raise ValueError('orphan Node lock path')
 if R.archive_identity(q['source_job'])['sha256']!=q['source_archive_sha256']:raise ValueError('Node source archive mismatch')
 stage=safe_dir(p/'node-prerequisite');sealed=stage/'request.json'
 if sealed.exists():
  if R.completion_json(sealed)!=q:raise ValueError('Node requirement changed after admission')
 else:R.completion_write(sealed,q)
 return stage,q

def sync_directory(path):
 fd=os.open(path,os.O_DIRECTORY)
 try:os.fsync(fd)
 finally:os.close(fd)

def sync_tree(root):
 directories=[root]
 for p in root.rglob('*'):
  if p.is_symlink():continue
  if p.is_dir():directories.append(p)
  else:
   with p.open('rb') as f:os.fsync(f.fileno())
 for directory in reversed(directories):sync_directory(directory)

def binding(q):return dict(capability='node_packages',**{k:q[k] for k in ('source_job','source_archive_sha256','admission_sha256')})
def unit(job):return 'lectern-node-dependencies-'+job+'.service'
def active(R,job):return R.completion_service_active(unit(job))

def freeze(R,stage):
 root=safe_dir(stage/'tools');manifest=root/'manifest.json'
 if not manifest.exists() and any(root.iterdir()):
  os.rename(root,stage/('interrupted-tools-'+str(uuid.uuid4())))
  root=safe_dir(stage/'tools');manifest=root/'manifest.json'
 if not manifest.exists():
  records={}
  for name in FILES:
   source=Path(__file__).with_name(name) if name!='autonomy-runner.py' else Path(R.__file__)
   info=R.regular(source)
   if info.st_uid!=0 or info.st_mode&0o022 or info.st_size>1024**2:raise ValueError('untrusted Node executable source')
   data=source.read_bytes();dest=root/name
   if dest.exists() and dest.read_bytes()!=data:raise ValueError('interrupted helper snapshot changed')
   if not dest.exists():dest.write_bytes(data);dest.chmod(0o555)
   records[name]=hashlib.sha256(data).hexdigest()
  R.completion_write(manifest,records)
  root.chmod(0o555)
 verify_tools(R,stage)

def verify_tools(R,stage):
 records=R.completion_json(stage/'tools/manifest.json')
 if set(records)!=set(FILES):raise ValueError('incomplete Node executable inventory')
 for name,digest in records.items():
  p=stage/'tools'/name;st=R.regular(p)
  if st.st_uid!=0 or st.st_mode&0o222 or R.digest_file(p)!=digest:raise ValueError('frozen Node helper changed')
 return records

def launch(R,job,generation=1):
 if type(generation)!=int or generation<1:raise ValueError("invalid Node generation")
 if R.status(job)['state']=='running':raise ValueError('Node provisioning needs inactive worker')
 stage,q=request(R,job)
 with (stage/'guard').open('a') as guard:
  fcntl.flock(guard,fcntl.LOCK_EX)
  cancelled=R.completion_json(stage/'cancel.json').get('revoked_through',0) if (stage/'cancel.json').exists() else 0
  if generation<=cancelled:return dict(binding(q),state='waiting',generation=generation,reason='Node launch generation revoked')
  freeze(R,stage)
  receipt=R.completion_json(stage/'receipt.json') if (stage/'receipt.json').exists() else binding(q)
  if any(receipt.get(k)!=v for k,v in binding(q).items()):raise ValueError('Node receipt binding mismatch')
  if receipt.get('generation',generation)>generation:return dict(binding(q),state='waiting',generation=generation,reason='stale Node launch generation')
  if active(R,job) and receipt.get('generation',generation)!=generation:return dict(binding(q),state='waiting',generation=generation,reason='prior Node generation is still active')
  receipt['generation']=generation
  (stage/'heartbeat').touch()
  if receipt.get('state')=='verified':
   try:bundle(R,job,False)
   except (OSError,ValueError) as e:
    receipt.update(state='unavailable',unsupported=True,reason='Retained Node environment unavailable: '+str(e)[:300])
   R.completion_write(stage/'receipt.json',receipt);return receipt
  if active(R,job):return dict(receipt,state='recovering')
  if receipt.get('unsupported'):return receipt
  if receipt.get('retry_at',0)>time.time():return dict(receipt,state='waiting')
  receipt.update(state='recovering',started_at=time.time());R.completion_write(stage/'receipt.json',receipt)
  R.run(['/usr/bin/systemd-run','--quiet','--collect','--unit='+unit(job),'--property=RuntimeMaxSec=900','--property=MemoryMax=2G','--property=MemorySwapMax=0','--property=CPUQuota=200%','--property=TasksMax=128','--property=LimitFSIZE=2147483648','--property=KillMode=control-group','--property=PrivateMounts=yes','--property=UMask=0077','/usr/bin/python3',str(stage/'tools/autonomy-runner.py'),'_node-dependencies','--job',job,'--generation',str(generation)],pass_fds=(guard.fileno(),))
  return receipt

def stop(R,job,generation=1):
 if type(generation)!=int or generation<1:raise ValueError("invalid Node generation")
 stage=safe_dir(R.job_path(job)/'node-prerequisite')
 with (stage/'guard').open('a') as guard:
  fcntl.flock(guard,fcntl.LOCK_EX)
  old=R.completion_json(stage/'cancel.json').get('revoked_through',0) if (stage/'cancel.json').exists() else 0
  R.completion_write(stage/'cancel.json',{'revoked_through':max(old,generation)})
  current=R.completion_json(stage/'receipt.json') if (stage/'receipt.json').exists() else {}
  if current.get('generation',generation)>generation:return {'state':'stopped','job':job,'generation':generation}
  if active(R,job):R.run(['/usr/bin/systemctl','stop',unit(job)],pass_fds=(guard.fileno(),))
  if active(R,job):return {'state':'stopping','job':job,'generation':generation}
  if (stage/'receipt.json').exists():
   value=R.completion_json(stage/'receipt.json')
   if value.get('state')=='recovering':value.update(state='waiting',reason='Node provisioning stopped by controller',retry_at=0);R.completion_write(stage/'receipt.json',value)
 return {'state':'stopped','job':job,'generation':generation}

def tooling(R,key=None,full=True):
 root=R.DEPENDENCIES/'node-tooling';safe_dir(root)
 if key is None:
  p=root/'active.json'
  if not p.exists():raise Unsupported('registered Node tooling unavailable')
  st=R.regular(p)
  if st.st_uid!=0 or st.st_mode&0o222:raise ValueError('unsafe Node tooling selector')
  key=json.loads(p.read_text())['key']
 if not HEX.fullmatch(key):raise ValueError('invalid Node tooling key')
 base=root/key;p=base/'manifest.json'
 if not p.exists():raise Unsupported('retained Node tooling unavailable')
 st=R.regular(p)
 if st.st_uid!=0 or st.st_mode&0o222 or st.st_size>32*1024**2:raise ValueError('unsafe Node tooling manifest')
 m=json.loads(p.read_text());body=dict(m);body.pop('key',None)
 if hashlib.sha256(json.dumps(body,sort_keys=True,separators=(',',':'),ensure_ascii=True).encode()).hexdigest()!=key or m['key']!=key:raise ValueError('Node tooling identity mismatch')
 if m['platform']!=platform.system() or m['machine']!=platform.machine():raise Unsupported('retained Node tooling platform incompatible')
 if full:verify_inventory(R,base/'runtime',m['files'])
 return base,m

def verify_inventory(R,root,rows):
 expected=set()
 for row in rows:
  rel=row['path']
  if not isinstance(rel,str) or str(PurePosixPath(rel))!=rel or rel.startswith('/') or any(x in ('.','..') for x in rel.split('/')):raise ValueError('unsafe published Node path')
  p=root/rel;expected.add(rel);st=p.lstat()
  if 'link' in row:
   if not stat.S_ISLNK(st.st_mode) or os.readlink(p)!=row['link'] or not p.resolve().is_relative_to(root.resolve()) or st.st_uid!=0:raise ValueError('Node symlink changed')
  elif not stat.S_ISREG(st.st_mode) or st.st_uid!=0 or stat.S_IMODE(st.st_mode)!=row['mode'] or st.st_size!=row['size'] or R.digest_file(p)!=row['sha256']:raise ValueError('Node bundle bytes/modes changed')
 actual=set()
 for p in root.rglob('*'):
  st=p.lstat()
  if stat.S_ISDIR(st.st_mode):
   if st.st_uid!=0 or st.st_mode&0o222:raise ValueError('unsafe Node directory')
  else:actual.add(p.relative_to(root).as_posix())
 if expected!=actual:raise ValueError('Node inventory changed')

def sandbox(R,job,stage,phase,online=False):
 cmd=['/usr/bin/bwrap','--unshare-all','--die-with-parent','--new-session','--cap-drop','ALL','--clearenv','--tmpfs','/','--ro-bind','/usr','/usr']
 for p in ('/lib','/lib64','/bin'):
  if Path(p).exists():cmd+=['--ro-bind',p,p]
 for p in ('/usr/local/bin','/usr/local/sbin','/usr/local/etc'):
  if Path(p).exists():cmd+=['--tmpfs',p]
 cmd+=['--proc','/proc','--dev','/dev','--tmpfs','/tmp','--dir','/etc','--ro-bind','/tmp/node-tools','/tools','--ro-bind','/tmp/node-runtime','/opt/node','--ro-bind' if phase=='probe' else '--bind','/tmp/node-fetch','/fetch','--ro-bind','/tmp/node-source','/source.tar.gz','--bind','/tmp/node-proof','/proof','--setenv','PATH','/opt/node/bin:/usr/bin:/bin','--setenv','HOME','/tmp','--chdir','/fetch']
 script=''
 if online:cmd+=['--ro-bind','/tmp/node-dependency.sock','/dependency.sock'];script='/usr/bin/python3 -I /tools/node-project-dependencies.py proxy &\nsleep .2\n'
 script+='exec /usr/bin/python3 -I /tools/node-project-dependencies.py '+phase
 def drop():
  resource.setrlimit(resource.RLIMIT_FSIZE,(256*1024**2,256*1024**2));os.setgroups([]);os.setgid(R.GID);os.setuid(R.UID)
 with (stage/(phase+'.log')).open('w') as log:
  proc=subprocess.Popen(cmd+['--','/bin/sh','-c',script],preexec_fn=drop,stdout=log,stderr=log,close_fds=True)
  deadline=time.monotonic()+600
  while proc.poll() is None:
   if time.monotonic()>deadline or not 0<=time.time()-(stage/'heartbeat').stat().st_mtime<60:
    R.run(['/usr/bin/systemctl','kill','--kill-whom=all','--signal=SIGKILL',unit(job)]);raise RuntimeError('Node heartbeat or runtime bound expired')
   time.sleep(.5)
  if proc.returncode:
   exc=Unsupported('offline Node '+phase+' requirement unavailable') if proc.returncode==2 else RuntimeError('Node '+phase+' transport/execution failed')
   exc.diagnostic=R.python_failure_diagnostic(stage/(phase+'.log'));raise exc

def bundle(R,job,full=True):
 p=R.job_path(job)
 if not (p/'node-requirement.json').exists():return None
 stage,q=request(R,job);r=R.completion_json(stage/'receipt.json')
 if r.get('state')!='verified' or any(r.get(k)!=v for k,v in binding(q).items()):raise Unsupported('Node requirement not verified')
 root=R.DEPENDENCIES/'node-project'/r['bundle_key'];path=root/'manifest.json';st=R.regular(path)
 if st.st_uid!=0 or st.st_mode&0o222 or st.st_size>32*1024**2:raise ValueError('unsafe Node manifest')
 m=json.loads(path.read_text());body=dict(m);body.pop('key',None)
 if hashlib.sha256(json.dumps(body,sort_keys=True,separators=(',',':')).encode()).hexdigest()!=r['bundle_key'] or any(m.get(k)!=r.get(k) for k in ('input_key','runtime_digest','lock_sha256','probe_sha256')):raise ValueError('Node manifest/receipt identity mismatch')
 for expected,actual in (('expected_input_key','input_key'),('expected_bundle_key','bundle_key'),('expected_lock_sha256','lock_sha256')):
  if q.get(expected) and q[expected]!=r[actual]:raise ValueError('retained Node environment changed')
 tooling(R,m['runtime_digest'],full)
 if full:verify_inventory(R,root/'payload',m['files'])
 return root

def execute(R,job,generation=1):
 R.DEPENDENCIES.mkdir(mode=0o755,exist_ok=True)
 with (R.DEPENDENCIES/'.provision.lock').open('a') as lock:
  try:fcntl.flock(lock,fcntl.LOCK_EX|fcntl.LOCK_NB)
  except BlockingIOError:
   stage,q=request(R,job);r=R.completion_json(stage/'receipt.json');r.update(state='waiting',reason='another bounded prerequisite provisioner is active',retry_at=time.time()+15);R.completion_write(stage/'receipt.json',r);return 0
  return execute_locked(R,job,generation)

def execute_locked(R,job,generation=1):
 if not Path('/proc/self/cgroup').read_text().strip().endswith('/'+unit(job)):raise ValueError('Node provisioner outside owned cgroup')
 stage,q=request(R,job);verify_tools(R,stage);H=helper(stage);r=R.completion_json(stage/'receipt.json')
 if r.get('generation')!=generation:raise ValueError('Node unit generation differs')
 if (stage/'cancel.json').exists() and R.completion_json(stage/'cancel.json')['revoked_through']>=generation:return 0
 try:
  if R.shutil.disk_usage(R.ROOT).free<24*1024**3 or R.allocated_storage()>R.STORAGE_LIMIT-4*1024**3:raise RuntimeError('Node prerequisite storage headroom unavailable')
  cache=safe_dir(R.DEPENDENCIES/'node-project')
  if q.get('expected_input_key'):
   selected,_=lookup_by_keys(R,q['expected_bundle_key'],q['expected_input_key'],q['expected_lock_sha256'],None,full=True)
   m=json.loads((selected/'manifest.json').read_text())
   if any(m['semantic'].get(k,[])!=q.get(k,[]) for k in ('requirements','modules','binaries')) or any(m['semantic'].get(k,'')!=q.get(k,'') for k in ('package_json','package_lock')):raise ValueError('inherited Node selectors differ')
   r.update({k:m[k] for k in ('input_key','runtime_digest','lock_sha256','probe_sha256')},bundle_key=m['key'],state='verified')
   for k in ('source_manifest_sha256','source_lock_sha256'):
    if k in m:r[k]=m[k]
   if q.get('package_json') and any(not HEX.fullmatch(r.get(k,'')) for k in ('source_manifest_sha256','source_lock_sha256')):raise Unsupported('retained Node source manifest provenance unavailable')
   R.completion_write(stage/'receipt.json',r);return 0
  archive=R.job_path(q['source_job'])/'artifact.tar.gz'
  if R.digest_file(archive)!=q['source_archive_sha256']:raise ValueError('Node source bytes changed')
  sourcecopy=stage/'source.tar.gz'
  if not sourcecopy.exists():
   if R.regular(archive).st_size>2*1024**3:raise Unsupported('source archive bound exceeded')
   temporary=stage/'source.pending';shutil.copyfile(archive,temporary);temporary.chmod(0o444)
   if R.digest_file(temporary)!=q['source_archive_sha256']:raise ValueError('source archive changed while copying')
   os.replace(temporary,sourcecopy)
  if R.digest_file(sourcecopy)!=q['source_archive_sha256']:raise ValueError('retained Node source changed')
  identity_path=stage/'identity.json'
  if identity_path.exists():
   ident=R.completion_json(identity_path)
   runtime,rt=tooling(R,ident['runtime_digest'])
  else:
   runtime,rt=tooling(R)
   ident=dict(runtime_digest=rt['key'],helper_hashes=verify_tools(R,stage))
   R.completion_write(identity_path,ident)
  R.dependency_volume(stage);fetch=R.ensure_work(stage)
  if (stage/'lock.json').exists():shutil.copyfile(stage/'lock.json',fetch/'sealed-lock.json')
  elif (fetch/'sealed-lock.json').exists():(fetch/'sealed-lock.json').unlink()
  (fetch/'input.json').write_bytes(H.canonical(q))
  for p in (fetch,*fetch.rglob('*')):
   if p.is_symlink():
    if not p.resolve().is_relative_to(fetch.resolve()):raise ValueError('escaping Node stage symlink')
    os.lchown(p,R.UID,R.GID)
   else:os.chown(p,R.UID,R.GID)
  proof=stage/'proof'
  if proof.exists() or proof.is_symlink():
   st=proof.lstat()
   if not stat.S_ISDIR(st.st_mode) or st.st_uid not in (0,R.UID) or st.st_mode&0o077:raise ValueError('unsafe prior Node proof directory')
   os.rename(proof,stage/('prior-proof-'+str(uuid.uuid4())))
  proof=safe_dir(proof);os.chown(proof,R.UID,R.GID)
  R.run(['/usr/bin/mount','--make-rprivate','/']);R.run(['/usr/bin/mount','-t','tmpfs','-o','mode=0755,size=16m,nosuid,nodev','node-staging','/tmp'])
  sources={'node-tools':stage/'tools','node-runtime':runtime/'runtime','node-fetch':fetch,'node-proof':proof,'node-source':sourcecopy,'node-dependency.sock':R.job_path(job)/'node-dependency.sock'}
  for name,source in sources.items():
   target=Path('/tmp')/name
   if source.is_dir():target.mkdir()
   else:target.touch()
   R.run(['/usr/bin/mount','--bind',str(source),str(target)])
  sandbox(R,job,stage,'resolve',True)
  for name in ('resolved-lock.json','resolved-manifest.json'):
   st=R.regular(fetch/name)
   if st.st_size>8*1024**2:raise Unsupported('Node manifest/lock bound exceeded')
  raw=(fetch/'resolved-lock.json').read_bytes();manifest=(fetch/'resolved-manifest.json').read_bytes();H.V.validate_lock(manifest,raw)
  if (stage/'lock.json').exists() and (stage/'lock.json').read_bytes()!=raw:raise ValueError('Node resolved lock changed after sealing')
  if not (stage/'lock.json').exists():
   temporary=stage/'lock.pending'
   with temporary.open('wb') as out:out.write(raw);out.flush();os.fsync(out.fileno())
   temporary.chmod(0o600);os.replace(temporary,stage/'lock.json');sync_directory(stage)
  raw=(stage/'lock.json').read_bytes();shutil.copyfile(stage/'lock.json',fetch/'sealed-lock.json');os.chown(fetch/'sealed-lock.json',R.UID,R.GID)
  semantic=dict(source_tree_sha256=H.sha(H.canonical(H.inventory(fetch/'project'))) if q.get('package_json') else '',requirements=q['requirements'],package_json=q.get('package_json',''),package_lock=q.get('package_lock',''),runtime_digest=rt['key'],helper_hashes=ident['helper_hashes'],manifest_sha256=H.sha(manifest),lock_sha256=H.sha(raw),modules=q['modules'],binaries=q['binaries'])
  key=H.sha(H.canonical(semantic));r.update(input_key=key,runtime_digest=rt['key'],lock_sha256=H.sha(raw))
  if q.get('package_json'):
   if R.regular(fetch/'source-lock.json').st_size>8*1024**2:raise Unsupported('source lock bound exceeded')
   r.update(source_manifest_sha256=H.sha(manifest),source_lock_sha256=H.sha((fetch/'source-lock.json').read_bytes()))
  index=cache/(key+'.json')
  if index.exists():
   saved=R.completion_json(index)
   selected,_=lookup_by_keys(R,saved['bundle_key'],key,r['lock_sha256'],rt['key'])
   saved_manifest=json.loads((selected/'manifest.json').read_text())
   if saved_manifest['semantic']!=semantic:raise ValueError('cached Node semantic identity differs')
   r.update(bundle_key=saved['bundle_key'],probe_sha256=saved_manifest['probe_sha256'],state='verified')
   R.completion_write(stage/'receipt.json',r);return 0
  qinput=dict(q,input_key=key);(fetch/'input.json').write_bytes(H.canonical(qinput));os.chown(fetch/'input.json',R.UID,R.GID)
  sandbox(R,job,stage,'fetch',True)
  sandbox(R,job,stage,'install',False)
  before=H.inventory(fetch/'project')
  sandbox(R,job,stage,'probe',False)
  if H.inventory(fetch/'project')!=before:raise ValueError('Node probe changed installation')
  probe=R.regular(proof/'probe.json')
  if probe.st_size>16384:raise ValueError('Node probe receipt bound')
  observed=json.loads((proof/'probe.json').read_text())
  if observed!=dict(modules=q['modules'],binaries=q['binaries'],network='unshared-no-socket',input_key=key):raise ValueError('Node probe identity differs')
  r['probe_sha256']=R.digest_file(proof/'probe.json')
  pending=cache/('.pending-'+job)
  if pending.exists():shutil.rmtree(pending)
  payload=pending/'payload';payload.mkdir(parents=True)
  for name in ('project','cache'):shutil.copytree(fetch/name,payload/name,symlinks=True)
  rows=H.inventory(payload)
  m=dict(schema_version=1,kind='node-project-runtime',policy=H.POLICY,input_key=key,runtime_digest=rt['key'],lock_sha256=r['lock_sha256'],probe_sha256=r['probe_sha256'],files=rows,semantic=semantic)
  for source_field in ('source_manifest_sha256','source_lock_sha256'):
   if source_field in r:m[source_field]=r[source_field]
  bkey=H.sha(H.canonical(m));m['key']=bkey;(pending/'manifest.json').write_bytes(H.canonical(m))
  for p in (pending,*pending.rglob('*')):
   if p.is_symlink():os.lchown(p,0,0);continue
   os.chown(p,0,0);p.chmod(0o555 if p.is_dir() or p.stat().st_mode&0o111 else 0o444)
  sync_tree(pending)
  if (cache/bkey).exists():shutil.rmtree(pending)
  else:os.rename(pending,cache/bkey)
  sync_directory(cache)
  r.update(state='verified',bundle_key=bkey,verified_at=time.time())
  lookup_by_keys(R,bkey,key,r['lock_sha256'],rt['key'])
  if (cache/(key+'.json')).exists():raise ValueError('Node publication index unexpectedly changed under provision lock')
  R.completion_write(cache/(key+'.json'),{k:r[k] for k in ('input_key','bundle_key','runtime_digest','lock_sha256','probe_sha256','source_manifest_sha256','source_lock_sha256') if k in r})
  R.completion_write(stage/'receipt.json',r)
  return 0
 except Exception as e:
  unsupported=isinstance(e,(Unsupported,H.Unsupported,ValueError))
  r.update(state='unavailable' if unsupported else 'waiting',unsupported=unsupported,reason=str(e)[:400],retry_at=time.time()+120)
  if getattr(e,'diagnostic',None):r['diagnostic']=e.diagnostic
  R.completion_write(stage/'receipt.json',r);return 1

def lookup_by_keys(R,bundle_key,input_key,lock_sha256,runtime_digest,full=True):
 """Exact trusted-test selection; returns (project bundle, tooling bundle)."""
 for key in (bundle_key,input_key,lock_sha256,*([runtime_digest] if runtime_digest is not None else [])):
  if not isinstance(key,str) or not HEX.fullmatch(key):raise ValueError('invalid selected Node runtime key')
 root=R.DEPENDENCIES/'node-project'/bundle_key;p=root/'manifest.json';st=R.regular(p)
 if st.st_uid!=0 or st.st_mode&0o222 or st.st_size>32*1024**2:raise ValueError('unsafe selected Node manifest')
 m=json.loads(p.read_text());body=dict(m);body.pop('key',None)
 if runtime_digest is None:runtime_digest=m.get('runtime_digest','')
 if not isinstance(runtime_digest,str) or not HEX.fullmatch(runtime_digest):raise ValueError('invalid manifest Node runtime identity')
 if hashlib.sha256(json.dumps(body,sort_keys=True,separators=(',',':')).encode()).hexdigest()!=bundle_key or m.get('key')!=bundle_key or any(m.get(k)!=v for k,v in {'input_key':input_key,'lock_sha256':lock_sha256,'runtime_digest':runtime_digest}.items()):raise ValueError('selected Node environment identity mismatch')
 runtime,_=tooling(R,runtime_digest,full)
 if full:verify_inventory(R,root/'payload',m['files'])
 return root,runtime


def validation_failure(R,job,error):
 stage,q=request(R,job);r=R.completion_json(stage/'receipt.json')
 r.update(state='unavailable',unsupported=True,executed=False,scope='full content validation before model execution',diagnostic='missing' if isinstance(error,FileNotFoundError) else 'integrity',reason='Selected Node runtime failed full validation: '+str(error)[:300])
 R.completion_write(stage/'receipt.json',r);R.completion_write(R.job_path(job)/'node-runtime.json',r)
 return r
