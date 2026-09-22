"""Owned interactive Git worktrees. Never reset, force-remove, or delete branches."""
import json,os,pathlib,subprocess,sys,time
operation,raw=sys.argv[1:3]
p=json.loads(raw)
inherited_lock=tuple([int(sys.argv[3])]) if len(sys.argv)>3 and sys.argv[3]!='-' else ()
git_timeout=float(sys.argv[4]) if len(sys.argv)>4 else 90
control=None
operation_deadline=time.monotonic()+git_timeout
def remaining_timeout():
 remaining=operation_deadline-time.monotonic()
 if remaining<=0:raise ValueError('Workspace operation timed out; allocation retained for inspection')
 return remaining
def git(repo,*args,cancel_check=True):
 budget=remaining_timeout() if cancel_check else git_timeout
 if control is not None and cancel_check:
  control.check()
  command=['git','-C',repo,*args]
  if not inherited_lock and args[:2]==('worktree','add'):
   # A detached monitor retains the operation lock and cancellation observer
   # even if this launch supervisor dies while Git's checkout hook runs.
   monitor=setup_control_source+"\nimport sys\nc=SetupControl(json.loads(sys.argv[1]),create=False)\nc.check()\ng=subprocess.Popen(sys.argv[4:],stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,pass_fds=(int(sys.argv[2]),))\no,e=c.wait(g,float(sys.argv[3]),os.getpgrp())\nsys.stdout.write(o);sys.stderr.write(e);sys.exit(g.returncode)"
   command=['python3','-c',monitor,json.dumps(p),str(control.lease_file.fileno()),str(budget),*command]
  child=subprocess.Popen(command,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,start_new_session=not inherited_lock,pass_fds=inherited_lock or ((control.lease_file.fileno(),) if control.lease_file else ()))
  stdout,stderr=control.wait(child,budget,os.getpgrp() if inherited_lock else None)
  r=subprocess.CompletedProcess(child.args,child.returncode,stdout,stderr)
 else:r=subprocess.run(['git','-C',repo,*args],capture_output=True,text=True,timeout=budget,pass_fds=inherited_lock)
 if r.returncode:raise ValueError(r.stderr.strip() or r.stdout.strip() or 'Git command failed')
 return r.stdout.strip()
def run_setup(dest):
 if not p.get('setup_command','').strip():return
 budget=remaining_timeout()
 p['setup_state']='running'
 if control and not inherited_lock:control.allocation(dict(p))
 # The target-side monitor owns the cancellation lease while the setup shell
 # runs, even if the HTTP/SSH supervisor disappears. Output goes to a temporary
 # file rather than a pipe; only its final 4 KiB enters the allocation receipt.
 monitor=setup_control_source+"""
import sys,tempfile
plan=json.loads(sys.argv[1]); owner=json.loads(sys.argv[2]); fd=int(sys.argv[3]); timeout=float(sys.argv[4])
c=SetupControl(owner,create=False);c.check()
with tempfile.TemporaryFile() as output:
 g=subprocess.Popen(['bash','-c',plan['setup_command']],cwd=plan['path'],env={**os.environ,**plan.get('setup_env',{})},stdin=subprocess.DEVNULL,stdout=output,stderr=subprocess.STDOUT,pass_fds=(fd,))
 try:
  c.wait(g,timeout,os.getpgrp())
  plan['setup_state']='complete' if g.returncode==0 else 'failed'
 except BaseException:
  plan['setup_state']='interrupted'
  raise
 finally:
  output.seek(0,2); size=output.tell(); output.seek(max(0,size-4096));plan['setup_output']=output.read().decode('utf-8','replace')
  if plan['token']==owner['token']:c.allocation(plan)
 print(json.dumps({'rc':g.returncode,'plan':plan}))
"""
 owner=json.loads(sys.argv[5]) if inherited_lock else p
 fd=inherited_lock[0] if inherited_lock else control.lease_file.fileno()
 child=subprocess.Popen(['python3','-c',monitor,json.dumps(p),json.dumps(owner),str(fd),str(budget)],stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,start_new_session=not inherited_lock,pass_fds=(fd,))
 stdout,stderr=control.wait(child,budget,os.getpgrp() if inherited_lock else None)
 if child.returncode:raise ValueError('Workspace setup did not finish; inspect the retained allocation')
 result=json.loads(stdout);p.update(result['plan'])
 if result['rc']:raise ValueError('Workspace setup command exited with status '+str(result['rc'])+'\n'+p.get('setup_output',''))
