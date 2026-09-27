#!/usr/bin/python3
"""Trusted private integration transactions; untrusted code runs only isolated.

Private publication is distinct from canonical promotion, deployment and public
publication. The controller owns semantic authorization; this helper authenticates
bytes, execution observations and compare-and-swap transactions.
"""
import base64
import datetime
import fcntl
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import pwd
import shutil
import stat
import subprocess
import time
from types import SimpleNamespace
import uuid

R=None
MAX_TREE=1900*1024**2
MAX_FILES=100000
CHECK_PROFILE={'seconds':600,'scratch_bytes':2*1024**3,'output_bytes':16*1024**2,'memory_bytes':2*1024**3,'cpu_percent':200,'tasks':256}
TERMINAL={'prepared','sealed','copied','published_private','rolled_back','rejected','exited','timeout','output_limit','cancelled','interrupted','unavailable','base_advanced'}
PHASES={'prepare','audit','review','seal','check','publish','consume','rollback'}

def canonical(value):return json.dumps(value,sort_keys=True,separators=(',',':')).encode()
def sha(data):return hashlib.sha256(data).hexdigest()
def now():return datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00','Z')
def key(value):return isinstance(value,str) and len(value)==64 and all(c in '0123456789abcdef' for c in value)
def oid(value):return isinstance(value,str) and len(value) in (40,64) and all(c in '0123456789abcdef' for c in value)
def unique(pairs):
 out={}
 for k,v in pairs:
  if k in out:raise ValueError('duplicate integration JSON key')
  out[k]=v
 return out

def rootdir(path,mode=0o700):
 path.mkdir(mode=mode,exist_ok=True)
 st=path.lstat()
 if not stat.S_ISDIR(st.st_mode) or st.st_uid!=os.geteuid() or st.st_mode&0o022:raise ValueError('unsafe private integration directory')
 return path

def record(path,limit=256*1024):
 st=R.regular(path)
 if st.st_uid!=os.geteuid() or st.st_mode&0o022 or st.st_size>limit:raise ValueError('unsafe integration record')
 return json.loads(path.read_bytes(),object_pairs_hook=unique)

def write(path,value):R.completion_write(path,value)
def seal(path,value):
 value=dict(value);value.pop('receipt_sha256',None);body=dict(value);body.pop('generation',None);value['receipt_sha256']=sha(canonical(body));write(path,value);return value

def receipt(path):
 if not path.exists():return None
 value=record(path)
 if value.get('state') in TERMINAL:
  body=dict(value);expected=body.pop('receipt_sha256',None);body.pop('generation',None)
  if expected!=sha(canonical(body)):raise ValueError('integration receipt checksum mismatch')
 return value

def root():return rootdir(R.INTEGRATION_ROOT)
def attempt(integration):
 if not key(integration):raise ValueError('integration ID must be SHA256')
 return rootdir(rootdir(root()/'runs')/integration)
def project(project_id):
 if type(project_id) is not int or project_id<=0:raise ValueError('positive registered project ID required')
 return rootdir(rootdir(root()/'projects')/str(project_id))
def generation(value):
 if type(value) is not int or not 1<=value<=2**31-1:raise ValueError('invalid integration generation')
 return value

def checked_path(value):
 if not isinstance(value,str) or not value or len(value.encode())>4096 or value.startswith('/') or '\\' in value or '\0' in value or any(p in ('','.','..') for p in value.split('/')):raise ValueError('unsafe integration path')
 if any(p.lower()=='.git' for p in value.split('/')):raise ValueError('Git metadata is not project content')
 return value

def scopes(values):
 if not isinstance(values,list) or not 1<=len(values)<=128:raise ValueError('integration scope must be bounded')
 result=[]
 for value in values:
  base=value[:-3] if value.endswith('/**') else value
  checked_path(base)
  if '*' in base or '?' in base or '[' in base:raise ValueError('only literal trailing /** scope is supported')
  if value in result or any(allowed(base,[old]) or (value.endswith('/**') and allowed(old[:-3] if old.endswith('/**') else old,[value])) for old in result):raise ValueError('overlapping integration scope')
  result.append(value)
 return sorted(result)
def allowed(path,values):return any(path==v or (v.endswith('/**') and (path==v[:-3] or path.startswith(v[:-2]))) for v in values)

def request(job,integration,phase,check=None):
 if phase not in PHASES:raise ValueError('unsupported integration phase')
 owner=R.job_path(job);run=attempt(integration)
 directory=owner/'integration-requests';st=directory.lstat()
 if not stat.S_ISDIR(st.st_mode) or st.st_uid not in (0,pwd.getpwnam('admin').pw_uid) or st.st_mode&0o022:raise ValueError('unsafe integration request directory')
 if phase=='check' and not key(check):raise ValueError('check ID must be SHA256')
 suffix='check-'+check if phase=='check' else phase
 path=directory/(integration+'.'+suffix+'.json');st=R.regular(path)
 if st.st_uid not in (0,pwd.getpwnam('admin').pw_uid) or st.st_mode&0o077 or st.st_size>(1024*1024 if phase=='check' else 256*1024):raise ValueError('unsafe integration request file')
 raw=path.read_bytes();data=json.loads(raw,object_pairs_hook=unique);digest=sha(raw)
 if data.get('schema_version')!=1 or data.get('integration_id')!=integration or data.get('owner_job')!=job:raise ValueError('integration request ownership mismatch')
 for name in ('pin_key','root_id'):
  if not key(data.get(name)):raise ValueError('invalid integration pin')
 for name in ('owner_task','project_id'):
  if type(data.get(name)) is not int or data[name]<=0:raise ValueError('invalid integration task/project')
 if phase=='check' and check!=digest:raise ValueError('check ID/request mismatch')
 binding={name:data[name] for name in ('integration_id','pin_key','root_id','project_id')}
 with (run/'identity.lock').open('a') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX)
  if (run/'identity.json').exists():
   if record(run/'identity.json')!=binding:raise ValueError('integration attempt identity changed')
  else:write(run/'identity.json',binding)
 operation=sha((phase+':'+job+':'+digest).encode());stage=rootdir(rootdir(run/'operations')/operation)
 frozen=stage/'request.json'
 if frozen.exists():
  if frozen.read_bytes()!=raw:raise ValueError('sealed integration request changed')
 else:
  fd=os.open(frozen,os.O_CREAT|os.O_EXCL|os.O_WRONLY|os.O_NOFOLLOW,0o600)
  with os.fdopen(fd,'wb') as out:out.write(raw);out.flush();os.fsync(out.fileno())
 base={name:data[name] for name in ('integration_id','pin_key','root_id','owner_job','owner_task','project_id')}
 base.update(schema_version=1,phase=phase,request_sha256=digest,operation_id=operation)
 return run,stage,data,base

def unit(integration,operation):return 'lectern-private-integration-'+integration+'-'+operation+'.service'
def revoked(run):return record(run/'revoked.json')['generation'] if (run/'revoked.json').exists() else 0

