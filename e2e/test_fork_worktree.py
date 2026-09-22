"""Conversation forks and Git allocations form one real terminal workflow."""
import json,re,subprocess,time,uuid
from pathlib import Path
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_native_history import prepare
from test_session_restore import request
from test_terminal_dashboard import Dashboard

def setup(t,agent):
    cid,file,_=prepare(t,agent)
    def git(*args):
        return subprocess.check_output(['git','-C',str(t['root']),*args],text=True).strip()
    with (t['root']/'.git/info/exclude').open('a') as exclude:exclude.write('\n.console-config/\n')
    (t['root']/'base.txt').write_text('committed base')
    git('add','hello.txt','base.txt')
    git('-c','user.name=Fixture','-c','user.email=fixture@localhost','commit','-qm','Base')
    (t['root']/'base.txt').write_text('uncommitted parent change')
    (t['root']/'parent-only.txt').write_text('Untracked parent file')
    return cid,file,git,git('status','--porcelain')

def argv(directory):
    file=directory/'fork-argv.json';end=time.monotonic()+10
    while time.monotonic()<end:
        try:return json.loads(file.read_text())
        except (FileNotFoundError,json.JSONDecodeError):time.sleep(.05)
    raise AssertionError('fork did not launch in '+str(directory))

def check(t,row,cid,file,before,git,status,agent):
    deadline=time.monotonic()+15
    while row.get('setup_state')=='creating' and time.monotonic()<deadline:
        time.sleep(.05)
        row=t['api'](f"/sessions/{row['id']}")
    assert row.get('setup_state')!='failed',row.get('setup_error')
    dest=Path(row['workdir'])
    assert row['workspace']['state']=='ready' and row['workspace']['path']==str(dest)
    assert dest!=t['root'] and (dest/'base.txt').read_text()=='committed base'
    assert not (dest/'parent-only.txt').exists()
    assert (t['root']/'base.txt').read_text()=='uncommitted parent change'
    assert file.read_bytes()==before and git('status','--porcelain')==status
    args=argv(dest)
    if agent=='codex':
        assert cid in args
        assert args[-2:]==['--cd',str(dest)]
    else:
        assert args[args.index('--resume')+1]==str(file)
        assert '--fork-session' in args
    source=t['api'](f"/sessions/{t['id']}")
    subprocess.run(['tmux','has-session','-t','='+source['tmux_session']],env=t['env'],check=True)
    return dest

@pytest.mark.parametrize('agent',['claude','codex'])
def test_fork_worktree_then_resume_stays_in_allocated_directory(real_terminal,agent):
    t=real_terminal;cid,file,git,status=setup(t,agent);before=file.read_bytes()
    row=t['api'](f"/sessions/{t['id']}/fork",{'conversation_id':cid,'worktree':{'branch':'fork-context'}})
    dest=check(t,row,cid,file,before,git,status,agent)
    assert row['workspace']['branch']=='fork-context'
    # Give the fixture agent a native child history so an exact continuation can
    # verify inherited configuration without invoking a paid model.
    child_id=str(uuid.uuid4())
    records=[]
    for line in file.read_text().splitlines():
        record=json.loads(line)
        if agent=='claude':record.update(sessionId=child_id,cwd=str(dest))
        elif record.get('type')=='session_meta':record['payload'].update(id=child_id,cwd=str(dest))
        records.append(record)
    folder=t['root']/'native-home'/('sessions' if agent=='codex' else 'projects/'+re.sub(r'[^a-zA-Z0-9]','-',str(dest)))
    folder.mkdir(parents=True,exist_ok=True)
    (folder/(child_id+'.jsonl')).write_text(''.join(json.dumps(r)+'\n' for r in records))
    assert request(t,'DELETE',f"/sessions/{row['id']}")[0]==200
    (dest/'fork-argv.json').unlink()
    resumed=t['api'](f"/sessions/{row['id']}/resume",{'conversation_id':child_id})
    assert resumed['workdir']==str(dest)
    args=argv(dest)
    assert child_id in args and '--fork-session' not in args
    if agent=='codex':assert args==['resume',child_id,'--cd',str(dest)]
    assert file.read_bytes()==before
    status,detail=request(t,'DELETE',f"/sessions/{row['id']}/worktree")
    assert status==409 and str(resumed['id']) in detail['detail'] and dest.exists()
    assert request(t,'DELETE',f"/sessions/{resumed['id']}")[0]==200
    (dest/'fork-argv.json').unlink()
    assert request(t,'DELETE',f"/sessions/{row['id']}/worktree")[0]==200
    assert not dest.exists()
    status,detail=request(t,'POST',f"/sessions/{resumed['id']}/resume",{'conversation_id':child_id})
    assert status==409 and 'working directory is unavailable' in detail['detail']
    assert file.read_bytes()==before