def within(path,root):
 return os.path.commonpath([os.path.realpath(path),root])==root
def claim_created_worktree(repo,dest,common,commit):
 # A post-checkout hook can fail after Git has allocated a complete worktree.
 # Claim only that exact new allocation, never an existing or substituted tree.
 if git(dest,'rev-parse','--show-toplevel',cancel_check=False)!=dest:raise ValueError('Created directory is not the worktree root')
 if git(dest,'rev-parse','--path-format=absolute','--git-common-dir',cancel_check=False)!=common:raise ValueError('Created worktree belongs to another repository')
 if git(dest,'symbolic-ref','--quiet','--short','HEAD',cancel_check=False)!=p['branch']:raise ValueError('Created worktree branch does not match')
 if git(dest,'rev-parse','HEAD',cancel_check=False)!=commit:raise ValueError('Created worktree revision does not match')
 owner=pathlib.Path(git(dest,'rev-parse','--absolute-git-dir',cancel_check=False))/'lectern-owner'
 with owner.open('x') as file:file.write(p['token'])
try:
 if operation=='create':
  if not inherited_lock:control=SetupControl(p);control.lease()
  elif len(sys.argv)>5:control=SetupControl(json.loads(sys.argv[5]))
  if control:control.check()
 repo=os.path.realpath(p['repo']);dest=os.path.realpath(p['path'])
 if not os.path.isabs(p['repo']) or not os.path.isabs(p['path']):raise ValueError('Worktree paths must be absolute')
 common=git(repo,'rev-parse','--path-format=absolute','--git-common-dir')
 if operation=='create':
  if p['base'].startswith('-') or not p['base']:raise ValueError('Choose a branch, tag or commit as the base')
  git(repo,'check-ref-format','--branch',p['branch'])
  commit=git(repo,'rev-parse','--verify','--end-of-options',p['base']+'^{commit}')
  if os.path.lexists(p['path']):raise ValueError('Worktree path already exists; nothing was changed')
  pathlib.Path(dest).parent.mkdir(parents=True,exist_ok=True)
  if control and not inherited_lock:control.allocation(dict(p,repo=repo,path=dest,commit=commit))
  try:
   git(repo,'worktree','add','-b',p['branch'],'--',dest,commit)
  except ValueError as failure:
   if os.path.isdir(dest):
    try:
     claim_created_worktree(repo,dest,common,commit)
     p.update(repo=repo,path=dest,commit=commit,state='failed',error=str(failure))
    except (OSError,ValueError,subprocess.TimeoutExpired):
     # Keep the original Git failure. An allocation whose identity cannot be
     # proven stays unclaimed and must not be removed automatically.
     pass
   raise
  owner=pathlib.Path(git(dest,'rev-parse','--absolute-git-dir'))/'lectern-owner'
  with owner.open('x') as file:file.write(p['token'])
  p.update(repo=repo,path=dest,commit=commit,state='creating')
  try:run_setup(dest)
  except (OSError,ValueError,subprocess.TimeoutExpired) as failure:
   p.update(state='failed',error='Workspace setup command timed out; allocation retained for inspection' if isinstance(failure,subprocess.TimeoutExpired) else str(failure))
   if p.get('setup_state')=='running':p['setup_state']='interrupted'
   raise
  p['state']='ready'
 elif operation in ('recover','check-recover'):
  if os.path.islink(p['path']):raise ValueError('Allocation path was replaced by a symlink')
  if not inherited_lock:
   recovery=SetupControl(p,create=False);recovery.lease(create=False)
   recorded=recovery.allocation()
   if not recorded:raise ValueError('The original checkout revision was not recorded; inspect this allocation manually')
   if any(recorded.get(k)!=p.get(k) for k in ('token','branch')):raise ValueError('Recorded allocation identity does not match')
   if recorded.get('repo')!=repo or recorded.get('path')!=dest:raise ValueError('Recorded allocation paths do not match')
   if not isinstance(recorded.get('commit'),str) or not re.fullmatch('[0-9a-f]{40}|[0-9a-f]{64}',recorded['commit']):raise ValueError('Recorded checkout revision is unavailable')
   if p.get('commit') and p['commit']!=recorded['commit']:raise ValueError('Recorded checkout revisions do not match')
   p['commit']=recorded['commit']
   for key in ('setup_command','setup_env','setup_state','setup_output'):
    if key in recorded:p[key]=recorded[key]
   if p.get('setup_state')=='running':p['setup_state']='interrupted'
  if not os.path.isdir(dest):
   if os.path.lexists(p['path']):raise ValueError('Allocation path was replaced')
   registrations=git(repo,'worktree','list','--porcelain','-z').split('\0')
   if any(entry=='worktree '+dest for entry in registrations):raise ValueError('Missing checkout is still registered with Git; inspect it manually')
   p['state']='removed';print(json.dumps({'workspace':p}));sys.exit(0)
  if git(dest,'rev-parse','--show-toplevel')!=dest:raise ValueError('Directory is not the recorded worktree root')
  if git(dest,'rev-parse','--path-format=absolute','--git-common-dir')!=common:raise ValueError('Worktree repository does not match')
  if git(dest,'symbolic-ref','--quiet','--short','HEAD')!=p['branch']:raise ValueError('Worktree branch changed; inspect it manually')
  panes=subprocess.run(['tmux','list-panes','-a','-F','#{pane_current_path}'],capture_output=True,text=True,timeout=10)
  if panes.returncode and not any(x in panes.stderr.lower() for x in ['no server running','no such file or directory']):raise ValueError('Could not check active terminals before recovery')
  if any(within(cwd,dest) for cwd in panes.stdout.splitlines() if cwd):raise ValueError('A terminal is using this allocation; leave it before recovery')
  owner=pathlib.Path(git(dest,'rev-parse','--absolute-git-dir'))/'lectern-owner'
  if os.path.lexists(owner):
   fd=os.open(owner,os.O_RDONLY|os.O_NOFOLLOW|os.O_NONBLOCK)
   with os.fdopen(fd) as file:
    if not stat.S_ISREG(os.fstat(file.fileno()).st_mode) or file.read()!=p['token']:raise ValueError('Worktree ownership does not match')
  else:
   if not p.get('commit') or git(dest,'rev-parse','HEAD')!=p['commit']:raise ValueError('Worktree revision changed; ownership was not recovered')
   if operation=='recover':claim_created_worktree(repo,dest,common,p['commit'])
  if p.get('setup_command') and p.get('setup_state') not in ('complete','failed'):p['setup_state']='interrupted'
  p.update(repo=repo,path=dest,state='ready');p.pop('error',None)
 elif operation in ('remove','check-remove'):
  if not os.path.isdir(dest):
   registrations=git(repo,'worktree','list','--porcelain','-z').split('\0')
   if any(entry=='worktree '+dest for entry in registrations):raise ValueError('Worktree directory is missing but still registered with Git; inspect it before cleanup')
   p['state']='removed';print(json.dumps({'workspace':p}));sys.exit(0)
  if git(dest,'rev-parse','--show-toplevel')!=dest:raise ValueError('Directory is not the recorded worktree root')
  if git(dest,'rev-parse','--path-format=absolute','--git-common-dir')!=common:raise ValueError('Worktree belongs to another repository')
  owner=pathlib.Path(git(dest,'rev-parse','--absolute-git-dir'))/'lectern-owner'
  if not owner.is_file() or owner.read_text()!=p['token']:raise ValueError('Worktree ownership does not match; nothing was removed')
  if git(dest,'symbolic-ref','--quiet','--short','HEAD')!=p['branch']:raise ValueError('Worktree branch changed; nothing was removed')
  panes=subprocess.run(['tmux','list-panes','-a','-F','#{pane_current_path}'],capture_output=True,text=True,timeout=10)
  if panes.returncode and not any(x in panes.stderr.lower() for x in ['no server running','no such file or directory']):raise ValueError('Could not check active terminals; nothing was removed')
  if any(within(cwd,dest) for cwd in panes.stdout.splitlines() if cwd):raise ValueError('A terminal is still using this worktree; end or leave it first')
  if git(dest,'--no-optional-locks','status','--porcelain','--untracked-files=all','--ignored=matching'):raise ValueError('Worktree contains changed, untracked or ignored files; commit or move them before removal')
  if operation=='remove':
   git(repo,'worktree','remove','--',dest)
   p['state']='removed'
 else:raise ValueError('Unknown worktree operation')
 print(json.dumps({'workspace':p}))
except (OSError,ValueError,subprocess.TimeoutExpired) as e:
 out={'error':'Workspace operation timed out; allocation retained for inspection' if isinstance(e,subprocess.TimeoutExpired) else str(e)}
 if p.get('state')=='failed' and p.get('error'):out['workspace']=p
 print(json.dumps(out));sys.exit(1)