def git(repository,args,input=None,limit=32*1024**2):
 command=['/usr/bin/git','--git-dir='+str(repository),'-c','core.hooksPath=/dev/null','-c','core.fsmonitor=false','-c','protocol.allow=never','-c','gc.auto=0','-c','core.fsync=all','-c','core.fsyncMethod=fsync',*args]
 environment={'PATH':'/usr/bin:/bin','HOME':'/nonexistent','GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null','GIT_NO_REPLACE_OBJECTS':'1','GIT_NO_LAZY_FETCH':'1','GIT_TERMINAL_PROMPT':'0','GIT_OPTIONAL_LOCKS':'0','LC_ALL':'C'}
 process=subprocess.Popen(command,stdin=subprocess.PIPE if input is not None else subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,env=environment,close_fds=True)
 # Inputs are bounded raw object/commit/index data; no submitted Git arguments.
 try:out,err=process.communicate(input,timeout=120)
 except BaseException:
  process.kill();process.wait();raise
 if len(out)>limit or len(err)>65536:raise ValueError('Git output limit exceeded')
 if process.returncode:raise RuntimeError('private Git operation failed: '+err.decode(errors='replace')[-500:])
 return out

def git_init(repository,object_format='sha1'):
 if not repository.exists():
  repository.mkdir(mode=0o700)
  subprocess.run(['/usr/bin/git','init','--quiet','--bare','--object-format='+object_format,str(repository)],check=True,capture_output=True,env={'PATH':'/usr/bin:/bin','HOME':'/nonexistent','GIT_CONFIG_NOSYSTEM':'1','GIT_CONFIG_GLOBAL':'/dev/null'},timeout=15)
 return repository

def canonical_object_view(repo_path,commit,stage):
 if not oid(commit) or not isinstance(repo_path,str) or not repo_path.startswith('/') or '\0' in repo_path:raise ValueError('invalid registered committed source')
 repository=Path(repo_path).resolve(strict=True)
 metadata=repository if (repository/'objects').is_dir() and (repository/'HEAD').is_file() else repository/'.git'
 if metadata.is_file():
  raw=metadata.read_bytes()
  if len(raw)>4096 or not raw.startswith(b'gitdir: ') or b'\0' in raw:raise ValueError('invalid registered Git metadata')
  metadata=(repository/os.fsdecode(raw[8:].strip())).resolve(strict=True)
 if not metadata.is_dir():raise ValueError('registered source lacks Git metadata')
 common=metadata
 if (metadata/'commondir').exists():
  raw=(metadata/'commondir').read_bytes()
  if len(raw)>4096 or b'\0' in raw:raise ValueError('invalid registered Git common directory')
  common=(metadata/os.fsdecode(raw.strip())).resolve(strict=True)
 objects=common/'objects'
 if not objects.is_dir():raise ValueError('registered source lacks object storage')
 # A fresh root-owned Git metadata view prevents source config, attributes,
 # hooks, replace refs, credentials and worktree/index from influencing reads.
 view=git_init(stage/'object-view','sha256' if len(commit)==64 else 'sha1')
 alternate=view/'objects/info/alternates';alternate.write_text(str(objects.resolve())+'\n');alternate.chmod(0o400)
 actual=git(view,['rev-parse','--verify',commit+'^{commit}'],limit=128).decode().strip()
 if actual!=commit:raise ValueError('registered revision is not an exact commit')
 return view

def observe_canonical(destination,stage):
 pinned=destination.get('canonical_commit') or (destination.get('commit','') if destination.get('base_kind')=='canonical' else '')
 result={'canonical_commit':pinned,'observed_canonical_commit':'','canonical_observation':'unavailable','canonical_base_advanced':False}
 try:
  repository=Path(destination['repo_path']).resolve(strict=True);metadata=repository/'.git'
  if (repository/'objects').is_dir():metadata=repository
  if metadata.is_file():
   raw=metadata.read_bytes()
   if len(raw)>4096 or not raw.startswith(b'gitdir: '):raise ValueError('invalid Git metadata')
   metadata=(repository/os.fsdecode(raw[8:].strip())).resolve(strict=True)
  common=metadata
  if (metadata/'commondir').exists():
   raw=(metadata/'commondir').read_bytes()
   if len(raw)>4096:raise ValueError('invalid Git common directory')
   common=(metadata/os.fsdecode(raw.strip())).resolve(strict=True)
  def small(path,limit):
   info=R.regular(path)
   if info.st_size>limit:raise ValueError('Git ref metadata exceeds bound')
   return path.read_text().strip()
  head=small(metadata/'HEAD',4096)
  if head.startswith('ref: '):
   ref=head[5:];checked_path(ref)
   if not ref.startswith('refs/heads/'):raise ValueError('unexpected canonical HEAD ref')
   path=common/ref
   if path.exists():head=small(path,128)
   else:
    matches=[line.split()[0] for line in small(common/'packed-refs',8*1024**2).splitlines() if not line.startswith(('#','^')) and len(line.split())==2 and line.split()[1]==ref]
    if len(matches)!=1:raise ValueError('canonical ref is unavailable')
    head=matches[0]
  if not oid(head):raise ValueError('canonical HEAD is not a commit identity')
  canonical_object_view(str(repository),head,rootdir(stage/'canonical-observation'))
  result.update(observed_canonical_commit=head,canonical_observation='observed',canonical_base_advanced=bool(pinned and head!=pinned))
 except (OSError,ValueError,RuntimeError,KeyError):pass
 return result

def valid_link(path,target):
 if not target or target.startswith('/') or '\0' in target or '\\' in target:raise ValueError('escaping integration symlink')
 pieces=path.split('/')[:-1]
 for piece in target.split('/'):
  if piece in ('','.'):continue
  if piece=='..':
   if not pieces:raise ValueError('escaping integration symlink')
   pieces.pop()
  else:pieces.append(piece)
 if any(piece.lower()=='.git' for piece in pieces):raise ValueError('symlink targets Git metadata')

def inventory(directory):
 rows=[];total=0
 for item in sorted(directory.rglob('*')):
  relative=item.relative_to(directory).as_posix();checked_path(relative);info=item.lstat()
  if stat.S_ISDIR(info.st_mode):continue
  if stat.S_ISLNK(info.st_mode):
   target=os.readlink(item);valid_link(relative,target);data=os.fsencode(target);mode='120000'
  elif stat.S_ISREG(info.st_mode) and info.st_nlink==1:
   if info.st_size>256*1024**2:raise ValueError('integration file exceeds256MiB')
   data=item.read_bytes();mode='100755' if info.st_mode&0o111 else '100644'
  else:raise ValueError('integration tree contains special or hardlinked file')
  total+=len(data)
  if len(rows)>=MAX_FILES or total>MAX_TREE:raise ValueError('integration tree size/count limit')
  rows.append({'path':relative,'mode':mode,'bytes':len(data),'sha256':sha(data)})
 return rows,sha(canonical(rows))

