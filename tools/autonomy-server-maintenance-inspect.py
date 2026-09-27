#!/usr/bin/python3
"""Bounded read-only inspection of a retained maintenance rollback conflict."""
import base64
import fcntl
import importlib.util
import json
import os
from pathlib import Path
import time
from types import SimpleNamespace

_here=Path(__file__).parent
_helper=_here/'autonomy-server-maintenance.py'
if not _helper.exists():_helper=_here/'lectern-autonomy-server-maintenance.py'
_spec=importlib.util.spec_from_file_location('maintenance_inspection_base',_helper)
M=importlib.util.module_from_spec(_spec);_spec.loader.exec_module(M)
R=None
PROFILE='registered_service_external_health_v1'

def sealed(value):
 value=dict(value);value['receipt_sha256']=M.sha(M.canonical(value));return value

def folder(operation,inspection):
 if not M.key(inspection):raise ValueError('invalid inspection identity')
 return M.private_directory(M.private_directory(M.operation_root(operation)/'inspections')/inspection)

def unit(operation,inspection):
 return 'lectern-maintenance-inspect-'+operation+'-'+inspection+'.service'

def request_at(root,operation,inspection,job,generation,create=False):
 path=root/'request.json'
 if path.exists():raw=M.root_bytes(path,16384)[0]
 else:
  # A persisted controller intent may precede the first runner invocation.
  # Status can bind that existing trusted input without freezing or launching.
  raw=M.controller_bytes(R.job_path(job)/'server-maintenance'/(operation+'.inspect-'+inspection+'.json'))
 request=json.loads(raw,object_pairs_hook=M.OBS.unique)
 M.owner_identity(request)
 if request.get('operation_id')!=operation or request.get('owner_job')!=job or request.get('generation')!=generation:raise ValueError('inspection owner differs')
 # Authority must come from the root-frozen original effect, never a new model.
 journal=json.loads(M.root_bytes(M.transaction_root()/(operation+'.json'))[0])
 if journal.get('phase')!='rollback_conflict':raise ValueError('retained rollback conflict required')
 M.verify_sealed(journal['receipt'])
 original_generation=journal.get('generation')
 if type(original_generation) is not int or original_generation<1:raise ValueError('original effect generation unavailable')
 authority_path=M.operation_root(operation)/'phases'/'apply'/job/str(original_generation)/'authority.json'
 authority=json.loads(M.root_bytes(authority_path,16384)[0])
 if M.sha(M.canonical(authority))!=request.get('authority_sha256'):raise ValueError('original frozen authority unavailable')
 registry,registry_sha=M.OBS.load_registry(M.operation_root(operation)/'registry.json')
 M.validate(request,authority,registry_sha)
 if journal.get('request_sha256')!=M.request_digest(request) or journal.get('authority_sha256')!=request['authority_sha256']:raise ValueError('inspection changed effect authority')
 if create and not path.exists():M.frozen_file(path,raw)
 return request,raw,registry,registry_sha

def response(root,request,raw,inspection,state,**extra):
 return sealed(dict(schema_version=1,operation_id=request['operation_id'],inspection_id=inspection,owner_job=request['owner_job'],owner_task=request['owner_task'],pin_sha256=request['pin_sha256'],generation=request['generation'],request_sha256=M.sha(raw),phase='inspect',state=state,**extra))

