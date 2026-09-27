#!/usr/bin/python3
"""Administrator-only snapshot of a selected installed Node/npm distribution."""
import argparse,hashlib,importlib.util,json,os,platform,shutil,stat,subprocess,tempfile
from pathlib import Path
s=importlib.util.spec_from_file_location('node_helper',Path(__file__).with_name('node-project-dependencies.py'));m=importlib.util.module_from_spec(s);s.loader.exec_module(m)
def provision(source,destination):
 if os.geteuid()!=0:raise ValueError('root installation required')
 source=Path(source).resolve();destination=Path(destination)
 destination.mkdir(parents=True,exist_ok=True)
 if destination.is_symlink() or destination.stat().st_uid!=0 or destination.stat().st_mode&0o022:raise ValueError('unsafe tooling cache')
 stage=Path(tempfile.mkdtemp(prefix='.pending-',dir=destination))
 shutil.copytree(source,stage/'runtime',symlinks=True)
 rows=m.inventory(stage/'runtime')
 env={'PATH':'/usr/bin:/bin','HOME':str(stage),'NPM_CONFIG_USERCONFIG':str(stage/'empty-user.npmrc'),'NPM_CONFIG_GLOBALCONFIG':str(stage/'empty-global.npmrc'),'NPM_CONFIG_UPDATE_NOTIFIER':'false'}
 # Only administrator-selected existing tooling is executed here, never package
 # input or a worker-selected executable.
 node=stage/'runtime/bin/node';npm=stage/'runtime/lib/node_modules/npm/bin/npm-cli.js'
 runtime=dict(schema_version=1,kind='node-tooling',policy=m.POLICY,platform=platform.system(),machine=platform.machine(),node_version=subprocess.check_output([str(node),'--version'],text=True,env=env).strip(),npm_version=subprocess.check_output([str(node),str(npm),'--version'],text=True,env=env).strip(),files=rows)
 if m.inventory(stage/'runtime')!=rows:raise ValueError('runtime changed during version identification')
 key=m.sha(m.canonical(runtime));runtime['key']=key
 (stage/'manifest.json').write_bytes(m.canonical(runtime))
 for p in (stage,*stage.rglob('*')):
  if p.is_symlink():os.lchown(p,0,0);continue
  os.chown(p,0,0);p.chmod(0o555 if p.is_dir() or p.stat().st_mode&0o111 else 0o444)
 final=destination/key
 if final.exists():
  st=final.lstat()
  if not stat.S_ISDIR(st.st_mode) or st.st_uid!=0 or st.st_mode&0o222 or (final/'manifest.json').read_bytes()!=m.canonical(runtime) or m.inventory(final/'runtime')!=rows:raise ValueError('existing runtime content differs')
  for p in (final,*final.rglob('*')):
   st=p.lstat()
   if st.st_uid!=0 or (not p.is_symlink() and st.st_mode&0o222):raise ValueError('existing runtime ownership/mode differs')
  shutil.rmtree(stage)
 else:os.rename(stage,final)
 for p in final.rglob('*'):
  if p.is_file() and not p.is_symlink():
   with p.open('rb') as f:os.fsync(f.fileno())
 fd=os.open(final,os.O_DIRECTORY);os.fsync(fd);os.close(fd)
 selector=destination/'active.pending';selector.write_bytes(m.canonical({'key':key}));selector.chmod(0o444);
 with selector.open('rb') as f:os.fsync(f.fileno())
 os.replace(selector,destination/'active.json')
 fd=os.open(destination,os.O_DIRECTORY);os.fsync(fd);os.close(fd)
 return runtime
if __name__=='__main__':
 p=argparse.ArgumentParser();p.add_argument('--source',required=True);p.add_argument('--destination',required=True);a=p.parse_args();print(json.dumps(provision(a.source,a.destination)))