def export_tree(repository,commit,destination):
 if destination.exists():shutil.rmtree(destination)
 destination.mkdir(mode=0o755)
 tree=git(repository,['rev-parse','--verify',commit+'^{tree}'],limit=128).decode().strip()
 if not oid(tree):raise ValueError('invalid committed tree')
 listing=git(repository,['ls-tree','-r','-z','--full-tree',tree])
 entries=[entry for entry in listing.split(b'\0') if entry]
 if len(entries)>MAX_FILES:raise ValueError('committed tree entry limit')
 parsed=[];seen=set()
 for entry in entries:
  header,name=entry.split(b'\t',1);mode,kind,object_id=header.decode('ascii').split();name=name.decode('utf-8');checked_path(name)
  if name in seen:raise ValueError('duplicate committed path')
  seen.add(name);parsed.append((name,mode,kind,object_id))
 for name in seen:
  if any('/'.join(name.split('/')[:i]) in seen for i in range(1,len(name.split('/')))):raise ValueError('committed file/directory collision')
 total=0
 for name,mode,kind,object_id in parsed:
  if kind!='blob' or mode not in ('100644','100755','120000') or not oid(object_id):raise ValueError('unsupported committed object mode')
  size=int(git(repository,['cat-file','-s',object_id],limit=64).strip())
  total+=size
  if size>256*1024**2 or total>MAX_TREE:raise ValueError('committed content size limit')
  data=git(repository,['cat-file','blob',object_id],limit=256*1024**2)
  if len(data)!=size or hashlib.new('sha256' if len(object_id)==64 else 'sha1',b'blob '+str(size).encode()+b'\0'+data).hexdigest()!=object_id:raise ValueError('committed blob identity mismatch')
  target=destination/name;target.parent.mkdir(parents=True,exist_ok=True)
  if mode=='120000':
   link=data.decode('utf-8');valid_link(name,link);target.symlink_to(link)
  else:target.write_bytes(data);target.chmod(0o755 if mode=='100755' else 0o644)
 rows,digest=inventory(destination)
 return tree,rows,digest

def freeze_tree(directory):
 for item in directory.rglob('*'):
  info=item.lstat()
  if stat.S_ISDIR(info.st_mode):item.chmod(0o555)
  elif stat.S_ISREG(info.st_mode):
   item.chmod(0o555 if info.st_mode&0o111 else 0o444)
   with item.open('rb') as content:os.fsync(content.fileno())
 for item in sorted(directory.rglob('*'),reverse=True):
  if stat.S_ISDIR(item.lstat().st_mode):fsync_directory(item)
 directory.chmod(0o555);fsync_directory(directory)

def copy_tree(source,destination):
 if destination.exists():
  if destination.is_symlink():raise ValueError('linked integration destination')
  for item in destination.iterdir():
   if item.is_dir() and not item.is_symlink():shutil.rmtree(item)
   else:item.unlink()
 else:destination.mkdir(parents=True)
 shutil.copytree(source,destination,symlinks=True,dirs_exist_ok=True)
 for item in (destination,*destination.rglob('*')):
  info=item.lstat()
  if stat.S_ISDIR(info.st_mode):item.chmod(0o755)
  elif stat.S_ISREG(info.st_mode):item.chmod(0o755 if info.st_mode&0o111 else 0o644)
 return destination

def trusted_tree(run,name):
 path=run/(name+'.json');value=record(path,32*1024**2)
 tree=run/name
 _,actual=inventory(tree)
 if actual!=value['tree_sha256']:raise ValueError('immutable integration tree changed')
 return tree,value

RESERVED={'autonomy-report.json','.lectern-review','.lectern-reports'}

def selected_archive(job,kind,expected):
 R.job_path(job)
 if not key(expected):raise ValueError('archive SHA256 required')
 if kind=='raw':
  if R.archive_identity(job)['sha256']!=expected:raise ValueError('source archive differs from admitted identity')
  archive=R.job_path(job)/'artifact.tar.gz'
 elif kind=='derived':
  value=R.completion_ready(R.completion_stage(job))
  if value.get('state')!='ready' or value.get('derived_archive_sha256')!=expected:raise ValueError('derived source differs from reviewed identity')
  archive=R.job_path(job)/'completion-artifact.tar.gz'
 else:raise ValueError('unsupported source archive kind')
 return archive

def unpack(archive,expected,target):
 if target.exists():shutil.rmtree(target)
 R.evidence_extract_verified_archive(archive,expected,target)
 return target

def source_evidence(data,target):
 source=data['source'];job=source['job'];reviewer=source['review_job']
 rootdir(target)
 unpack(selected_archive(job,source['archive_kind'],source['archive_sha256']),source['archive_sha256'],target/'source')
 report=target/'source/autonomy-report.json'
 # Documentary derived exports intentionally carry no current submission.
 if source['archive_kind']=='derived':
  raw_sha=source.get('raw_archive_sha256');selected_archive(job,'raw',raw_sha)
  submission=R.archive_report_read(job,raw_sha)
  if submission['report_sha256']!=source['report_sha256']:raise ValueError('derived source original submission differs')
  write(target/'source-submission.json',submission)
 if source['archive_kind']=='raw':
  if not report.is_file() or report.is_symlink() or R.digest_file(report)!=source['report_sha256']:raise ValueError('archived source report identity mismatch')
 unpack(selected_archive(reviewer,'raw',source['review_archive_sha256']),source['review_archive_sha256'],target/'reviewer')
 review_report=target/'reviewer/autonomy-report.json'
 if review_report.is_symlink() or not review_report.is_file() or R.digest_file(review_report)!=source.get('review_report_sha256'):raise ValueError('source reviewer report identity mismatch')
 for path in (target/'source',target/'reviewer'):freeze_evidence(path)
 write(target/'manifest.json',{'source':source,'purpose':'immutable untrusted source and review evidence; not new approval'})

def freeze_evidence(path):
 # Evidence may contain arbitrary literal symlink targets, unlike publication
 # content. Never follow those targets for ownership, modes, reads or copies.
 for item in (path,*path.rglob('*')):
  info=item.lstat()
  if stat.S_ISDIR(info.st_mode):item.chmod(0o555)
  elif stat.S_ISREG(info.st_mode):item.chmod(0o555 if info.st_mode&0o111 else 0o444)

def mutable_work(job):
 R.completion_stage(job,create=True)
 return R.completion_archive_volume(job)

def admin_work(work):
 admin=pwd.getpwnam('admin')
 for item in (work,*work.rglob('*')):os.chown(item,admin.pw_uid,admin.pw_gid,follow_symlinks=False)

def base_material(run,stage,data):
 destination=data['destination'];project_id=data['project_id'];commit=destination['commit']
 if not oid(commit) or not oid(destination['tree_oid']) or destination['base_kind'] not in ('canonical','private'):raise ValueError('invalid pinned destination')
 expected=destination.get('expected_managed_commit','')
 if expected and not oid(expected):raise ValueError('invalid managed compare-and-swap base')
 if (run/'base.json').exists():
  base,meta=trusted_tree(run,'base')
  if meta['pin_key']!=data['pin_key'] or meta['base_commit']!=commit or meta['scope']!=scopes(data['paths']) or meta['destination']!=destination or meta['source']!=data['source']:raise ValueError('integration base pin changed')
  return base,meta
 base=run/'base'
 if destination['base_kind']=='private':
  publication=publication_for(destination['base_publication_id'])
  if publication['receipt_sha256']!=destination.get('base_publication_receipt_sha256') or publication['project_id']!=project_id or publication['commit']!=commit or publication['tree_oid']!=destination['tree_oid'] or expected!=commit:raise ValueError('managed base publication mismatch')
  repository=project(project_id)/'repo.git'
 else:
  if expected:raise ValueError('canonical-only base cannot replace prior cumulative private tip')
  repository=canonical_object_view(destination['repo_path'],commit,stage)
 tree,rows,digest=export_tree(repository,commit,base)
 if tree!=destination['tree_oid']:raise ValueError('pinned destination tree differs')
 if any((base/name).exists() or (base/name).is_symlink() for name in RESERVED):raise ValueError('destination uses reserved workshop transport path')
 freeze_tree(base)
 meta={'pin_key':data['pin_key'],'base_commit':commit,'base_kind':destination['base_kind'],'tree_oid':tree,'tree_sha256':digest,'scope':scopes(data['paths']),'rows':rows,'destination':destination,'source':data['source']}
 # Retain canonical committed input separately when cumulative private base is
 # selected. It is evidence for adaptation, never silently substituted.
 observed=destination.get('canonical_commit')
 if destination['base_kind']=='private' and observed:
  view=canonical_object_view(destination['repo_path'],observed,rootdir(stage/'canonical-input'))
  tree,rows,digest=export_tree(view,observed,run/'canonical-observed');freeze_tree(run/'canonical-observed')
  write(run/'canonical-observed.json',{'base_commit':observed,'tree_oid':tree,'tree_sha256':digest,'rows':rows})
 write(run/'base.json',meta)
 return base,meta