def freeze(root):
 manifest=root/'executables.json'
 if manifest.exists():
  value=json.loads(M.root_bytes(manifest)[0])
  if not M.key(value.get('digest')) or set(value.get('files',{}))!={'autonomy-runner.py','autonomy-server-maintenance.py','autonomy-server-operations.py','autonomy-server-maintenance-inspect.py','entry.py'}:raise ValueError('invalid inspector executable manifest')
  cache=R.SERVER_MAINTENANCE_TOOLS/value['digest']
  for directory in (R.SERVER_MAINTENANCE_TOOLS,cache):
   fd=M.OBS.open_path(directory,directory=True)
   try:
    info=os.fstat(fd)
    if info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe inspection cache')
   finally:os.close(fd)
  for name,digest in value['files'].items():
   if M.sha(M.root_bytes(cache/name,16*1024*1024)[0])!=digest:raise ValueError('inspection executable changed')
  return cache
 sources={'autonomy-runner.py':Path(R.__file__),'autonomy-server-maintenance.py':R.SERVER_MAINTENANCE_HELPER,'autonomy-server-operations.py':R.SERVER_OPERATIONS_HELPER,'autonomy-server-maintenance-inspect.py':R.SERVER_MAINTENANCE_INSPECT_HELPER}
 data={name:M.root_bytes(path,16*1024*1024)[0] for name,path in sources.items()}
 files={name:M.sha(raw) for name,raw in data.items()};digest=M.sha(M.canonical(files))
 R.SERVER_MAINTENANCE_TOOLS.mkdir(mode=0o755,parents=True,exist_ok=True)
 cache=R.SERVER_MAINTENANCE_TOOLS/digest;cache.mkdir(mode=0o755,exist_ok=True)
 for directory in (R.SERVER_MAINTENANCE_TOOLS,cache):
  fd=M.OBS.open_path(directory,directory=True)
  try:
   info=os.fstat(fd)
   if info.st_uid!=0 or info.st_mode&0o022:raise ValueError('unsafe inspection executable cache')
  finally:os.close(fd)
 for name,raw in data.items():M.frozen_file(cache/name,raw,0o444)
 launcher="import importlib.util,sys\nfrom pathlib import Path\np=Path(__file__).parent\ns=importlib.util.spec_from_file_location('runner',p/'autonomy-runner.py');r=importlib.util.module_from_spec(s);s.loader.exec_module(r)\n"
 for key in ('ROOT','SERVER_REGISTRY','SERVER_MAINTENANCE_ROOT','SERVER_MAINTENANCE_TOOLS'):
  launcher+='r.'+key+'=Path('+repr(str(getattr(R,key)))+')\n'
 for key,name in [('SERVER_MAINTENANCE_HELPER','autonomy-server-maintenance.py'),('SERVER_OPERATIONS_HELPER','autonomy-server-operations.py'),('SERVER_MAINTENANCE_INSPECT_HELPER','autonomy-server-maintenance-inspect.py')]:launcher+='r.'+key+"=p/"+repr(name)+'\n'
 launcher+='sys.exit(r.main())\n';M.frozen_file(cache/'entry.py',launcher.encode(),0o444);files['entry.py']=M.sha(launcher.encode())
 M.atomic(manifest,dict(digest=digest,files=files));return cache

def inspect_backend(backend,resource,request,inspection,registry_sha,live_registry_sha,cancelled,candidate_owned,conflict_receipt_sha):
 identity=dict(schema_version=1,conflict_receipt_sha256=conflict_receipt_sha,generation=request['generation'],operation_id=request['operation_id'],inspection_id=inspection,request_sha256=M.request_digest(request),authority_sha256=request['authority_sha256'],before_sha256=request['expected_state_sha256'],candidate_sha256=M.sha(M.canonical(request['limits'])),registry_sha256=registry_sha,profile=PROFILE,no_mutation=True,helper_sha256=M.sha(Path(__file__).read_bytes()))
 start=time.time();identity['observed_at']=start
 try:
  if live_registry_sha()!=registry_sha:raise M.Unavailable('registered mapping changed')
  before=backend.capture(resource);invocation=backend.invocation(resource)
  if candidate_owned():raise M.Unavailable('current generation remains operation-owned')
  observations=[]
  for _ in range(3):
   if cancelled():raise M.Unavailable('inspection cancelled')
   observation=backend._health_once(resource)
   properties=backend.properties(resource,['ActiveState','SubState'])
   observation['healthy']=observation.get('healthy') is True and properties.get('ActiveState')=='active' and properties.get('SubState')=='running'
   observation['properties']=properties;observations.append(observation)
   if observation.get('healthy') is not True:raise M.Unavailable('registered health failed')
   time.sleep(.1)
  after=backend.capture(resource);post_invocation=backend.invocation(resource)
  if cancelled() or candidate_owned() or live_registry_sha()!=registry_sha or backend.state_sha(before)!=backend.state_sha(after) or invocation!=post_invocation:raise M.Unavailable('external generation changed during observation')
  identity.update(state='external_healthy',owned_candidate=False,current_state_sha256=backend.state_sha(before),post_state_sha256=backend.state_sha(after),invocation_id=invocation,post_invocation_id=post_invocation,observations=observations)
 except Exception as error:identity.update(state='unavailable',reason=type(error).__name__)
 identity['completed_at']=time.time();return sealed(identity)

