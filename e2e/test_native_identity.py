"""Native selection follows a live tmux process, never workspace recency."""
import json,os,re,shlex,shutil,sqlite3,subprocess,sys,time,uuid
from pathlib import Path
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_native_history import prepare
from test_terminal_dashboard import Dashboard


def running(t,agent):
    cid,file,_=prepare(t,agent)
    records=[json.loads(line) for line in file.read_text().splitlines()]
    if agent=='codex':records[0]['payload']['source']='cli'
    if agent=='codex':records.insert(1,{'type':'session_meta','payload':{'id':str(uuid.uuid4()),'cwd':'/parent/workspace','source':'cli'}})
    file.write_text(''.join(json.dumps(r)+'\n' for r in records))
    other=str(uuid.uuid4());decoy=file.with_name(other+'.jsonl')
    changed=json.loads(json.dumps(records))
    for row in changed:
        if agent=='claude':row['sessionId']=other
        elif row['type']=='session_meta':row['payload']['id']=other
    decoy.write_text(''.join(json.dumps(r)+'\n' for r in changed).replace('History proof','Neighbor proof'));os.utime(decoy,(time.time()+60,)*2)
    script=t['root']/'identity-agent.py'
    script.write_text('''import json,os,sys,time
from pathlib import Path
agent,source,other,home,cwd=sys.argv[1:]
pid=os.getpid();start=Path('/proc',str(pid),'stat').read_text().rsplit(')',1)[1].split()[19]
if agent=='claude':
 folder=Path(home,'sessions');folder.mkdir(exist_ok=True)
 record=folder/(str(pid)+'.json')
 record.write_text(json.dumps({'pid':pid,'procStart':start,'sessionId':Path(source).stem,'cwd':cwd,'kind':'interactive','entrypoint':'cli'}))
held=[open(source)];extra=None
Path(cwd,'identity-ready').write_text(str(pid))
while True:
 if Path(cwd,'open-other').exists() and extra is None:extra=open(other)
 time.sleep(.03)
''')
    source=t['api'](f"/sessions/{t['id']}")
    executable='python3'
    if agent=='codex':
        executable=str(t['root']/'codex');shutil.copy2(Path(sys.executable).resolve(),executable)
    args=[executable,str(script),agent,str(file),str(decoy),str(t['root']/'native-home'),str(t['root'])]
    subprocess.run(['tmux','respawn-pane','-k','-t','='+source['tmux_session']+':','exec '+shlex.join(args)],env=t['env'],check=True)
    for _ in range(100):
        if (t['root']/'identity-ready').exists():break
        time.sleep(.05)
    else:raise AssertionError('native fixture did not start')
    return cid,file,decoy,int((t['root']/'identity-ready').read_text())


@pytest.mark.parametrize('agent,width',[('claude',390),('codex',1440)])
def test_web_selects_live_conversation_over_newer_neighbor(page,real_terminal,agent,width):
    t=real_terminal;cid,file,decoy,pid=running(t,agent)
    data=t['api'](f"/sessions/{t['id']}/conversations")
    assert data['current']=={'state':'identified','id':cid,'saved':True}
    page.set_viewport_size({'width':width,'height':900});page.goto(t['url']+'/#sessions')
    card=page.locator('.scard',has_text=t['api'](f"/sessions/{t['id']}")['name'])
    card.locator('summary').first.click();card.get_by_role('button',name='Saved conversations',exact=True).click()
    dialog=page.get_by_role('dialog',name='Saved conversations')
    expect(dialog.locator('.nh-select')).to_have_value(cid)
    expect(dialog.locator('.nh-messages')).to_contain_text('History proof 229')
    expect(dialog.locator('.nh-select option:checked')).to_contain_text('Current terminal')
    dialog.locator('.nh-select').select_option(decoy.stem)
    dialog.locator('.nh-refresh').click()
    expect(dialog.locator('.nh-fork')).to_be_enabled()
    expect(dialog.locator('.nh-status')).to_contain_text(decoy.stem)
    expect(dialog.locator('.nh-select')).to_have_value(decoy.stem)
    assert dialog.evaluate('(e)=>e.scrollWidth<=e.clientWidth')
    page.screenshot(path=f'/tmp/lectern-native-identity-{width}.png')