def prepare_operation(run,stage,data,value,audit=False):
 base,meta=base_material(run,stage,data)
 evidence=run/'evidence'
 if not (evidence/'manifest.json').exists():
  if evidence.exists():shutil.rmtree(evidence)
  source_evidence(data,evidence)
 elif record(evidence/'manifest.json')['source']!=data['source']:raise ValueError('integration evidence pin changed')
 work=mutable_work(data['owner_job'])
 target=work/'.lectern-review'/('integration-'+data['integration_id'])
 if audit:
  if target.exists():shutil.rmtree(target)
  target.mkdir(parents=True)
  shutil.copytree(base,target/'base',symlinks=True)
  shutil.copytree(evidence,target/'source-evidence',symlinks=True)
 else:
  if (R.job_path(data['owner_job'])/'job.json').exists():raise ValueError('integration preparation requires fresh worker')
  copy_tree(base,work);target.mkdir(parents=True)
  shutil.copytree(base,target/'base',symlinks=True)
  shutil.copytree(evidence,target/'source-evidence',symlinks=True)
 if (run/'canonical-observed').exists():shutil.copytree(run/'canonical-observed',target/'canonical-observed',symlinks=True)
 freeze_evidence(target);admin_work(work)
 value.update(state='copied' if audit else 'prepared',base_commit=meta['base_commit'],base_tree_oid=meta['tree_oid'],base_tree_sha256=meta['tree_sha256'],scope_sha256=sha(canonical(meta['scope'])),source_archive_sha256=data['source']['archive_sha256'],source_review_archive_sha256=data['source']['review_archive_sha256'])
 if not audit and not (run/'prepare.json').exists():seal(run/'prepare.json',value)
 return seal(stage/'receipt.json',value)

def git_tree_from_files(repository,directory):
 rows,digest=inventory(directory);nodes={}
 for row in rows:
  path=directory/row['path'];content=os.fsencode(os.readlink(path)) if row['mode']=='120000' else path.read_bytes()
  blob=git(repository,['hash-object','-w','--stdin'],input=content,limit=128).decode().strip()
  cursor=nodes;parts=row['path'].split('/')
  for part in parts[:-1]:cursor=cursor.setdefault(part,{})
  cursor[parts[-1]]=(row['mode'],blob)
 def tree(node):
  contents=[]
  for name,item in sorted(node.items()):
   mode,object_id,kind=('040000',tree(item),'tree') if isinstance(item,dict) else (item[0],item[1],'blob')
   contents.append((mode+' '+kind+' '+object_id+'\t').encode()+name.encode()+b'\0')
  return git(repository,['mktree','-z'],input=b''.join(contents),limit=128).decode().strip()
 return tree(nodes),rows,digest

def make_commit(repository,tree,parent,message):
 # Synthetic private commits use a declared fixed metadata epoch; actual
 # preparation/publication times live in authenticated journals and receipts.
 raw=('tree '+tree+'\n'+('parent '+parent+'\n' if parent else '')+'author Lectern private integration <private@localhost> 0 +0000\ncommitter Lectern private integration <private@localhost> 0 +0000\n\n'+message+'\n').encode()
 return git(repository,['hash-object','-t','commit','-w','--stdin'],input=raw,limit=128).decode().strip()

def candidate_archive(directory,archive):
 import gzip,tarfile
 temporary=archive.with_suffix('.pending')
 with temporary.open('wb') as output,gzip.GzipFile(fileobj=output,mode='wb',mtime=0) as compressed,tarfile.open(fileobj=compressed,mode='w') as tar:
  for path in (directory,*sorted(directory.rglob('*'))):
   name='work' if path==directory else 'work/'+path.relative_to(directory).as_posix()
   info=tar.gettarinfo(str(path),arcname=name);info.uid=info.gid=0;info.uname=info.gname='';info.mtime=0
   if info.isfile():
    with path.open('rb') as content:tar.addfile(info,content)
   else:tar.addfile(info)
 temporary.chmod(0o400)
 with temporary.open('rb') as content:os.fsync(content.fileno())
 os.replace(temporary,archive);fsync_directory(archive.parent)
 return R.digest_file(archive)

def seal_operation(run,stage,data,value):
 base,meta=trusted_tree(run,'base');prepared=record(run/'prepare.json')
 if data['prepare_request_sha256']!=prepared['request_sha256'] or data['expected_base_tree_sha256']!=meta['tree_sha256']:raise ValueError('candidate preparation binding differs')
 archive=selected_archive(data['owner_job'],'raw',data['builder_archive_sha256'])
 submitted=unpack(archive,data['builder_archive_sha256'],stage/'submitted')
 for name in RESERVED:
  item=submitted/name
  if item.is_dir() and not item.is_symlink():shutil.rmtree(item)
  elif item.exists() or item.is_symlink():item.unlink()
 rows,digest=inventory(submitted);before={row['path']:row for row in meta['rows']};after={row['path']:row for row in rows}
 changes=[{'path':name,'before':before.get(name),'after':after.get(name)} for name in sorted(before.keys()|after.keys()) if before.get(name)!=after.get(name)]
 for change in changes:
  if not allowed(change['path'],meta['scope']):raise ValueError('candidate changed path outside audited scope: '+change['path'])
 if (run/'candidate.json').exists():
  old=record(run/'candidate.json',32*1024**2)
  if old['tree_sha256']!=digest:raise ValueError('attempt already sealed different candidate; reserve a new attempt')
  if not (run/'sealed.json').exists():seal(run/'sealed.json',old['seal_values'])
  existing=receipt(run/'sealed.json')
  return seal(stage/'receipt.json',dict(value,**{k:v for k,v in existing.items() if k not in value and k not in ('receipt_sha256','state')},state='sealed'))
 repository=git_init(project(data['project_id'])/'repo.git','sha256')
 with (project(data['project_id'])/'objects.lock').open('a') as guard:
  fcntl.flock(guard,fcntl.LOCK_EX)
  tree,_,_=git_tree_from_files(repository,submitted)
  if meta['base_kind']=='private':parent=meta['base_commit']
  else:
   base_tree,_,_=git_tree_from_files(repository,base)
   parent=make_commit(repository,base_tree,'','Synthetic raw source anchor; canonical provenance '+meta['base_commit'])
  commit=parent if not changes else make_commit(repository,tree,parent,'Private integration '+data['integration_id']+'; pin '+data['pin_key'])
 if (run/'candidate').exists():shutil.rmtree(run/'candidate')
 os.replace(submitted,run/'candidate');freeze_tree(run/'candidate')
 manifest={'base_tree_sha256':meta['tree_sha256'],'candidate_tree_sha256':digest,'changes':changes,'scope':meta['scope']}
 write(run/'candidate-manifest.json',manifest)
 archive_sha=candidate_archive(run/'candidate',run/'candidate.tar.gz')
 value.update(state='sealed',no_changes=not changes,changed_paths=len(changes),base_commit=meta['base_commit'],base_tree_oid=meta['tree_oid'],base_tree_sha256=meta['tree_sha256'],candidate_tree_sha256=digest,candidate_tree_oid=tree,candidate_commit=commit,candidate_archive_sha256=archive_sha,candidate_manifest_sha256=sha(canonical(manifest)),prepare_request_sha256=prepared['request_sha256'],builder_archive_sha256=data['builder_archive_sha256'])
 write(run/'candidate.json',{'tree_sha256':digest,'tree_oid':tree,'commit':commit,'parent':parent,'rows':rows,'archive_sha256':archive_sha,'seal_values':value})
 seal(run/'sealed.json',value);return seal(stage/'receipt.json',value)

