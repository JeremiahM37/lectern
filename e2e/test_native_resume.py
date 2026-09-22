"""Continue exact native history through real API, browser and controlling PTY."""
import concurrent.futures
import hashlib
import json
import subprocess
import time
import pytest
from playwright.sync_api import expect
from test_native_history import prepare
from test_terminal_workspace import real_terminal
from test_terminal_dashboard import Dashboard
from test_session_restore import request


def stopped(t):
    row=t['api'](f"/sessions/{t['id']}")
    assert request(t,'DELETE',f"/sessions/{t['id']}")[0]==200
    subprocess.run(['tmux','kill-session','-t','='+row['tmux_session']],env=t['env'],check=True)


def argv(t):
    p=t['root']/'fork-argv.json'
    end=time.monotonic()+8
    while time.monotonic()<end and not p.exists():time.sleep(.05)
    return json.loads(p.read_text())


@pytest.mark.parametrize('agent,width',[('claude',390),('codex',1440)])
def test_web_resumes_exact_stopped_conversation(page,real_terminal,agent,width):
    t=real_terminal;cid,file,_=prepare(t,agent);before=file.read_bytes();stopped(t)
    page.set_viewport_size({'width':width,'height':900});page.goto(t['url']+'/#sessions')
    page.locator('#sess-scope').select_option('all')
    title='Real terminal' if agent=='claude' else 'Native source'
    card=page.locator('.scard',has_text=title)
    card.locator('summary').first.click();card.get_by_role('button',name='Saved conversations',exact=True).click()
    dialog=page.get_by_role('dialog',name='Saved conversations')
    expect(dialog.locator('.nh-resume')).to_be_disabled()
    dialog.locator('.nh-select').select_option(cid)
    expect(dialog.locator('.nh-messages')).to_contain_text('History proof 229')
    dialog.locator('.nh-resume').click()
    expect(dialog).to_contain_text('previous terminal must be stopped')
    dialog.locator('.nh-cancel').click();assert not (t['root']/'fork-argv.json').exists()
    dialog.locator('.nh-resume').click();dialog.locator('.nh-fork-name').fill('Continued exact history')
    dialog.get_by_role('button',name='Start resumed session',exact=True).click()
    expect(dialog).not_to_be_visible(timeout=15000)
    assert argv(t)==(['--resume',cid] if agent=='claude' else ['resume',cid])
    assert file.read_bytes()==before
    rows=t['api']('/sessions');resumed=next(r for r in rows if r['name']=='Continued exact history')
    assert resumed['resume_id']==cid
    assert resumed['workdir']==str(t['root'])
    # The resumed terminal opens within Lectern and paints real process output.
    expect(page.locator('#terminal-workspace')).to_be_visible()
    frame=page.frame_locator('#terminal-workspace .terminal-tabpanel:not([hidden]) iframe')
    expect(frame.locator('#connection')).to_have_text('Connected',timeout=15000)



def test_resume_checks_live_released_foreign_and_duplicate_requests(real_terminal):
    t=real_terminal;cid,file,foreign=prepare(t);path=f"/sessions/{t['id']}/resume"
    assert request(t,'POST',path,{'conversation_id':cid})[0]==409
    assert request(t,'POST',path,{'conversation_id':foreign})[0]==409
    assert request(t,'POST',path,{'conversation_id':'--last'})[0]==409
    assert request(t,'DELETE',f"/sessions/{t['id']}")[0]==200
    # A released terminal still runs, so it cannot be resumed as a second writer.
    assert request(t,'POST',path,{'conversation_id':cid})[0]==409
    subprocess.run(['tmux','kill-session','-t','=terminal-test'],env=t['env'],check=True)
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        results=list(pool.map(lambda _:request(t,'POST',path,{'conversation_id':cid}),range(2)))
    assert sorted(code for code,_ in results)==[201,409]
    resumed=next(body for code,body in results if code==201)
    assert argv(t)==['--resume',cid]
    assert resumed['resume_id']==cid
    assert request(t,'POST',path,{'conversation_id':cid})[0]==409
    # Explicit end permits another continuation, preserving the source history.
    assert request(t,'DELETE',f"/sessions/{resumed['id']}")[0]==200
    assert request(t,'POST',path,{'conversation_id':cid})[0]==201


def test_console_continues_exact_history(real_terminal):
    t=real_terminal;cid,_,_=prepare(t);stopped(t);d=Dashboard(t)
    try:
        d.wait('No matching items');d.send('z');d.wait('Real terminal');d.send('H');d.wait('Saved conversations')
        d.send('\t\x1b[C\x1b[C\x13');d.wait('previous terminal must be stopped');d.send('y')
        d.wait('Resume conversation completed');assert argv(t)==['--resume',cid]
        d.quit()
    finally:d.close()