@pytest.mark.parametrize('agent,width',[('claude',390),('codex',1440)])
def test_web_forks_history_into_worktree(page,real_terminal,agent,width):
    t=real_terminal;cid,file,git,status=setup(t,agent);before=file.read_bytes()
    page.set_viewport_size({'width':width,'height':900})
    page.goto(t['url']+'/#sessions')
    card=page.locator('.scard',has_text=t['api'](f"/sessions/{t['id']}")['name'])
    card.locator('summary').first.click();card.get_by_role('button',name='Saved conversations',exact=True).click()
    dialog=page.get_by_role('dialog',name='Saved conversations')
    dialog.locator('.nh-select').select_option(cid)
    dialog.get_by_role('button',name='Fork conversation',exact=True).click()
    dialog.locator('.nh-fork-name').fill('Isolated conversation')
    dialog.locator('.nh-workspace').select_option('isolated')
    expect(dialog.locator('.nh-confirm')).to_contain_text('Uncommitted changes stay')
    dialog.locator('.nh-branch').fill('web-context')
    assert dialog.evaluate('(e)=>e.scrollWidth<=e.clientWidth')
    page.screenshot(path=f'/tmp/lectern-fork-worktree-{width}.png')
    dialog.get_by_role('button',name='Create fork',exact=True).click()
    expect(dialog).not_to_be_visible(timeout=20000)
    row=next(r for r in t['api']('/sessions') if r['name']=='Isolated conversation')
    check(t,row,cid,file,before,git,status,agent)

@pytest.mark.parametrize('agent',['claude','codex'])
def test_console_forks_history_into_worktree(real_terminal,agent):
    t=real_terminal;cid,file,git,status=setup(t,agent);before=file.read_bytes()
    source=t['api'](f"/sessions/{t['id']}")
    d=Dashboard(t)
    try:
        d.wait(source['name']);d.send('/'+source['name']+'\r');d.send('H');d.wait('Saved conversations')
        d.send('\t\x1b[C\tConsole fork\t\x1b[C\x13')
        d.wait('Uncommitted');d.wait('changes stay');d.send('y');d.wait('Workspace setup started.')
        row=next(r for r in t['api']('/sessions') if r['name']=='Console fork')
        check(t,row,cid,file,before,git,status,agent)
        d.quit()
    finally:d.close()

def test_worktree_fork_does_not_invent_native_resume_support(real_terminal):
    t=real_terminal;cid,file,git,status=setup(t,'codex')
    spec={'name':'codex','command':'python3 '+str(t['root']/'agent.py'),'env':{'CODEX_HOME':str(t['root']/'native-home')},'fork_args':['fork','{id}']}
    assert request(t,'PUT','/agents',[spec])[0]==200
    row=t['api'](f"/sessions/{t['id']}/fork",{'conversation_id':cid,'worktree':{}})
    argv(Path(row['workdir']))
    assert request(t,'DELETE',f"/sessions/{row['id']}")[0]==200
    assert t['api'](f"/sessions/{row['id']}/conversations")['resume_supported'] is False