def candidate_for(run,data):
 value=receipt(run/'sealed.json')
 if not value or data['candidate_receipt_sha256']!=value['receipt_sha256'] or data['candidate_tree_sha256']!=value['candidate_tree_sha256']:raise ValueError('sealed candidate receipt binding mismatch')
 tree,meta=trusted_tree(run,'candidate')
 return tree,meta,value

def review_operation(run,stage,data,value):
 candidate,meta,sealed=candidate_for(run,data)
 work=mutable_work(data['owner_job']);copy_tree(candidate,work)
 evidence=work/'.lectern-review'/('integration-'+data['integration_id']);evidence.mkdir(parents=True)
 shutil.copytree(run/'base',evidence/'base',symlinks=True);shutil.copytree(run/'evidence',evidence/'source-evidence',symlinks=True)
 write(evidence/'candidate.json',sealed);freeze_evidence(evidence);admin_work(work)
 value.update(state='copied',candidate_receipt_sha256=sealed['receipt_sha256'],candidate_tree_sha256=meta['tree_sha256'],candidate_archive_sha256=meta['archive_sha256'],candidate_commit=meta['commit'])
 return seal(stage/'receipt.json',value)

def check_request(data):
 if data.get('profile')!='integration600':raise ValueError('unsupported integration test profile')
 script=data.get('script')
 if not isinstance(script,str) or not script.strip() or '\0' in script or len(script.encode())>128*1024:raise ValueError('invalid bounded test script')
 argv=data.get('argv')
 if not isinstance(argv,list) or len(argv)>32 or any(not isinstance(v,str) or '\0' in v or len(v.encode())>4096 for v in argv) or sum(len(v.encode()) for v in argv)>16384:raise ValueError('invalid bounded test argv')
 fixtures=data.get('fixtures');seen={'main.py'};rows=[];total=0
 if not isinstance(fixtures,list) or len(fixtures)>64:raise ValueError('invalid bounded test fixtures')
 for row in fixtures:
  if not isinstance(row,dict) or set(row)!={'path','content'}:raise ValueError('invalid test fixture')
  name=checked_path(row['path'])
  if name in seen or any(name.startswith(old+'/') or old.startswith(name+'/') for old in seen):raise ValueError('test fixture path collision')
  seen.add(name);content=base64.b64decode(row['content'],validate=True);total+=len(content)
  if total>512*1024:raise ValueError('test fixture size limit')
  rows.append({'path':name,'sha256':sha(content),'bytes':len(content)})
 runtime=data.get('runtime')
 if not isinstance(runtime,dict) or set(runtime)!={'python_bundle_key','python_input_key','browser_key','python_test_key'} or any(v!='' and not key(v) for v in runtime.values()):raise ValueError('invalid test runtime')
 if bool(runtime['python_bundle_key'])!=bool(runtime['python_input_key']) or (runtime['python_bundle_key'] and runtime['python_test_key']) or (runtime['browser_key'] and not runtime['python_bundle_key']):raise ValueError('conflicting test runtime')
 return rows

def check_operation(run,stage,data,value):
 candidate,meta,sealed=candidate_for(run,data);rows=check_request(data)
 expert=R.python_helper(stage/'expert.py');expert.R=R;expert.PROFILE['integration600']=CHECK_PROFILE
 expert.unit=lambda job,probe:unit(data['integration_id'],value['operation_id'])
 expert.seal_receipt=lambda st,v:seal(st/'receipt.json',v)
 value['execution_generation']=value['generation']
 if not expert.fresh_owner(data['owner_job']):raise RuntimeError('reviewer heartbeat unavailable')
 source=stage/'source'
 if source.is_symlink():source.unlink()
 elif source.exists():shutil.rmtree(source)
 source.symlink_to(candidate)
 inputs=stage/'inputs'
 if inputs.exists():shutil.rmtree(inputs)
 inputs.mkdir();(inputs/'main.py').write_text(data['script']);(inputs/'main.py').chmod(0o444)
 for row in data['fixtures']:
  path=inputs/row['path'];path.parent.mkdir(parents=True,exist_ok=True);path.write_bytes(base64.b64decode(row['content'],validate=True));path.chmod(0o444)
 freeze_tree(inputs)
 bundle,browser,runtime=expert.runtime_for(data['owner_job'],data)
 value.update(check_id=value['request_sha256'],candidate_receipt_sha256=sealed['receipt_sha256'],candidate_tree_sha256=meta['tree_sha256'],source_tree_sha256=meta['tree_sha256'],runtime_digest=runtime,runtime=data['runtime'],script_sha256=sha(data['script'].encode()),fixtures_sha256=sha(canonical(sorted(rows,key=lambda row:row['path']))),argv_sha256=sha(canonical(data['argv'])),profile='integration600',limits=CHECK_PROFILE,executed=False,projection_status='readonly sealed candidate mounted at /source and /work; behavior coverage requires semantic review')
 value['launch_policy_sha256']=sha(canonical({'driver':R.digest_file(stage/'helper.py'),'expert':R.digest_file(stage/'expert.py'),'runner':R.digest_file(stage/'runner.py'),'profile':CHECK_PROFILE,'candidate':'readonly','network':'unshared-no-socket'}))
 # Shared helper checks its fixed local cancel marker. Attempt-wide generation
 # revocation is also observed on every capture heartbeat, not just launch.
 fresh=expert.fresh_owner;expert.fresh_owner=lambda job:fresh(job) and value['generation']>revoked(run)
 expert.mount_inputs(stage,data,bundle,browser)
 return expert.capture(data['owner_job'],value['operation_id'],stage,data,bundle,browser,value)

def publication_for(integration):
 value=receipt(attempt(integration)/'publication.json')
 if not value or value['state']!='published_private' or value.get('canonical_changed') is not False or value.get('deployed') is not False:raise ValueError('verified private publication unavailable')
 return value

def ref_read(repository,reference):
 path=repository/reference
 if not path.exists():return ''
 info=R.regular(path)
 if info.st_uid!=os.geteuid() or info.st_mode&0o022 or info.st_size>128:raise ValueError('unsafe managed Git ref')
 value=path.read_text().strip()
 if not oid(value):raise ValueError('invalid managed Git ref')
 return value