@pytest.mark.parametrize('agent',['claude','codex'])
def test_console_marks_and_defaults_to_live_conversation(real_terminal,agent):
    t=real_terminal;cid,file,decoy,pid=running(t,agent);d=Dashboard(t)
    try:
        name=t['api'](f"/sessions/{t['id']}")['name']
        d.wait(name);d.send('/'+name+'\r');d.send('H');d.wait('Current terminal')
        d.send('\x13');d.wait('History proof 030');d.quit()
    finally:d.close()


def test_claude_rejects_stale_process_and_missing_persistence(real_terminal):
    t=real_terminal;cid,file,decoy,pid=running(t,'claude')
    record=t['root']/'native-home/sessions'/f'{pid}.json';row=json.loads(record.read_text())
    file.unlink()
    current=t['api'](f"/sessions/{t['id']}/conversations")['current']
    assert current=={'state':'identified','id':cid,'saved':False}
    row['procStart']='0';record.write_text(json.dumps(row))
    assert t['api'](f"/sessions/{t['id']}/conversations")['current']['state']=='unavailable'
    row['procStart']=Path('/proc',str(pid),'stat').read_text().rsplit(')',1)[1].split()[19]
    row['cwd']='/other';record.write_text(json.dumps(row))
    assert t['api'](f"/sessions/{t['id']}/conversations")['current']['state']=='unavailable'


def test_codex_rejects_multiple_live_conversations_and_subagents(real_terminal):
    t=real_terminal;cid,file,decoy,pid=running(t,'codex')
    (t['root']/'open-other').touch()
    for _ in range(100):
        current=t['api'](f"/sessions/{t['id']}/conversations")['current']
        if current['state']=='ambiguous':break
        time.sleep(.05)
    assert current=={'state':'ambiguous','saved':False}
    records=[json.loads(line) for line in decoy.read_text().splitlines()]
    records[0]['payload']['source']={'subagent':{'thread_spawn':{'parent_thread_id':cid}}}
    decoy.write_text(''.join(json.dumps(r)+'\n' for r in records))
    assert t['api'](f"/sessions/{t['id']}/conversations")['current']=={'state':'identified','id':cid,'saved':True}
    source=t['api'](f"/sessions/{t['id']}")
    subprocess.run(['tmux','set-option','-t','='+source['tmux_session']+':','@lectern-tracking-identity','a'*32],env=t['env'],check=True)
    assert t['api'](f"/sessions/{t['id']}/conversations")['current']['state']=='changed'


@pytest.mark.parametrize('agent',['claude','codex'])
def test_unmarked_legacy_terminal_is_identified_without_writes(real_terminal,agent):
    t=real_terminal;cid,file,decoy,pid=running(t,agent)
    source=t['api'](f"/sessions/{t['id']}")
    with sqlite3.connect(t['root'].parent/'test.db') as db:
        db.execute("update sessions set tracking_identity='' where id=?",(t['id'],))
    subprocess.run(['tmux','set-option','-u','-t','='+source['tmux_session']+':','@lectern-tracking-identity'],env=t['env'],check=True)
    current=t['api'](f"/sessions/{t['id']}/conversations")['current']
    assert current=={'state':'identified','id':cid,'saved':True}
    with sqlite3.connect(t['root'].parent/'test.db') as db:
        assert db.execute('select tracking_identity from sessions where id=?',(t['id'],)).fetchone()[0]==''
    marker=subprocess.check_output(['tmux','show-options','-qv','-t','='+source['tmux_session']+':','@lectern-tracking-identity'],env=t['env'],text=True)
    assert not marker.strip()