def dispatch(runtime,command,job,operation,inspection,generation):
 global R
 R=SimpleNamespace(**runtime) if isinstance(runtime,dict) else runtime;M.R=R
 root=folder(operation,inspection);unitname=unit(operation,inspection)
 if command=='server-maintenance-inspect-stop':
  M.atomic(root/'cancel.json',dict(cancelled=True))
  with (root/'launch.lock').open('a') as guard:
   try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
   except BlockingIOError:return dict(state='stopping',operation_id=operation,inspection_id=inspection,generation=generation)
   if R.completion_service_active(unitname):R.run(['/usr/bin/systemctl','stop','--no-block',unitname],timeout=3,pass_fds=(guard.fileno(),))
   active=R.completion_service_active(unitname)
  return dict(state='stopping' if active else 'stopped',operation_id=operation,inspection_id=inspection,generation=generation,no_mutation=True)
 internal=command=='_server-maintenance-inspect'
 start=command=='server-maintenance-inspect'
 request,raw,registry,registry_sha=request_at(root,operation,inspection,job,generation,start)
 receipt=root/'receipt.json'
 if receipt.exists():
  value=json.loads(M.root_bytes(receipt)[0]);M.verify_sealed(value)
  if value['state']!='running':return value
 if internal:
  current=Path('/proc/self/cgroup').read_text().strip().split('::')[-1]
  if not current.endswith('/'+unitname):raise ValueError('inspection outside owned unit')
  backend=M.SystemdBackend(registry,registry_sha);resource=backend.resolve(request['target_id'],request['service_id'])
  journal=json.loads(M.root_bytes(M.transaction_root()/(operation+'.json'))[0])
  inner=inspect_backend(backend,resource,request,inspection,registry_sha,lambda:M.OBS.load_registry(R.SERVER_REGISTRY)[1],lambda:(root/'cancel.json').exists(),lambda:backend.owns(resource,journal['before'],base64.b64decode(journal['candidate'],validate=True)),journal['receipt']['receipt_sha256'])
  result=response(root,request,raw,inspection,inner['state'],result=inner);M.atomic(receipt,result);return 0
 if R.completion_service_active(unitname):return response(root,request,raw,inspection,'running')
 if receipt.exists() or (root/'cancel.json').exists():
  with (root/'launch.lock').open('a') as observation_guard:
   try:fcntl.flock(observation_guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
   except BlockingIOError:return response(root,request,raw,inspection,'running')
   if R.completion_service_active(unitname):return response(root,request,raw,inspection,'running')
   if receipt.exists():
    prior=json.loads(M.root_bytes(receipt)[0]);M.verify_sealed(prior)
    if prior['state']!='running':return prior
   result=response(root,request,raw,inspection,'unavailable',reason='cancelled or interrupted immutable inspection');M.atomic(receipt,result);return result
 if not start:return response(root,request,raw,inspection,'waiting')
 with (root/'launch.lock').open('a') as guard:
  try:fcntl.flock(guard,fcntl.LOCK_EX|fcntl.LOCK_NB)
  except BlockingIOError:return response(root,request,raw,inspection,'waiting')
  if (root/'cancel.json').exists():return response(root,request,raw,inspection,'unavailable',reason='revoked before launch')
  if receipt.exists():
   prior=json.loads(M.root_bytes(receipt)[0]);M.verify_sealed(prior);return prior
  cache=freeze(root);R.completion_launch_capacity()
  M.atomic(receipt,response(root,request,raw,inspection,'running'))
  R.run(['/usr/bin/systemd-run','--quiet','--collect','--unit='+unitname,'--property=RuntimeMaxSec=45s','--property=MemoryMax=256M','--property=MemorySwapMax=0','--property=CPUQuota=50%','--property=TasksMax=32','--property=KillMode=control-group','--property=TimeoutStopSec=5s','--property=UMask=0077','--property=NoNewPrivileges=yes','--property=IPAddressDeny=any','--property=IPAddressAllow=localhost','--property=ProtectSystem=strict','--property=ProtectHome=read-only','--property=ReadWritePaths='+str(root),'/usr/bin/python3','-I','-S',str(cache/'entry.py'),'_server-maintenance-inspect','--job',job,'--operation-id',operation,'--inspection-id',inspection,'--generation',str(generation)],pass_fds=(guard.fileno(),))
 return response(root,request,raw,inspection,'running')