def integration_tip(project_id):
 location=project(project_id);repository=location/'repo.git'
 if not repository.exists():return {'state':'absent','project_id':project_id}
 with (location/'publish.lock').open('a') as guard:
  try:fcntl.flock(guard,fcntl.LOCK_SH|fcntl.LOCK_NB)
  except BlockingIOError:return {'state':'publishing','project_id':project_id}
  commit=ref_read(repository,'refs/heads/managed')
  if not commit:return {'state':'absent','project_id':project_id}
  index=location/'commits'/ (commit+'.json')
  if not index.exists():return {'state':'publishing','project_id':project_id,'commit':commit}
  selected=record(index)
  try:published=publication_for(selected['integration_id'])
  except (ValueError,FileNotFoundError):return {'state':'publishing','project_id':project_id,'commit':commit,'integration_id':selected['integration_id']}
  if published['commit']!=commit:raise ValueError('managed tip publication identity mismatch')
  return {'state':'ready','project_id':project_id,'integration_id':published['integration_id'],'commit':commit,'tree_oid':published['tree_oid'],'tree_sha256':published['tree_sha256'],'base_commit':published['base_commit'],'publication_receipt_sha256':published['receipt_sha256']}

def check_receipts(run,data,sealed):
 checks=data.get('checks')
 if not isinstance(checks,list) or not 1<=len(checks)<=32:raise ValueError('publication needs bounded independent executed checks')
 accepted=[]
 for expected in checks:
  if not key(expected.get('check_id')) or not key(expected.get('receipt_sha256')):raise ValueError('invalid check receipt binding')
  found=[]
  for operation in (run/'operations').iterdir():
   path=operation/'receipt.json'
   if path.exists():
    value=receipt(path)
    if value.get('phase')=='check' and value.get('check_id')==expected['check_id']:found.append(value)
  if len(found)!=1:raise ValueError('unique trusted check receipt unavailable')
  value=found[0]
  if value['receipt_sha256']!=expected['receipt_sha256'] or value['owner_task']!=data['owner_task'] or value['candidate_tree_sha256']!=sealed['candidate_tree_sha256'] or value['candidate_receipt_sha256']!=sealed['receipt_sha256'] or value['state']!='exited' or value.get('executed') is not True or value.get('exit_code')!=0 or value.get('truncated') is not False:raise ValueError('check did not independently verify this exact candidate')
  accepted.append(expected)
 return accepted

def fsync_directory(path):
 fd=os.open(path,os.O_RDONLY|os.O_DIRECTORY)
 try:os.fsync(fd)
 finally:os.close(fd)

def publish_operation(run,stage,data,value):
 candidate,meta,sealed=candidate_for(run,data)
 if data['candidate_commit']!=meta['commit'] or not key(data.get('authorization_sha256')):raise ValueError('invalid exact candidate publication authorization')
 checks=check_receipts(run,data,sealed)
 review=data['review']
 if review['job']!=data['owner_job']:raise ValueError('publication reviewer differs from check owner')
 unpack(selected_archive(review['job'],'raw',review['archive_sha256']),review['archive_sha256'],stage/'review')
 report=stage/'review/autonomy-report.json'
 if report.is_symlink() or not report.is_file() or R.digest_file(report)!=review['report_sha256']:raise ValueError('review archive report differs')
 base=record(run/'base.json',32*1024**2);expected=base['destination'].get('expected_managed_commit','')
 if data['expected_managed_commit']!=expected:raise ValueError('publication CAS differs from audited base')
 location=project(data['project_id']);repository=location/'repo.git';reference='refs/lectern/integrations/'+data['integration_id']
 exported=run/'published-source'
 if not exported.exists():shutil.copytree(candidate,exported,symlinks=True);freeze_tree(exported)
 _,export_sha=inventory(exported)
 if export_sha!=meta['tree_sha256']:raise ValueError('private consumer export differs')
 value.update(state='published_private',id=data['integration_id'],git_tree=meta['tree_oid'],parent=base['base_commit'],commit=meta['commit'],tree_oid=meta['tree_oid'],tree_sha256=meta['tree_sha256'],candidate_tree_sha256=meta['tree_sha256'],candidate_receipt_sha256=sealed['receipt_sha256'],base_commit=base['base_commit'],base_tree_sha256=base['tree_sha256'],actual_git_parent=meta['parent'],ref=reference,source_archive_sha256=base['source']['archive_sha256'],review_archive_sha256=review['archive_sha256'],review_report_sha256=review['report_sha256'],checks=checks,authorization_sha256=data['authorization_sha256'],canonical_changed=False,deployed=False,consumer_ready=True,no_changes=sealed.get('no_changes',False),published_at=now(),canonical_pending=True)
 value.update(observe_canonical(base['destination'],stage))
 journal=run/'publication-journal.json'
 if journal.exists():
  prior=record(journal)
  if prior['new_commit']!=meta['commit'] or prior['expected_commit']!=expected:raise ValueError('publication journal changed')
  value=dict(prior['receipt'],generation=value['generation'])
 else:
  write(journal,{'state':'prepared','new_commit':meta['commit'],'expected_commit':expected,'receipt':value});fsync_directory(run)
 with (location/'publish.lock').open('a') as guard:
  fcntl.flock(guard,fcntl.LOCK_EX)
  old_ref=ref_read(repository,reference);head=ref_read(repository,'refs/heads/managed')
  historical=receipt(run/'publication.json')
  if historical:
   if old_ref!=historical['commit']:raise ValueError('immutable publication ref differs')
   return seal(stage/'receipt.json',historical)
  if old_ref:
   if old_ref!=meta['commit']:raise ValueError('immutable integration ref changed')
   # Publication happened, possibly followed by another successful integration.
   # Reconcile history without moving a later managed head backwards.
   value=dict(value,managed_current=head==old_ref,current=False,current_presence='present' if head==old_ref else 'superseded')
  else:
   if head!=expected:return seal(stage/'receipt.json',dict(value,state='base_advanced',reason='managed private tip advanced; audit adaptation against new base',observed_managed_commit=head,consumer_ready=False))
   if value['generation']<=revoked(run):return dict(value,state='waiting',reason='publication generation revoked before CAS',consumer_ready=False)
   write(journal,{'state':'committing','new_commit':meta['commit'],'expected_commit':expected,'receipt':value});fsync_directory(run)
   rootdir(location/'commits');write(location/'commits'/(meta['commit']+'.json'),{'integration_id':data['integration_id'],'commit':meta['commit']})
   zero='0'*len(meta['commit'])
   transaction=('start\ncreate '+reference+' '+meta['commit']+'\nupdate refs/heads/managed '+meta['commit']+' '+(expected or zero)+'\nprepare\ncommit\n').encode()
   git(repository,['update-ref','--stdin'],input=transaction,limit=4096)
   fsync_directory(repository/'refs/heads');fsync_directory(repository/'refs/lectern/integrations')
   value=dict(value,managed_current=True,current=False,current_presence='present')
  result=seal(run/'publication.json',value);write(journal,{'state':'committed','new_commit':meta['commit'],'expected_commit':expected,'receipt':result});fsync_directory(run)
  return seal(stage/'receipt.json',result)

def consume_operation(run,stage,data,value):
 published=publication_for(data['integration_id'])
 for requested,persisted in (('publication_receipt_sha256','receipt_sha256'),('commit','commit'),('tree_oid','tree_oid'),('tree_sha256','tree_sha256')):
  if data[requested]!=published[persisted]:raise ValueError('private source selection differs from immutable publication')
 source=run/'published-source';_,digest=inventory(source)
 if digest!=published['tree_sha256']:raise ValueError('private source export changed')
 work=mutable_work(data['owner_job']);copy_tree(source,work);admin_work(work)
 value.update(state='copied',source_integration_id=data['integration_id'],publication_receipt_sha256=published['receipt_sha256'],commit=published['commit'],tree_oid=published['tree_oid'],tree_sha256=digest,source_revision=published['commit'],source_kind='private_integration')
 return seal(stage/'receipt.json',value)

