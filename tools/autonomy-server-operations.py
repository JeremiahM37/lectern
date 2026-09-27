#!/usr/bin/python3
"""Registered, read-only server observations. No submitted shell, path or URL.

Observation receipts describe measured facts, not permission to mutate a service.
Backup unit success is deliberately not represented as snapshot/restore proof.
"""
import datetime
import fcntl
import pwd
from types import SimpleNamespace
import hashlib
import http.client
import json
import math
import os
from pathlib import Path
import re
import selectors
import signal
import stat
import subprocess
import time
import uuid

SCHEMA=1
MAINTENANCE_BOUNDS={'memory_max_bytes':(256*1024**2,2*1024**3),'cpu_quota_percent':(50,200),'tasks_max':(64,512)}
MAINTENANCE_HEADROOM_BYTES=32*1024**2
MAINTENANCE_HEADROOM_RATIO=2
MAINTENANCE_PROFILE='service_resource_limits_v1'
MAINTENANCE_PROTECTED={'lectern.service','agentdeck.service','ssh.service','sshd.service','tailscaled.service','homelab-api.service','systemd-logind.service'}
MAX_REGISTRY=256*1024
MAX_REQUEST=8192
MAX_COMMAND=256*1024
MAX_RECEIPT=512*1024
PROPERTIES=('Id','InvocationID','LoadState','ActiveState','SubState','Result','MainPID','ExecMainStatus','NRestarts',
 'MemoryCurrent','MemoryPeak','MemoryMax','TasksCurrent','TasksMax','CPUUsageNSec','CPUQuotaPerSecUSec',
 'CPUQuotaPeriodUSec','ActiveEnterTimestampMonotonic','InactiveEnterTimestampMonotonic',
 'LastTriggerUSecMonotonic','NextElapseUSecMonotonic')
ENUMS={
 'LoadState':{'loaded','not-found','bad-setting','error','masked','stub','merged'},
 'ActiveState':{'active','reloading','inactive','failed','activating','deactivating','maintenance','refreshing'},
 'SubState':{'dead','running','exited','failed','waiting','elapsed','start-pre','start','start-post','stop','stop-watchdog','stop-sigterm','stop-sigkill','stop-post','final-watchdog','final-sigterm','final-sigkill','auto-restart','auto-restart-queued','cleaning','listening'},
 'Result':{'success','resources','timeout','exit-code','signal','core-dump','watchdog','start-limit-hit','oom-kill','exec-condition','protocol','assert','dependency','unsupported','skipped'},
}
LIMITS=('MemoryMax','TasksMax','CPUQuotaPerSecUSec','CPUQuotaPeriodUSec')

class Invalid(ValueError):pass
class Unavailable(Exception):
 def __init__(self,code):self.code=code;super().__init__(code)

def canonical(value):return json.dumps(value,sort_keys=True,separators=(',',':'),allow_nan=False).encode()
def digest(data):return hashlib.sha256(data).hexdigest()
def stamp():return datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00','Z')
def sha(value):return isinstance(value,str) and re.fullmatch('[0-9a-f]{64}',value) is not None
def identifier(value):return isinstance(value,str) and re.fullmatch('[a-z][a-z0-9_-]{0,63}',value) is not None

def unique(pairs):
 result={}
 for key,value in pairs:
  if key in result:raise Invalid('duplicate JSON key')
  result[key]=value
 return result

def keys(value,required,optional=()):
 if not isinstance(value,dict) or not set(required)<=set(value) or set(value)-set(required)-set(optional):raise Invalid('unexpected object fields')

def local_path(value):
 if not isinstance(value,str) or len(value.encode())>4096 or not value.startswith('/') or '\\' in value or '\x00' in value or '\n' in value or (value!='/' and any(p in ('','.','..') for p in value[1:].split('/'))):raise Invalid('invalid registered path')
 return value

def unit(value,timer=False):
 suffix='timer' if timer else 'service'
 if not isinstance(value,str) or not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_.@:-]{0,180}\.'+suffix,value):raise Invalid('invalid registered unit')
 return value

def mapping(value,limit):
 if not isinstance(value,dict) or len(value)>limit or any(not identifier(k) for k in value):raise Invalid('invalid registered resource IDs')
 return value

