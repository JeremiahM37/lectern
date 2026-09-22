"""A controller crash after tmux creation must recover the same agent, not relaunch."""
import sqlite3
import subprocess
import time
import pytest
from playwright.sync_api import expect
from conftest import _binary
from test_terminal_workspace import real_terminal
from test_interactive_worktree import setup
from test_session_restore import request


@pytest.mark.parametrize('real_terminal',[{'hold_setup_launch':True}],indirect=True)
@pytest.mark.parametrize('width',[390,1440])
def test_restart_recovers_agent_created_before_setup_publication(page,real_terminal,width):
    t=real_terminal;setup(t)
    counter=t['root'].parent/'agent-starts'
    stub=t['root'].parent/'recovery-agent.py'
    stub.write_text('from pathlib import Path\nimport time\np=Path('+repr(str(counter))+')\nwith p.open("a") as f:f.write("start\\n")\nprint("RECOVERY AGENT READY",flush=True)\nwhile True:time.sleep(1)\n')
    assert request(t,'PUT','/agents',[{'name':'claude','command':'python3 '+str(stub)}])[0]==200
    row=t['api']('/sessions',{'name':'Recovered setup proof','target_id':t['target_id'],'agent':'claude','workdir':str(t['root']),'background':True,'worktree':{}})
    restarted=None
    release=t['root']/'release-launch'
    try:
        deadline=time.monotonic()+10
        while not (t['root']/'launch-held').exists() and time.monotonic()<deadline:time.sleep(.05)
        assert (t['root']/'launch-held').exists()
        before=t['api'](f"/sessions/{row['id']}")
        assert before['setup_state']=='creating'
        original_id=subprocess.check_output(['tmux','display-message','-p','-t','='+before['tmux_session']+':','#{session_id}'],env=t['env']).decode().strip()
        t['proc'].kill();t['proc'].wait(timeout=10)
        # Verify the crash window on disk, independent of the old process.
        with sqlite3.connect(t['env']['LECTERN_DB']) as db:
            assert db.execute('select setup_state,tracking_identity from sessions where id=?',(row['id'],)).fetchone()==('creating','')
        restarted=subprocess.Popen([_binary()],cwd=t['root'],env={**t['env'],'LECTERN_SESSION_POLL':'0.2'},stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        deadline=time.monotonic()+15
        while time.monotonic()<deadline:
            try:
                recovered=t['api'](f"/sessions/{row['id']}")
                if recovered['setup_state']=='ready':break
            except Exception:pass
            time.sleep(.1)
        assert recovered['setup_state']=='ready' and recovered['ended_at'] is None,recovered
        with sqlite3.connect(t['env']['LECTERN_DB']) as db:
            identity=db.execute('select tracking_identity from sessions where id=?',(row['id'],)).fetchone()[0]
            assert len(identity)==32
        assert 'tracking_identity' not in recovered
        assert counter.read_text()=='start\n'
        assert subprocess.check_output(['tmux','display-message','-p','-t','='+before['tmux_session']+':','#{session_id}'],env=t['env']).decode().strip()==original_id
        page.set_viewport_size({'width':width,'height':900});page.goto(t['url']+'/#sessions')
        page.locator('.scard',has_text='Recovered setup proof').get_by_role('button',name='⌨ Attach',exact=True).click()
        terminal=page.frame_locator('#terminal-workspace iframe')
        expect(terminal.locator('#connection')).to_have_text('Connected',timeout=15000)
        expect(terminal.locator('#agent-terminal .xterm-screen')).to_contain_text('RECOVERY AGENT READY',timeout=15000)
        assert counter.read_text()=='start\n'
    finally:
        release.touch()
        if restarted is not None:restarted.terminate();restarted.wait(timeout=15)