def rollback_operation(run,stage,data,value):
 original=publication_for(data['integration_id']);target=publication_for(data['target_publication_id'])
 if original['receipt_sha256']!=data['publication_receipt_sha256'] or target['project_id']!=data['project_id'] or target['commit']!=data['target_commit'] or target['tree_sha256']!=data['target_tree_sha256'] or not key(data['authorization_sha256']):raise ValueError('rollback publication binding differs')
 location=project(data['project_id']);repository=location/'repo.git';expected=data['expected_managed_commit']
 if not oid(expected):raise ValueError('rollback needs exact managed tip')
 reference='refs/lectern/rollbacks/'+value['operation_id']
 journal=stage/'transaction.json'
 if not journal.exists():write(journal,{'state':'prepared','expected':expected,'target':target['commit'],'target_publication_id':target['integration_id']});fsync_directory(stage)
 with (location/'publish.lock').open('a') as guard:
  fcntl.flock(guard,fcntl.LOCK_EX)
  existing=ref_read(repository,reference);head=ref_read(repository,'refs/heads/managed')
  if existing and existing!=target['commit']:raise ValueError('rollback transaction changed')
  if not existing:
   if head!=expected:return seal(stage/'receipt.json',dict(value,state='base_advanced',reason='rollback refuses to reset later managed work',observed_managed_commit=head))
   if value['generation']<=revoked(run):return dict(value,state='waiting',reason='rollback generation revoked')
   write(journal,{'state':'committing','expected':expected,'target':target['commit'],'target_publication_id':target['integration_id']});fsync_directory(stage)
   git(repository,['update-ref','--stdin'],input=('start\ncreate '+reference+' '+target['commit']+'\nupdate refs/heads/managed '+target['commit']+' '+expected+'\nprepare\ncommit\n').encode(),limit=4096)
  # Update managed-tip lookup to the retained target publication. Original
  # publication and every intervening integration ref remain untouched.
  rootdir(location/'commits');write(location/'commits'/(target['commit']+'.json'),{'integration_id':target['integration_id'],'commit':target['commit']})
  result=seal(stage/'receipt.json',dict(value,state='rolled_back',previous_commit=expected,commit=target['commit'],target_publication_id=target['integration_id'],ref=reference,canonical_changed=False,deployed=False,history_preserved=True))
  write(journal,{'state':'committed','receipt':result});fsync_directory(stage);return result

def freeze_file(source,target):
 if target.exists():
  info=R.regular(target)
  if info.st_uid!=os.geteuid() or info.st_mode&0o222:raise ValueError('unsafe frozen integration executable')
  return target
 info=R.regular(source)
 if info.st_uid!=os.geteuid() or info.st_mode&0o022 or info.st_size>512*1024:raise ValueError('unsafe integration executable source')
 temporary=target.with_suffix('.pending')
 if temporary.exists():temporary.unlink()
 with temporary.open('xb') as out:out.write(source.read_bytes());out.flush();os.fsync(out.fileno())
 temporary.chmod(0o400);os.replace(temporary,target);return target

def launcher(stage):
 freeze_file(R.PRIVATE_INTEGRATION_HELPER,stage/'helper.py');freeze_file(Path(R.__file__),stage/'runner.py');freeze_file(R.EXPERT_PROBE_HELPER,stage/'expert.py')
 entry=stage/'entry.py'
 if not entry.exists():
  source="import importlib.util,sys\nfrom pathlib import Path\np=Path(__file__).parent\ns=importlib.util.spec_from_file_location('frozen_runner',p/'runner.py');r=importlib.util.module_from_spec(s);s.loader.exec_module(r)\n"
  for name in ('ROOT','INSTALL','ASSET_CACHE','DEPENDENCIES','ARTIFACT_LOCK','EXPERT_PROBE_HELPER','PYTHON_HELPER','BROWSER_HELPER','PRIVATE_INTEGRATION_HELPER','INTEGRATION_ROOT'):
   value=str(getattr(R,name));source+='r.'+name+'='+(repr(value) if name=='INSTALL' else 'Path('+repr(value)+')')+'\n'
  source+="r.PRIVATE_INTEGRATION_HELPER=p/'helper.py'\nr.EXPERT_PROBE_HELPER=p/'expert.py'\n"
  source+='sys.exit(r.main())\n';temporary=stage/'entry.pending'
  if temporary.exists():temporary.unlink()
  with temporary.open('x') as out:out.write(source);out.flush();os.fsync(out.fileno())
  temporary.chmod(0o400);os.replace(temporary,entry)
 return entry

def interruption(stage,value,reason):
 value=dict(value,state='interrupted',reason=reason,ended_at=now())
 if value.get('executed'):value.update(charged_ms=CHECK_PROFILE['seconds']*1000,accounting='conservative_reserved_limit_after_unobserved_end',exit_code=None)
 return seal(stage/'receipt.json',value)

def status(job,integration,phase,check=None,current_generation=None):
 try:run,stage,data,value=request(job,integration,phase,check)
 except FileNotFoundError:
  R.job_path(job);run=attempt(integration)
  if phase!='check' or not key(check) or not current_generation or not revoked(run):raise
  diagnostic={'state':'cancelled','reason':'cancelled_missing_request','evidence_scope':'revoked_missing_request','integration_id':integration,'owner_job':job,'check_id':check,'request_sha256':check,'generation':current_generation,'revoked_through_generation':revoked(run),'executed':False,'charged_ms':0}
  location=rootdir(run/'missing-requests')/(sha((job+':'+check).encode())+'.json')
  existing=receipt(location)
  return dict(existing,generation=current_generation) if existing else seal(location,diagnostic)
 old=receipt(stage/'receipt.json')
 active=R.completion_service_active(unit(integration,value['operation_id']))
 if old and old['state'] in TERMINAL:return dict(old,generation=current_generation if current_generation is not None else old['generation'])
 if not active:
  old=receipt(stage/'receipt.json') or old
  if old and old['state'] in TERMINAL:return dict(old,generation=current_generation if current_generation is not None else old['generation'])
  if old and old.get('executed'):old=interruption(stage,old,'execution ended without a finalized observation')
 if old:return dict(old,generation=current_generation if current_generation is not None else old.get('generation',0),unit_active=active)
 return dict(value,state='waiting',generation=current_generation or 0,unit_active=active)

