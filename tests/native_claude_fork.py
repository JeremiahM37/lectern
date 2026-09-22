import tempfile,pathlib,json,uuid,os,pty,termios,fcntl,struct,subprocess,time,select,signal,hashlib,shutil,re
with tempfile.TemporaryDirectory(prefix='lec-native-claude-') as tmp:
 home=pathlib.Path(tmp);work=home/'workspace';work.mkdir();config=home/'config';config.mkdir()
 source=str(uuid.uuid4());first=str(uuid.uuid4());second=str(uuid.uuid4());stamp='2026-09-10T04:00:00.000Z'
 folder=config/'projects'/re.sub(r'[^a-zA-Z0-9]','-',str(work));folder.mkdir(parents=True)
 file=folder/(source+'.jsonl')
 common={'isSidechain':False,'userType':'external','cwd':str(work),'sessionId':source,'version':'2.1.0','timestamp':stamp}
 records=[{**common,'type':'user','uuid':first,'parentUuid':None,'message':{'role':'user','content':'FICTIONAL CLAUDE FORK FIXTURE. Do not run any commands.'}},{**common,'type':'assistant','uuid':second,'parentUuid':first,'message':{'id':'msg_fixture','type':'message','role':'assistant','model':'claude-sonnet-4-6','content':[{'type':'text','text':'Saved Claude assistant proof.'}],'stop_reason':'end_turn','stop_sequence':None,'usage':{'input_tokens':10,'output_tokens':5}}}]
 file.write_text(''.join(json.dumps(r)+'\n' for r in records));original=hashlib.sha256(file.read_bytes()).hexdigest()
 auth=pathlib.Path.home()/'.claude/.credentials.json'
 if auth.exists():shutil.copy2(auth,config/'.credentials.json')
 (config/'.claude.json').write_text(json.dumps({'hasCompletedOnboarding':True,'theme':'dark','projects':{str(work):{'hasTrustDialogAccepted':True,'projectOnboardingSeenCount':1}}}))
 master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',35,120,0,0))
 def controlling():os.setsid();fcntl.ioctl(0,termios.TIOCSCTTY,0)
 proc=subprocess.Popen(['claude','--resume',source,'--fork-session'],cwd=work,env={**{k:v for k,v in os.environ.items() if not k.startswith('CLAUDE')},'CLAUDE_CONFIG_DIR':str(config),'TERM':'xterm-256color'},stdin=slave,stdout=slave,stderr=slave,preexec_fn=controlling)
 output=b''
 try:
  deadline=time.monotonic()+30
  while time.monotonic()<deadline:
   if select.select([master],[],[],.1)[0]:
    block=os.read(master,65536);output+=block
    if b'\x1b[6n' in block:os.write(master,b'\x1b[1;1R')
   plain=re.sub(rb'\x1b\[[0-9;?]*[A-Za-z]',b'',output)
   if b'SavedClaudeassistantproof.' in re.sub(rb'\s+',b'',plain):break
   if proc.poll() is not None:break
  pathlib.Path('/tmp/native-claude-fork-screen.txt').write_bytes(output)
  assert b'SavedClaudeassistantproof.' in re.sub(rb'\s+',b'',plain),'History not visible; inspect saved terminal output'
  children=[p for p in folder.glob('*.jsonl') if p!=file]
  print('Native fork files:',len(children))
  for child in children:
   rows=[json.loads(line) for line in child.read_text().splitlines() if line]
   ids={r.get('sessionId') for r in rows if r.get('sessionId')}
   print('Child filename matches record IDs:',all(x==child.stem for x in ids),'copied assistant:',any('Saved Claude assistant proof.' in json.dumps(r) for r in rows))
  assert hashlib.sha256(file.read_bytes()).hexdigest()==original
  print('PASS: installed Claude CLI loads saved assistant history with --fork-session; original transcript unchanged; no new prompt sent')
 finally:
  if proc.poll() is None:os.killpg(proc.pid,signal.SIGTERM);proc.wait(timeout=10)
  os.close(master);os.close(slave)