def validate_registry(value):
 keys(value,('schema_version','targets'),('maintenance','backup_profiles'))
 for namespace in ('maintenance','backup_profiles'):
  if namespace in value:mapping(value[namespace],32)
 if value['schema_version']!=SCHEMA or type(value['schema_version']) is not int:raise Invalid('unsupported registry schema')
 targets=mapping(value['targets'],16)
 for target in targets.values():
  keys(target,('kind','services','filesystems','backups'),('health',))
  if target['kind']!='local_systemd':raise Invalid('unsupported registered target adapter')
  for service in mapping(target['services'],16).values():
   keys(service,('unit',),('identity_files','dropin_directory'))
   unit(service['unit'])
   files=service.get('identity_files',[])
   if not isinstance(files,list) or len(files)>16:raise Invalid('identity file limit')
   seen=set()
   for item in files:
    keys(item,('id','path','max_bytes'))
    if not identifier(item['id']) or item['id'] in seen:raise Invalid('invalid identity file ID')
    seen.add(item['id']);local_path(item['path'])
    if type(item['max_bytes']) is not int or not 1<=item['max_bytes']<=4*1024**2:raise Invalid('identity file size limit')
   if 'dropin_directory' in service:local_path(service['dropin_directory'])
  for filesystem in mapping(target['filesystems'],8).values():
   keys(filesystem,('path',));local_path(filesystem['path'])
  for backup in mapping(target['backups'],8).values():
   keys(backup,('service_unit','timer_unit'),('verification_unit',))
   unit(backup['service_unit']);unit(backup['timer_unit'],True)
   if 'verification_unit' in backup:unit(backup['verification_unit'])
  for health in mapping(target.get('health',{}),1).values():
   keys(health,('adapter','port','temperature_fields'))
   if health['adapter']!='temp_api_v1' or type(health['port']) is not int or not 1<=health['port']<=65535:raise Invalid('unsupported fixed health adapter')
   fields=health['temperature_fields']
   if not isinstance(fields,list) or len(fields)>32 or len(set(fields))!=len(fields) or any(not isinstance(v,str) or not re.fullmatch('[A-Za-z0-9_-]{1,96}',v) or v.startswith('_') for v in fields):raise Invalid('invalid temperature fields')
 seen_resources=set()
 for entry in value.get('maintenance',{}).values():
  keys(entry,('target_id','service_id','action','health_id','backup_profile','stateless','protected'))
  if entry['action']!='service_resource_limits' or entry['stateless'] is not True or entry['protected'] is not False:raise Invalid('unsupported maintenance registration')
  for field in ('target_id','service_id','health_id','backup_profile'):
   if not identifier(entry[field]):raise Invalid('invalid maintenance resource reference')
  target=targets.get(entry['target_id'],{});service=target.get('services',{}).get(entry['service_id'],{})
  resource=(entry['target_id'],entry['service_id'])
  if resource in seen_resources:raise Invalid('duplicate maintenance registration')
  seen_resources.add(resource)
  if not service or service['unit'] in MAINTENANCE_PROTECTED or entry['health_id'] not in target.get('health',{}) or entry['backup_profile'] not in value.get('backup_profiles',{}):raise Invalid('unavailable or protected maintenance resource')
  if service.get('dropin_directory')!='/etc/systemd/system/'+service['unit']+'.d':raise Invalid('maintenance drop-in registration differs')
 return value

def open_path(path,directory=False):
 """Descriptor walk refuses linked ancestors and never follows final links."""
 local_path(str(path));parts=Path(path).parts[1:]
 fd=os.open('/',os.O_RDONLY|os.O_DIRECTORY|os.O_CLOEXEC)
 try:
  for index,part in enumerate(parts):
   final=index==len(parts)-1
   flags=os.O_RDONLY|os.O_CLOEXEC|os.O_NOFOLLOW|os.O_NONBLOCK
   if not final or directory:flags|=os.O_DIRECTORY
   child=os.open(part,flags,dir_fd=fd);os.close(fd);fd=child
  info=os.fstat(fd)
  if directory and not stat.S_ISDIR(info.st_mode):raise Unavailable('not_directory')
  if not directory and (not stat.S_ISREG(info.st_mode) or info.st_nlink!=1):raise Unavailable('not_independent_regular_file')
  return fd
 except BaseException:
  os.close(fd);raise

def read_descriptor(fd,limit):
 info=os.fstat(fd)
 if info.st_size>limit:raise Unavailable('size_limit')
 data=bytearray()
 while True:
  part=os.read(fd,min(65536,limit+1-len(data)))
  if not part:break
  data.extend(part)
  if len(data)>limit:raise Unavailable('size_limit')
 after=os.fstat(fd)
 if (info.st_dev,info.st_ino,info.st_size,info.st_mtime_ns,info.st_ctime_ns)!=(after.st_dev,after.st_ino,after.st_size,after.st_mtime_ns,after.st_ctime_ns):raise Unavailable('changed_during_read')
 return bytes(data),after

