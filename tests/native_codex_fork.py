import shutil,re,tempfile,pathlib,json,uuid,os,pty,termios,fcntl,struct,subprocess,time,select,signal,hashlib
with tempfile.TemporaryDirectory(prefix='lec-native-codex-') as tmp:
 home=pathlib.Path(tmp);work=home/'workspace';work.mkdir();saved=home/'sessions';saved.mkdir()
 source=str(uuid.uuid4());stamp='2026-09-10T04:00:00.000Z'
 file=saved/f'rollout-2026-09-10T04-00-00-{source}.jsonl'
 records=[{'timestamp':stamp,'type':'session_meta','payload':{'id':source,'timestamp':stamp,'cwd':str(work),'originator':'codex_cli_rs','cli_version':'0.148.0','source':'cli','model_provider':'openai'}}, {'timestamp':stamp,'type':'response_item','payload':{'type':'message','role':'user','content':[{'type':'input_text','text':'FICTIONAL FORK FIXTURE: native context proof. Do not run any commands.'}]}}, {'timestamp':stamp,'type':'response_item','payload':{'type':'message','role':'assistant','content':[{'type':'output_text','text':'Saved assistant history proof.'}]}}]
 file.write_text(''.join(json.dumps(r)+'\n' for r in records));original=hashlib.sha256(file.read_bytes()).hexdigest()
 auth=pathlib.Path.home()/'.codex/auth.json'
 if auth.exists():shutil.copy2(auth,home/'auth.json')
 (home/'config.toml').write_text('check_for_update_on_startup = false\n[projects.'+json.dumps(str(work))+']\ntrust_level = \"trusted\"\n')
 master,slave=pty.openpty();fcntl.ioctl(slave,termios.TIOCSWINSZ,struct.pack('HHHH',35,120,0,0))
 def controlling():os.setsid();fcntl.ioctl(0,termios.TIOCSCTTY,0)
 proc=subprocess.Popen(['codex','fork',source,'--no-alt-screen'],cwd=work,env={**{k:v for k,v in os.environ.items() if not k.startswith('CODEX_')},'CODEX_HOME':str(home),'TERM':'xterm-256color'},stdin=slave,stdout=slave,stderr=slave,preexec_fn=controlling)
 output=b''; trusted=False
 try:
  deadline=time.monotonic()+35
  while time.monotonic()<deadline:
   if select.select([master],[],[],.1)[0]:
    block=os.read(master,65536);output+=block
    if b'\x1b[6n' in block:os.write(master,b'\x1b[1;1R')
   plain=re.sub(rb'\x1b\[[0-9;?]*[A-Za-z]',b'',output)
   if not trusted and b'doyoutrust' in re.sub(rb'\s+',b'',plain.lower()):os.write(master,b'\r');trusted=True
   files=list(saved.rglob('*.jsonl'))
   if len(files)>1:break
   if proc.poll() is not None:break
  (pathlib.Path('/tmp/native-codex-fork-screen.txt')).write_bytes(output)
  children=[p for p in saved.rglob('*.jsonl') if p!=file]
  assert children,'No fork persisted; inspect /tmp/native-codex-fork-screen.txt'
  data=children[0].read_text();assert 'Saved assistant history proof.' in data and 'FICTIONAL FORK FIXTURE' in data
  first=json.loads(data.splitlines()[0]);assert first['payload']['id']!=source
  assert hashlib.sha256(file.read_bytes()).hexdigest()==original
  print('PASS: installed Codex CLI fork creates a different native ID, copies both saved messages, and leaves parent byte-identical; no new prompt sent')
 finally:
  if proc.poll() is None:os.killpg(proc.pid,signal.SIGTERM);proc.wait(timeout=10)
  os.close(master);os.close(slave)
