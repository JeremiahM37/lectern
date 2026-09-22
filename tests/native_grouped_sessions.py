"""Manual installed-CLI proof with synthetic history; never sends a model prompt.
Run with the E2E venv: python tests/native_grouped_sessions.py --mode fork|resume.
Uses installed Claude/Codex authentication through isolated temporary profiles.
"""
import sys,tempfile,pathlib,subprocess,json,uuid,os,shutil,re,time,hashlib,urllib.request
sys.path.insert(0,str(pathlib.Path(__file__).resolve().parents[1]/'e2e'))
from test_terminal_workspace import real_terminal

def put(t,path,body):
 request=urllib.request.Request(t['url']+'/api'+path,method='PUT',data=json.dumps(body).encode(),headers={'Content-Type':'application/json'})
 with urllib.request.urlopen(request) as response:return json.load(response)

import argparse
parser=argparse.ArgumentParser();parser.add_argument('--mode',choices=('fork','resume'),default='fork');mode=parser.parse_args().mode

for agent in ('codex','claude'):
 with tempfile.TemporaryDirectory(prefix='lec-native-api-') as tmp:
  generator=real_terminal.__wrapped__(pathlib.Path(tmp),object());t=next(generator)
  try:
   root=t['root'];home=pathlib.Path(tmp)/'native-profile';home.mkdir(mode=0o700)
   subprocess.run(['git','-C',str(root),'add','hello.txt'],check=True)
   subprocess.run(['git','-C',str(root),'-c','user.name=Fixture','-c','user.email=fixture@localhost','commit','-qm','Fixture'],check=True)
   put(t,'/agents',[{'name':'fixture-shell','command':'sleep 600'}])
   primary=t['api']('/projects',{'name':'Primary native fixture','target_id':t['target_id'],'repo_path':str(root)})
   extra_root=pathlib.Path(tmp)/'second-repository';extra_root.mkdir()
   subprocess.run(['git','init','-q',str(extra_root)],check=True)
   (extra_root/'second.txt').write_text('second repo base\n')
   subprocess.run(['git','-C',str(extra_root),'add','.'],check=True)
   subprocess.run(['git','-C',str(extra_root),'-c','user.name=Fixture','-c','user.email=fixture@localhost','commit','-qm','Second fixture'],check=True)
   extra=t['api']('/projects',{'name':'Second native fixture','target_id':t['target_id'],'repo_path':str(extra_root)})
   parent=t['api']('/sessions',{'name':'Grouped native source','agent':'fixture-shell','project_id':primary['id'],'worktree':{'extra_repositories':[{'project_id':extra['id']}]}})
   root=pathlib.Path(parent['workdir'])
   source_id=str(uuid.uuid4());first=str(uuid.uuid4());second=str(uuid.uuid4());stamp='2026-09-10T04:00:00.000Z'
   if agent=='codex':
    folder=home/'sessions';folder.mkdir();env={'CODEX_HOME':str(home)}
    records=[{'timestamp':stamp,'type':'session_meta','payload':{'id':source_id,'timestamp':stamp,'cwd':str(root),'originator':'codex_cli_rs','cli_version':'0.148.0','source':'cli','model_provider':'openai'}},{'timestamp':stamp,'type':'response_item','payload':{'type':'message','role':'user','content':[{'type':'input_text','text':'FICTIONAL NATIVE API FIXTURE. Do not run any commands.'}]}},{'timestamp':stamp,'type':'response_item','payload':{'type':'message','role':'assistant','content':[{'type':'output_text','text':'Saved assistant API proof.'}]}}]
    auth=pathlib.Path.home()/'.codex/auth.json'
    if auth.exists():(home/'auth.json').symlink_to(auth)
    (home/'config.toml').write_text('check_for_update_on_startup = false\n')
    args=['fork','{id}']
   else:
    folder=home/'projects'/re.sub(r'[^a-zA-Z0-9]','-',str(root));folder.mkdir(parents=True);env={'CLAUDE_CONFIG_DIR':str(home)}
    common={'isSidechain':False,'userType':'external','cwd':str(root),'sessionId':source_id,'version':'2.1.0','timestamp':stamp}
    records=[{**common,'type':'user','uuid':first,'parentUuid':None,'message':{'role':'user','content':'FICTIONAL NATIVE API FIXTURE. Do not run commands.'}},{**common,'type':'assistant','uuid':second,'parentUuid':first,'message':{'id':'msg_fixture','type':'message','role':'assistant','model':'claude-sonnet-4-6','content':[{'type':'text','text':'Saved assistant API proof.'}],'stop_reason':'end_turn','stop_sequence':None,'usage':{'input_tokens':10,'output_tokens':5}}}]
    auth=pathlib.Path.home()/'.claude/.credentials.json'
    if auth.exists():shutil.copy2(auth,home/'.credentials.json')
    (home/'.claude.json').write_text(json.dumps({'hasCompletedOnboarding':True,'remoteControlAtStartup':False,'theme':'dark'}))
    (home/'settings.json').write_text(json.dumps({'remoteControlAtStartup':False}))
    args=['--resume','{id}','--fork-session']
   file=folder/(('rollout-2026-09-10T04-00-00-' if agent=='codex' else '')+source_id+'.jsonl');file.write_text(''.join(json.dumps(r)+'\n' for r in records));original=file.read_bytes()
   specs=t['api']('/agents');spec=next(s for s in specs if s['name']==agent)
   empty_profile=pathlib.Path(tmp)/'unused-profile';empty_profile.mkdir()
   base_env={key:str(empty_profile) for key in env}
   spec.update(env=base_env,fork_args=args,command=shutil.which(agent));put(t,'/agents',[spec])
   profile=t['api']('/launch-profiles',{'name':'Installed '+agent,'agent':agent,'command':shutil.which(agent),'env_json':json.dumps(env)})
   if mode=='resume':
    source=t['api']('/sessions',{'yolo':False,'name':'Native resume source','agent':agent,'target_id':t['target_id'],'workdir':str(root),'profile_id':profile['id']})
    request=urllib.request.Request(t['url']+'/api/sessions/'+str(source['id']),method='DELETE')
    with urllib.request.urlopen(request) as response:assert response.status==200
    child=t['api']('/sessions/'+str(source['id'])+'/resume',{'conversation_id':source_id,'name':'Exact grouped native resume'})
    assert child['launch_profile']=='Installed '+agent
    assert child['workdir']==str(root)
    assert child['resume_id']==source_id
   else:
    search=t['api']('/conversation-search',{'query':'Saved assistant API proof','agent':agent,'target_id':t['target_id']})
    for _ in range(100):
     search=t['api']('/conversation-search/'+search['id'])
     if search['done']:break
     time.sleep(.1)
    assert search['complete'],search
    hit=next(hit for hit in search['results'] if hit['conversation_id']==source_id)
    endpoint='/conversation-search/'+search['id']+'/results/'+hit['id']
    read=t['api'](endpoint);choice=next(option for option in read['fork_options'] if option['supported'] and option['label']=='Launch profile: Installed '+agent)
    child=t['api'](endpoint+'/fork',{'configuration_id':choice['id'],'worktree':{'branch':'native-api-proof'}})
    assert child['launch_profile']=='Installed '+agent
    assert len(child['workspace']['repositories'])==2
    assert child['workdir']!=str(root)
    assert all(pathlib.Path(r['worktree']['path']).is_dir() for r in child['workspace']['repositories'])
   current=None;screen='';deadline=time.monotonic()+35
   while time.monotonic()<deadline:
    data=t['api'](f"/sessions/{child['id']}/conversations");current=data['current']
    screen=subprocess.check_output(['tmux','capture-pane','-p','-J','-S','-100','-t','='+child['tmux_session']+':'],env=t['env'],text=True)
    if current['state']=='identified' and ((agent=='codex' and current['saved']) or (agent=='claude' and 'Saved assistant API proof.' in screen)):break
    time.sleep(.15)
   pathlib.Path(f'/tmp/lectern-grouped-native-path-integrated-{agent}-screen.txt').write_text(screen)
   diagnostic=pathlib.Path('/tmp/lectern-grouped-native-path-integrated-diagnostic');diagnostic.mkdir(exist_ok=True,mode=0o700)
   (diagnostic/(agent+'-response.json')).write_text(json.dumps(data))
   for native in home.glob('sessions/**/*.jsonl'):
    shutil.copy2(native,diagnostic/native.name)
   (diagnostic/(agent+'-row.json')).write_text(json.dumps(child))
   assert current and current['state']=='identified',current
   assert (current['id']==source_id)==(mode=='resume'),current
   if agent=='claude':assert 'Saved assistant API proof.' in screen,'native history did not render'
   if agent=='codex':
    assert current['saved'],current
    history=t['api'](f"/sessions/{child['id']}/conversations/{current['id']}")
    assert any('Saved assistant API proof.' in m['text'] for m in history['messages'])
   assert file.read_bytes().startswith(original) if mode=='resume' else file.read_bytes()==original
   receipt={'agent':agent,'current':current,'workdir':child['workdir'],'original_history_preserved':True,'mode':mode,'copied_history_readable':True}
   pathlib.Path(f'/tmp/lectern-grouped-native-path-integrated-{agent}-receipt.json').write_text(json.dumps(receipt))
   print('PASS:',agent,mode,'via grouped workspace + named profile + real API + installed CLI; exact identity, correct cwd, saved history readable, original history preserved; no model turn',flush=True)
  finally:
   try:next(generator)
   except StopIteration:pass