def load_registry(path,trusted_uid=0):
 parent=open_path(Path(path).parent,True)
 try:
  info=os.fstat(parent)
  if info.st_uid!=trusted_uid or info.st_mode&0o022:raise Invalid('unsafe registry directory')
 finally:os.close(parent)
 fd=open_path(path)
 try:
  info=os.fstat(fd)
  if info.st_uid!=trusted_uid or info.st_mode&0o022:raise Invalid('registry is not trusted immutable configuration')
  raw,_=read_descriptor(fd,MAX_REGISTRY)
 finally:os.close(fd)
 value=validate_registry(json.loads(raw,object_pairs_hook=unique));return value,digest(raw)

def validate_request(request,registry_sha):
 keys(request,('schema_version','request_id','owner_job','owner_task','target_id','registry_sha256'))
 if request['schema_version']!=SCHEMA or type(request['schema_version']) is not int:raise Invalid('unsupported observation request schema')
 if not sha(request['request_id']) or request['registry_sha256']!=registry_sha:raise Invalid('request/registry binding differs')
 try:valid=str(uuid.UUID(request['owner_job']))==request['owner_job']
 except (ValueError,TypeError,AttributeError):valid=False
 if not valid or type(request['owner_task']) is not int or request['owner_task']<=0 or not identifier(request['target_id']):raise Invalid('invalid observation owner or target')


def bounded_command(argv,seconds=4,limit=MAX_COMMAND):
 process=subprocess.Popen(argv,stdin=subprocess.DEVNULL,stdout=subprocess.PIPE,stderr=subprocess.PIPE,start_new_session=True,close_fds=True,env={'PATH':'/usr/bin:/bin','LANG':'C','LC_ALL':'C','SYSTEMD_COLORS':'0','SYSTEMD_PAGER':'cat'})
 selector=selectors.DefaultSelector();buffers={'stdout':bytearray(),'stderr':bytearray()};deadline=time.monotonic()+seconds
 try:
  for name,stream in (('stdout',process.stdout),('stderr',process.stderr)):
   os.set_blocking(stream.fileno(),False);selector.register(stream,selectors.EVENT_READ,name)
  while selector.get_map():
   if time.monotonic()>=deadline:raise Unavailable('command_timeout')
   for event,_ in selector.select(min(.1,max(0,deadline-time.monotonic()))):
    chunk=os.read(event.fileobj.fileno(),65536)
    if not chunk:selector.unregister(event.fileobj);event.fileobj.close();continue
    buffers[event.data].extend(chunk)
    if sum(len(v) for v in buffers.values())>limit:raise Unavailable('command_output_limit')
  process.wait(timeout=max(.001,deadline-time.monotonic()))
  if process.returncode:raise Unavailable('command_failed')
  return bytes(buffers['stdout'])
 finally:
  selector.close()
  try:os.killpg(process.pid,signal.SIGKILL)
  except ProcessLookupError:pass
  process.wait()
  for stream in (process.stdout,process.stderr):
   if not stream.closed:stream.close()


def number(value):
 if value=='infinity' or value=='[not set]':return None
 if not re.fullmatch('[0-9]{1,20}',value):raise Unavailable('invalid_property')
 n=int(value)
 if n>2**64-1:raise Unavailable('invalid_property')
 return n