def start(job,integration,phase,number,check=None):
 generation(number);run,stage,data,value=request(job,integration,phase,check);value['generation']=number
 if phase=='check':value.update(execution_generation=number,check_id=check,candidate_tree_sha256=data.get('candidate_tree_sha256',''),candidate_receipt_sha256=data.get('candidate_receipt_sha256',''),executed=False,charged_ms=0)
 with (run/'launch.lock').open('a') as guard:
  try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
  except BlockingIOError:return dict(value,state='waiting',reason='integration launch or stop in progress')
  if number<=revoked(run):return dict(value,state='waiting',reason='integration generation revoked',revoked=True)
  authority=run/'authority.json';previous=record(authority).get('generation',0) if authority.exists() else 0
  if number<previous:return dict(value,state='waiting',reason='stale integration generation',revoked=True)
  write(authority,{'generation':number})
  old=receipt(stage/'receipt.json')
  if old and old['state'] in TERMINAL:return dict(old,generation=number)
  if R.completion_service_active(unit(integration,value['operation_id'])):return dict(old or value,generation=number,state='testing' if phase=='check' else 'preparing')
  old=receipt(stage/'receipt.json') or old
  if old and old['state'] in TERMINAL:return dict(old,generation=number)
  if old and old.get('executed'):return dict(interruption(stage,old,'executed integration check cannot be silently repeated'),generation=number)
  if old and old.get('retry_at',0)>time.time():return dict(old,generation=number)
  R.completion_launch_capacity();entry=launcher(stage)
  states={'prepare':'preparing','audit':'copying','review':'copying','seal':'sealing','check':'testing','publish':'publishing','consume':'copying','rollback':'publishing'}
  value.update(state=states[phase],executed=False);write(stage/'receipt.json',value)
  if number<=revoked(run):return dict(value,state='waiting',revoked=True)
  command=['/usr/bin/systemd-run','--quiet','--collect','--unit='+unit(integration,value['operation_id']),
           '--property=RuntimeMaxSec='+('720' if phase=='check' else '600'),'--property=MemoryMax=2G','--property=MemorySwapMax=0',
           '--property=CPUQuota=200%','--property=TasksMax=256','--property=KillMode=control-group','--property=TimeoutStopSec=10',
           '--property=PrivateMounts=yes','--property=NoNewPrivileges=yes','--property=ProtectControlGroups=yes','--property=UMask=0077',
           '--property=LimitFSIZE=2147483648','/usr/bin/python3','-I','-S',str(entry),'_integration','--job',job,'--integration-id',integration,'--phase',phase,'--generation',str(number)]
  if phase!='check':command=[part for part in command if part not in ('--property=PrivateMounts=yes','--property=ProtectControlGroups=yes')]
  if check:command+=['--check-id',check]
  R.run(command,pass_fds=(guard.fileno(),));return value

def stop(integration,number):
 generation(number);run=attempt(integration)
 with (run/'revoke.lock').open('a') as guard:
  fcntl.flock(guard,fcntl.LOCK_EX);write(run/'revoked.json',{'generation':max(number,revoked(run))})
 with (run/'launch.lock').open('a') as guard:
  try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
  except BlockingIOError:return {'state':'stopping','integration_id':integration,'generation':number}
  operations=run/'operations';pending=False
  if operations.exists():
   stages=sorted(operations.iterdir())
   if any(not key(stage.name) for stage in stages):raise ValueError('unsafe integration operation identity')
   scan_path=run/'stop-scan.json';scan=record(scan_path) if scan_path.exists() else {}
   after=scan.get('after','') if scan.get('generation')==number else ''
   remaining=[stage for stage in stages if stage.name>after]
   batch=remaining[:4]
   pending=bool(scan.get('pending')) if after else False
   for stage in batch:
    name=unit(integration,stage.name)
    if R.completion_service_active(name):R.run(['/usr/bin/systemctl','stop','--no-block',name],pass_fds=(guard.fileno(),),timeout=3)
    if R.completion_service_active(name):pending=True
    else:
     value=receipt(stage/'receipt.json')
     if value and value.get('executed') and value['state'] not in TERMINAL:interruption(stage,value,'cancelled execution ended without final observation')
   more=len(remaining)>len(batch)
   write(scan_path,{'generation':number,'after':batch[-1].name if more else '', 'pending':pending if more else False})
   pending=pending or more
  return {'state':'stopping' if pending else 'stopped','integration_id':integration,'generation':number}

def execute(job,integration,phase,number,check=None):
 generation(number);run,stage,data,value=request(job,integration,phase,check);value['generation']=number
 if phase=='check':value.update(execution_generation=number,check_id=check,candidate_tree_sha256=data.get('candidate_tree_sha256',''),candidate_receipt_sha256=data.get('candidate_receipt_sha256',''),executed=False,charged_ms=0)
 current=Path('/proc/self/cgroup').read_text().strip().split('::')[-1]
 if not current.endswith('/'+unit(integration,value['operation_id'])):raise ValueError('integration helper outside matching service')
 old=receipt(stage/'receipt.json')
 if old and old['state'] in TERMINAL:return 0
 if old and old.get('executed'):interruption(stage,old,'check execution cannot be replayed');return 1
 if number<=revoked(run) or record(run/'authority.json')['generation']!=number:return 1
 try:
  if phase=='check':result=check_operation(run,stage,data,value)
  else:
   with R.ARTIFACT_LOCK.open('a') as guard:
    fcntl.flock(guard,fcntl.LOCK_EX);R.completion_capacity()
    if number<=revoked(run):return 1
    functions={'prepare':lambda:prepare_operation(run,stage,data,value),'audit':lambda:prepare_operation(run,stage,data,value,audit=True),'review':lambda:review_operation(run,stage,data,value),'seal':lambda:seal_operation(run,stage,data,value),'publish':lambda:publish_operation(run,stage,data,value),'consume':lambda:consume_operation(run,stage,data,value),'rollback':lambda:rollback_operation(run,stage,data,value)}
    result=functions[phase]()
  return 0 if result['state'] in ('prepared','copied','sealed','published_private','rolled_back','exited') else 1
 except (ValueError,FileNotFoundError,R.PythonUnsupported) as exc:
  old=receipt(stage/'receipt.json')
  if old and old.get('executed'):interruption(stage,old,'execution observation interrupted: '+str(exc)[:300])
  else:seal(stage/'receipt.json',dict(value,state='rejected',reason=str(exc)[:800],executed=False,charged_ms=0))
  return 1
 except Exception as exc:
  old=receipt(stage/'receipt.json')
  if old and old.get('executed'):interruption(stage,old,'execution observation interrupted: '+str(exc)[:300])
  else:write(stage/'receipt.json',dict(value,state='waiting',reason=str(exc)[:800],retry_at=time.time()+60))
  return 1

def output(job,integration,check,stream,offset,number):
 if stream not in ('stdout','stderr') or type(offset) is not int or offset<0:raise ValueError('invalid integration log selection')
 _,stage,_,_=request(job,integration,'check',check);value=receipt(stage/'receipt.json')
 if not value or value['state'] not in TERMINAL or stream not in value:raise ValueError('integration check log is not finalized')
 path=stage/(stream+'.log');info=R.regular(path)
 if info.st_uid!=os.geteuid() or info.st_mode&0o222 or info.st_size!=value[stream]['bytes']:raise ValueError('unsafe integration check log')
 with path.open('rb') as log:log.seek(offset);content=log.read(32768)
 return {'integration_id':integration,'generation':number,'check_id':check,'stream':stream,'offset':offset,'total':info.st_size,'sha256':value[stream]['sha256'],'data_base64':base64.b64encode(content).decode()}

def dispatch(runtime,command,job=None,integration=None,phase=None,number=1,check=None,stream=None,offset=0,project_id=None):
 global R
 R=SimpleNamespace(**runtime)
 if command=='integration-tip':return integration_tip(project_id)
 if command=='integration-stop':return stop(integration,number)
 if command=='integration-status':return status(job,integration,phase,current_generation=number)
 if command=='integration-test-status':return status(job,integration,'check',check,number)
 if command=='integration-test-output':return output(job,integration,check,stream,offset,number)
 if command=='_integration':return execute(job,integration,phase,number,check)
 phases={'integration-prepare':'prepare','integration-audit-copy':'audit','integration-review-copy':'review','integration-seal':'seal','integration-test':'check','integration-publish':'publish','private-source-copy':'consume','integration-rollback':'rollback'}
 if command not in phases:raise ValueError('unknown private integration command')
 return start(job,integration,phases[command],number,check)