def duration(value):
 if value=='infinity':return None
 if re.fullmatch('[0-9]+',value):return number(value)
 scales={'us':1,'µs':1,'ms':1000,'s':1000000,'min':60000000,'h':3600000000,'d':86400000000,'w':604800000000}
 tokens=re.findall(r'([0-9]+(?:\.[0-9]{1,6})?)(us|µs|ms|min|s|h|d|w)',value)
 if not tokens or ''.join(n+suffix for n,suffix in tokens)!=value.replace(' ',''):raise Unavailable('invalid_duration')
 total=sum(int((n.replace('.','') if '.' in n else n))*scales[suffix]//(10**len(n.split('.')[1]) if '.' in n else 1) for n,suffix in tokens)
 if total>2**64-1:raise Unavailable('invalid_duration')
 return total

def service_projection(values):
 result={'state':'available','fields':{},'resource_scope':'unit cgroup total including descendant processes; not individual process RSS or evidence of a leak'}
 for name in PROPERTIES:
  if name=='Id' or name not in values:continue
  raw=values[name]
  if name=='InvocationID':
   if re.fullmatch('[0-9a-f]{32}',raw):result['fields'][name]=raw
   else:result['fields'][name]=None;result.setdefault('unavailable_fields',[]).append(name)
  elif name in ENUMS:result['fields'][name]=raw if raw in ENUMS[name] else 'unrecognized'
  else:
   try:result['fields'][name]=duration(raw) if name in ('CPUQuotaPerSecUSec','CPUQuotaPeriodUSec','LastTriggerUSecMonotonic','NextElapseUSecMonotonic') else number(raw)
   except Unavailable:result['fields'][name]=None;result.setdefault('unavailable_fields',[]).append(name)
 if result['fields'].get('LoadState')=='not-found':result['state']='unavailable';result['reason']='unit_not_found'
 return result

def observe_units(target):
 names={s['unit'] for s in target['services'].values()}
 for backup in target['backups'].values():names.update(v for v in backup.values())
 if not names:return {}
 output=bounded_command(['/usr/bin/systemctl','show','--no-pager','--property='+','.join(PROPERTIES),'--',*sorted(names)])
 blocks={}
 for block in output.decode('utf-8',errors='strict').strip().split('\n\n'):
  fields={}
  for line in block.splitlines():
   if '=' not in line:raise Unavailable('invalid_unit_response')
   key,value=line.split('=',1)
   if key in fields or key not in PROPERTIES:raise Unavailable('invalid_unit_response')
   fields[key]=value
  name=fields.get('Id')
  if name not in names or name in blocks:raise Unavailable('unit_identity_mismatch')
  blocks[name]=service_projection(fields)
 return {name:blocks.get(name,{'state':'unavailable','reason':'missing_unit_response'}) for name in names}


def unavailable(error):
 if isinstance(error,Unavailable):return {'state':'unavailable','reason':error.code}
 if isinstance(error,FileNotFoundError):return {'state':'unavailable','reason':'not_found'}
 if isinstance(error,PermissionError):return {'state':'unavailable','reason':'permission_denied'}
 return {'state':'unavailable','reason':'observation_failed'}

def identity_file(path,limit):
 fd=open_path(path)
 try:data,info=read_descriptor(fd,limit)
 finally:os.close(fd)
 return {'state':'available','sha256':digest(data),'bytes':len(data),'mode':stat.S_IMODE(info.st_mode),'uid':info.st_uid,'gid':info.st_gid}

def dropins(path):
 try:fd=open_path(path,True)
 except FileNotFoundError:return {'state':'available','absent':True,'files':[]}
 try:
  before=os.fstat(fd);names=os.listdir(fd)
  if len(names)>64 or any(not re.fullmatch(r'[A-Za-z0-9_.-]{1,128}\.conf',name) for name in names):raise Unavailable('unsupported_dropin_inventory')
  result=[]
  for name in sorted(names):
   child=os.open(name,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK|os.O_CLOEXEC,dir_fd=fd)
   try:
    info=os.fstat(child)
    if not stat.S_ISREG(info.st_mode) or info.st_nlink!=1:raise Unavailable('unsafe_dropin_file')
    data,info=read_descriptor(child,256*1024)
    result.append({'name':name,'sha256':digest(data),'bytes':len(data),'mode':stat.S_IMODE(info.st_mode),'uid':info.st_uid,'gid':info.st_gid})
   finally:os.close(child)
  info=os.fstat(fd)
  if (before.st_mtime_ns,before.st_ctime_ns)!=(info.st_mtime_ns,info.st_ctime_ns):raise Unavailable('changed_during_read')
  return {'state':'available','absent':False,'mode':stat.S_IMODE(info.st_mode),'uid':info.st_uid,'gid':info.st_gid,'files':result}
 finally:os.close(fd)


def filesystem(path):
 fd=open_path(path,True)
 try:
  info=os.fstat(fd);fs=os.fstatvfs(fd)
  mount_id=None
  for line in Path('/proc/self/fdinfo/'+str(fd)).read_text().splitlines():
   if line.startswith('mnt_id:'):mount_id=int(line.split(':',1)[1].strip())
  return {'state':'available','mount_id':mount_id,'device_major':os.major(info.st_dev),'device_minor':os.minor(info.st_dev),'total_bytes':fs.f_blocks*fs.f_frsize,'free_bytes':fs.f_bfree*fs.f_frsize,'available_bytes':fs.f_bavail*fs.f_frsize,'total_inodes':fs.f_files,'free_inodes':fs.f_ffree,'readonly':bool(fs.f_flag&os.ST_RDONLY),'measurement_scope':'registered path filesystem, not physical drive capacity'}
 finally:os.close(fd)


def temp_health(config):
 connection=http.client.HTTPConnection('127.0.0.1',config['port'],timeout=3)
 try:
  connection.request('GET','/',headers={'Accept':'application/json','Connection':'close'})
  response=connection.getresponse()
  if response.status!=200:raise Unavailable('health_http_status')
  if response.getheader('Content-Type','').split(';')[0].strip()!='application/json':raise Unavailable('health_content_type')
  body=response.read(65537)
  if len(body)>65536:raise Unavailable('health_size_limit')
  data=json.loads(body,object_pairs_hook=unique)
  if not isinstance(data,dict):raise Unavailable('health_schema')
  projected={}
  for field,low,high in (('_cpu_percent',0,100),('_load',0,1000000),('_cores',1,100000),*((name,-100,250) for name in config['temperature_fields'])):
   value=data.get(field)
   if type(value) not in (int,float) or not math.isfinite(value) or not low<=value<=high:raise Unavailable('health_schema')
   projected[field]=value
  return {'state':'available','adapter':'temp_api_v1','http_status':200,'values':projected,'scope':'registered numeric fields only; process names and other body fields omitted'}
 finally:connection.close()


def collect(registry,registry_sha,request,request_bytes=None):
 validate_registry(registry);validate_request(request,registry_sha)
 if request['target_id'] not in registry['targets']:raise Invalid('unregistered target')
 raw=canonical(request) if request_bytes is None else request_bytes
 if len(raw)>MAX_REQUEST or json.loads(raw,object_pairs_hook=unique)!=request:raise Invalid('request bytes differ')
 started=stamp();target=registry['targets'][request['target_id']]
 try:units=observe_units(target)
 except Exception as error:
  failure=unavailable(error);units={s['unit']:failure for s in target['services'].values()}
 facts={'services':{},'filesystems':{},'backups':{},'health':{}}
 stable={}
 for name,service in target['services'].items():
  value=dict(units.get(service['unit'],{'state':'unavailable','reason':'unit_query_unavailable'}));files={}
  for item in service.get('identity_files',[]):
   try:files[item['id']]=identity_file(item['path'],item['max_bytes'])
   except Exception as error:files[item['id']]=unavailable(error)
  value['identity_files']=files
  if 'dropin_directory' in service:
   try:value['dropins']=dropins(service['dropin_directory'])
   except Exception as error:value['dropins']=unavailable(error)
  facts['services'][name]=value
  stable[name]={'load_state':value.get('fields',{}).get('LoadState'),'identity_files':files,'dropins':value.get('dropins'),'limits':{k:v for k,v in value.get('fields',{}).items() if k in LIMITS}}
 for name,config in target['filesystems'].items():
  try:facts['filesystems'][name]=filesystem(config['path'])
  except Exception as error:facts['filesystems'][name]=unavailable(error)
 for name,config in target['backups'].items():
  facts['backups'][name]={'state':'observed','units':{role:units.get(name,{'state':'unavailable','reason':'unit_query_unavailable'}) for role,name in config.items()},'snapshot_verified':False,'restore_verified':False,'scope':'unit invocation/result only; snapshot contents and restoration not independently established'}
 for name,config in target.get('health',{}).items():
  try:facts['health'][name]=temp_health(config)
  except Exception as error:facts['health'][name]=unavailable(error)
 complete=all(value.get('fields',{}).get('LoadState')=='loaded' and all(row['state']=='available' for row in value['identity_files'].values()) and value.get('dropins',{'state':'available'})['state']=='available' and all(field in value.get('fields',{}) and field not in value.get('unavailable_fields',[]) for field in LIMITS) for value in facts['services'].values())
 receipt={**request,'configuration_complete':complete,'state':'observed','captured_at':started,'completed_at':stamp(),'request_sha256':digest(raw),'helper_sha256':digest(Path(__file__).read_bytes()),'facts':facts,'facts_sha256':digest(canonical(facts)),'configuration_sha256':digest(canonical({'registry_sha256':registry_sha,'services':stable})),'authority':'read-only observation; does not authorize maintenance','mutation_performed':False}
 receipt['receipt_sha256']=digest(canonical(receipt))
 if len(canonical(receipt))>MAX_RECEIPT:raise Unavailable('receipt_size_limit')
 return receipt

R=None
TERMINAL={'observed','unavailable','failed','cancelled'}

def trusted_state_directory(path):
 path.mkdir(mode=0o700,exist_ok=True)
 info=path.lstat()
 if not stat.S_ISDIR(info.st_mode) or info.st_uid!=os.geteuid() or info.st_mode&0o077:raise Invalid('unsafe observer state directory')
 return path

def job_state(job):
 R.job_path(job)
 return trusted_state_directory(trusted_state_directory(R.SERVER_OBSERVATIONS_ROOT)/job)

def stage_path(job,observation):
 if not sha(observation):raise Invalid('observation ID must be SHA256')
 return trusted_state_directory(trusted_state_directory(job_state(job)/'observations')/observation)

def state_json(path):
 info=R.regular(path)
 if info.st_uid!=os.geteuid() or info.st_mode&0o022 or info.st_size>MAX_RECEIPT:raise Invalid('unsafe observation state')
 return json.loads(path.read_bytes(),object_pairs_hook=unique)

def write_state(path,value):R.completion_write(path,value)

def seal_state(stage,value):
 value=dict(value);value.pop('receipt_sha256',None);value['receipt_sha256']=digest(canonical(value))
 write_state(stage/'receipt.json',value);return value

def read_receipt(stage):
 path=stage/'receipt.json'
 if not path.exists():return None
 value=state_json(path)
 if value.get('state') in TERMINAL:
  body=dict(value);expected=body.pop('receipt_sha256',None)
  if digest(canonical(body))!=expected:raise Invalid('observation receipt checksum mismatch')
 return value

def freeze_bytes(stage,name,raw):
 target=stage/name
 with (stage/(name+'.lock')).open('a') as guard:
  fcntl.flock(guard,fcntl.LOCK_EX)
  if target.exists():
   info=R.regular(target)
   if info.st_uid!=os.geteuid() or info.st_mode&0o222 or target.read_bytes()!=raw:raise Invalid('sealed observation input changed')
   return
  temporary=stage/(name+'.'+uuid.uuid4().hex+'.pending')
  try:
   fd=os.open(temporary,os.O_WRONLY|os.O_CREAT|os.O_EXCL|os.O_NOFOLLOW,0o400)
   with os.fdopen(fd,'wb') as file:file.write(raw);file.flush();os.fsync(file.fileno())
   os.replace(temporary,target)
   fd=os.open(stage,os.O_RDONLY|os.O_DIRECTORY)
   try:os.fsync(fd)
   finally:os.close(fd)
  finally:
   if temporary.exists():temporary.unlink()


def read_request(job,observation):
 stage=stage_path(job,observation);frozen=stage/'request.json'
 if frozen.exists():
  info=R.regular(frozen)
  if info.st_uid!=os.geteuid() or info.st_mode&0o222:raise Invalid('unsafe sealed observation request')
  raw=frozen.read_bytes()
 else:
  source=R.job_path(job)/'server-observations'/observation/'request.json'
  fd=open_path(source)
  try:
   info=os.fstat(fd)
   if info.st_uid not in (0,pwd.getpwnam('admin').pw_uid) or info.st_mode&0o077:raise Invalid('unsafe controller observation request')
   raw,_=read_descriptor(fd,MAX_REQUEST)
  finally:os.close(fd)
 if len(raw)>MAX_REQUEST:raise Invalid('oversized observation request')
 request=json.loads(raw,object_pairs_hook=unique)
 validate_request(request,request.get('registry_sha256'))
 if request['request_id']!=observation or request['owner_job']!=job or not sha(request['registry_sha256']):raise Invalid('observation request owner differs')
 if not frozen.exists():freeze_bytes(stage,'request.json',raw)
 return stage,request,raw

def observation_unit(job,observation):return 'lectern-server-observe-'+job+'-'+observation+'.service'

def cancelled(job,stage):return (job_state(job)/'cancel.json').exists() or (stage/'cancel.json').exists()

def base_receipt(request,raw):
 return {**request,'request_sha256':digest(raw),'helper_sha256':digest(Path(__file__).read_bytes()),'mutation_performed':False}

def terminal_failure(stage,request,raw,state,reason):
 return seal_state(stage,dict(base_receipt(request,raw),state=state,reason=reason,completed_at=stamp()))

def catalogue():
 try:registry,registry_sha=load_registry(R.SERVER_REGISTRY)
 except FileNotFoundError:return {'schema_version':1,'state':'unavailable','reason':'registry_not_configured','targets':[]}
 result=[]
 for name,target in sorted(registry['targets'].items()):
  maintenance=[]
  for resource,entry in sorted(registry.get('maintenance',{}).items()):
   if entry['target_id']==name:maintenance.append({'resource_id':resource,'service_id':entry['service_id'],'action':entry['action'],'min':{key:pair[0] for key,pair in MAINTENANCE_BOUNDS.items()},'max':{key:pair[1] for key,pair in MAINTENANCE_BOUNDS.items()},'memory_headroom_bytes':MAINTENANCE_HEADROOM_BYTES,'memory_headroom_ratio':MAINTENANCE_HEADROOM_RATIO,'stateless':True,'protected':False,'validation_profile':MAINTENANCE_PROFILE,'authority':'registration only; not current health or maintenance authorization'})
  result.append({'id':name,'kind':target['kind'],'services':sorted(target['services']),'filesystems':sorted(target['filesystems']),'backups':sorted(target['backups']),'health':[{'id':identity,'adapter':value['adapter']} for identity,value in sorted(target.get('health',{}).items())],'maintenance':maintenance})
 return {'schema_version':1,'state':'registered','registry_sha256':registry_sha,'targets':result,'authority':'read-only observation; registration does not establish current health'}

def frozen_launcher(stage):
 for source,name in ((R.SERVER_OPERATIONS_HELPER,'helper.py'),(Path(R.__file__),'runner.py')):
  target=stage/name
  if target.exists():
   info=R.regular(target)
   if info.st_uid!=os.geteuid() or info.st_mode&0o222:raise Invalid('unsafe frozen observer helper')
   continue
  info=R.regular(source)
  if info.st_uid!=os.geteuid() or info.st_mode&0o022 or info.st_size>512*1024:raise Invalid('unsafe installed observer executable')
  temporary=stage/(name+'.pending')
  if temporary.exists():temporary.unlink()
  with temporary.open('xb') as file:file.write(Path(source).read_bytes());file.flush();os.fsync(file.fileno())
  temporary.chmod(0o400);os.replace(temporary,target)
 entry=stage/'entry.py'
 if not entry.exists():
  source="import importlib.util,sys\nfrom pathlib import Path\np=Path(__file__).parent\ns=importlib.util.spec_from_file_location('frozen_runner',p/'runner.py');r=importlib.util.module_from_spec(s);s.loader.exec_module(r)\n"
  for name in ('ROOT','SERVER_REGISTRY','SERVER_OBSERVATIONS_ROOT'):
   source+='r.'+name+'=Path('+repr(str(getattr(R,name)))+')\n'
  source+="r.SERVER_OPERATIONS_HELPER=p/'helper.py'\nsys.exit(r.main())\n"
  temporary=stage/'entry.pending';temporary.write_text(source);temporary.chmod(0o400);os.replace(temporary,entry)
 return entry

def observation_status(job,observation):
 stage,request,raw=read_request(job,observation);value=read_receipt(stage)
 if value and value['state'] in TERMINAL:return value
 with (job_state(job)/'launch.lock').open('a') as guard:
  try:fcntl.flock(guard,fcntl.LOCK_SH|fcntl.LOCK_NB)
  except BlockingIOError:return dict(base_receipt(request,raw),state='observing',reason='launch_or_stop_in_progress')
  if cancelled(job,stage):
   if R.completion_service_active(observation_unit(job,observation)):return dict(base_receipt(request,raw),state='observing',stop_requested=True)
   return terminal_failure(stage,request,raw,'cancelled','observation_cancelled')
  if R.completion_service_active(observation_unit(job,observation)):return value or dict(base_receipt(request,raw),state='observing')
  value=read_receipt(stage)
  if value and value['state'] in TERMINAL:return value
  if value and value.get('launched_at'):return terminal_failure(stage,request,raw,'unavailable','observation_interrupted')
  return dict(base_receipt(request,raw),state='waiting')


def observation_start(job,observation):
 stage,request,raw=read_request(job,observation);root=job_state(job)
 with (root/'launch.lock').open('a') as guard:
  try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
  except BlockingIOError:return dict(base_receipt(request,raw),state='waiting',reason='launch_or_stop_in_progress')
  old=read_receipt(stage)
  if old and old['state'] in TERMINAL:return old
  if cancelled(job,stage):return terminal_failure(stage,request,raw,'cancelled','observation_cancelled')
  if R.completion_service_active(observation_unit(job,observation)):return old or dict(base_receipt(request,raw),state='observing')
  old=read_receipt(stage)
  if old and old['state'] in TERMINAL:return old
  if old and old.get('launched_at'):return terminal_failure(stage,request,raw,'unavailable','observation_interrupted')
  try:registry,registry_sha=load_registry(R.SERVER_REGISTRY)
  except (OSError,ValueError,Unavailable):return terminal_failure(stage,request,raw,'unavailable','registry_unavailable')
  if registry_sha!=request['registry_sha256'] or request['target_id'] not in registry['targets']:return terminal_failure(stage,request,raw,'failed','registered_target_binding_changed')
  fd=open_path(R.SERVER_REGISTRY)
  try:registry_raw,_=read_descriptor(fd,MAX_REGISTRY)
  finally:os.close(fd)
  if digest(registry_raw)!=registry_sha:return terminal_failure(stage,request,raw,'failed','registry_changed_during_capture')
  freeze_bytes(stage,'registry.json',registry_raw)
  entry=frozen_launcher(stage);R.completion_launch_capacity()
  value=dict(base_receipt(request,raw),state='observing',launched_at=stamp());write_state(stage/'receipt.json',value)
  if cancelled(job,stage):return terminal_failure(stage,request,raw,'cancelled','observation_cancelled')
  command=['/usr/bin/systemd-run','--quiet','--collect','--unit='+observation_unit(job,observation),
   '--property=RuntimeMaxSec=30','--property=MemoryMax=128M','--property=MemorySwapMax=0',
   '--property=CPUQuota=50%','--property=TasksMax=16','--property=KillMode=control-group','--property=TimeoutStopSec=3',
   '--property=UMask=0077','--property=NoNewPrivileges=yes','--property=IPAddressDeny=any','--property=IPAddressAllow=localhost',
   '--property=LimitFSIZE=1048576','/usr/bin/python3','-I','-S',str(entry),'_server-observe','--job',job,'--observation-id',observation]
  R.run(command,pass_fds=(guard.fileno(),));return value

def observation_execute(job,observation):
 stage,request,raw=read_request(job,observation)
 current=Path('/proc/self/cgroup').read_text().strip().split('::')[-1]
 if not current.endswith('/'+observation_unit(job,observation)):raise Invalid('collector outside matching bounded service')
 old=read_receipt(stage)
 if old and old['state'] in TERMINAL:return 0
 if cancelled(job,stage):terminal_failure(stage,request,raw,'cancelled','observation_cancelled');return 0
 try:
  registry,frozen_sha=load_registry(stage/'registry.json')
  if frozen_sha!=request['registry_sha256']:raise Invalid('sealed registry differs from request')
  value=collect(registry,request['registry_sha256'],request,raw)
  value['collector_profile']={'wall_seconds':30,'memory_bytes':128*1024**2,'cpu_percent':50,'tasks':16,'receipt_bytes':MAX_RECEIPT,'network':'loopback only; fixed registered typed health adapter'}
  if cancelled(job,stage):terminal_failure(stage,request,raw,'cancelled','observation_cancelled')
  else:seal_state(stage,value)
  return 0
 except Exception:
  terminal_failure(stage,request,raw,'unavailable','collector_failed');return 1

def observation_stop(job,observation=None):
 root=job_state(job);stage=stage_path(job,observation) if observation else root
 # Durable job-wide or single-ID revocation precedes the shared launch guard.
 with (root/'cancel.lock').open('a') as lock:
  fcntl.flock(lock,fcntl.LOCK_EX);write_state(stage/'cancel.json',{'cancelled_at':stamp()})
 with (root/'launch.lock').open('a') as guard:
  try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
  except BlockingIOError:return {'state':'stopping','owner_job':job,'request_id':observation}
  if observation:names=[observation_unit(job,observation)]
  else:
   prefix='lectern-server-observe-'+job+'-'
   raw=bounded_command(['/usr/bin/systemctl','list-units','--all','--plain','--no-legend','--no-pager','--state=active,activating,deactivating,reloading',prefix+'*.service'],seconds=3,limit=65536)
   names=[]
   for row in raw.decode().splitlines():
    name=row.split()[0]
    if not re.fullmatch(re.escape(prefix)+'[0-9a-f]{64}\\.service',name):raise Invalid('unexpected observation unit identity')
    names.append(name)
  active=[name for name in names if R.completion_service_active(name)]
  if active:R.run(['/usr/bin/systemctl','stop','--no-block',*active],pass_fds=(guard.fileno(),),timeout=3)
  pending=any(R.completion_service_active(name) for name in active)
  return {'state':'stopping' if pending else 'stopped','owner_job':job,'request_id':observation}

def dispatch(runtime,command,job=None,observation=None):
 global R
 R=SimpleNamespace(**runtime)
 if command=='server-targets':return catalogue()
 if command=='server-observe':return observation_start(job,observation)
 if command=='server-observe-status':return observation_status(job,observation)
 if command=='server-observe-stop':return observation_stop(job,observation)
 if command=='_server-observe':return observation_execute(job,observation)
 raise Invalid('unsupported server observation command')
