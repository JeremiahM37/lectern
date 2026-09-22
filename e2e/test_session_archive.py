"""Archive lifecycle uses actual tmux processes and durable SQLite snapshots."""
import subprocess,time
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_terminal_dashboard import Dashboard


def choose_console_action(d, label):
    """Move to a visible action by its rendered selection highlight."""
    for _ in range(40):
        for y, line in enumerate(d.screen.display):
            if label not in line:
                continue
            cells = d.screen.buffer[y]
            start = line.index(label)
            end = start + len(label)
            if any(getattr(cells[x], 'bg', '') not in ('', 'default', 'black')
                   for x in range(start, min(end, len(cells)))):
                d.send('\r')
                return
        d.send('\x1b[B')
    raise AssertionError(f"Action {label!r} was not selectable:\n{d.text}")
from test_session_restore import request
from test_session_groups import patch


def seed(t):
    command="printf '\\nARCHIVE HISTORY PROOF <script>owned</script>\\n'"
    subprocess.run(['tmux','send-keys','-t','=terminal-test:',command,'Enter'],env=t['env'],check=True)
    end=time.monotonic()+3
    while time.monotonic()<end:
        text=subprocess.check_output(['tmux','capture-pane','-p','-t','=terminal-test:'],env=t['env'],text=True)
        if 'ARCHIVE HISTORY PROOF <script>owned</script>' in text:return
        time.sleep(.05)
    raise AssertionError(text)


def gone(t):
    return subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL).returncode!=0


def test_archive_requires_explicit_stop_and_unarchive_does_not_restart(real_terminal):
    t=real_terminal;seed(t);path=f"/sessions/{t['id']}";patch(t,t['id'],{'group_path':'Work/Archive'})
    assert request(t,'POST',path+'/archive',{})[0]==409;assert not gone(t)
    kept=t['root']/'keep-this-work.txt';kept.write_text('Uncommitted work remains')
    original=t['api'](path)
    status,row=request(t,'POST',path+'/archive',{'stop':True})
    assert status==200 and row['archived_at'] and row['ended_at'] and row['status']=='dead',row
    assert gone(t) and kept.read_text()=='Uncommitted work remains'
    for k in ['id','name','project_id','target_id','workdir','created_at','group_path']:assert row[k]==original[k]
    assert t['api']('/sessions?all=true')==[]
    assert [s['id'] for s in t['api']('/sessions?archived=true')]==[t['id']]
    assert 'archive_text' not in row
    text=t['api'](path+'/archive/history')['text'];assert 'ARCHIVE HISTORY PROOF' in text
    assert request(t,'POST',path+'/archive',{'stop':True})[1]['archived_at']==row['archived_at']
    assert request(t,'DELETE',path+'/archive')[0]==200
    assert gone(t) and t['api'](path)['ended_at'] is not None
    assert t['api'](path+'/archive/history')['text']==text
    assert t['api']('/sessions?archived=true')==[]
    assert [s['id'] for s in t['api']('/sessions?all=true')]==[t['id']]
    # Archival is organization, not a new end event. Preserve its original time.
    ended=t['api'](path)['ended_at']
    assert request(t,'POST',path+'/archive',{'stop':False})[0]==200
    assert t['api'](path)['ended_at']==ended
    assert t['api'](path+'/archive/history')['text']==text


def test_released_live_session_cannot_be_archived_as_stopped(real_terminal):
    t=real_terminal;path=f"/sessions/{t['id']}"
    assert request(t,'DELETE',path)[0]==200
    for stop in [False,True]:assert request(t,'POST',path+'/archive',{'stop':stop})[0]==409
    assert not gone(t)
    assert t['api'](path)['archived_at'] is None


@pytest.mark.parametrize('width',[390,1440])
def test_web_archive_output_and_unarchive(page,real_terminal,width):
    t=real_terminal;seed(t);page.set_viewport_size({'width':width,'height':900});page.goto(t['url']+'/#sessions')
    page.locator('summary[aria-label="More actions for Real terminal"]').click()
    page.once('dialog',lambda d:d.dismiss());page.get_by_role('button',name='Stop and archive',exact=True).click()
    assert not gone(t)
    page.locator('summary[aria-label="More actions for Real terminal"]').click()
    page.once('dialog',lambda d:d.accept());page.get_by_role('button',name='Stop and archive',exact=True).click()
    expect(page.locator('.scard')).to_have_count(0)
    subprocess.run(['tmux','new-session','-d','-s','keep-active','bash --norc'],env=t['env'],check=True)
    t['api']('/sessions/adopt',{'target_id':t['target_id'],'tmux_session':'keep-active','workdir':str(t['root']),'name':'Other active session','agent':'claude'})
    page.locator('#sess-scope').select_option('archived')
    expect(page.locator('#sess-badge')).to_have_text('1')
    card=page.locator('.scard',has_text='Real terminal');expect(card).to_contain_text('archived')
    card.locator('summary').first.click()
    panel=card.locator('.action-menu-panel')
    expect(panel).to_be_visible()
    # The menu must stay in usable space, not underneath fixed navigation.
    page.wait_for_function("""() => { const p=document.querySelector('.scard .action-menu[open] .action-menu-panel'); if(!p?.style.maxHeight)return false; const b=p.getBoundingClientRect(),n=document.querySelector('#tabbar').getBoundingClientRect(); return innerWidth>=1000 ? b.left>=n.right : b.bottom<=n.top; }""")
    menu_box=panel.bounding_box();nav_box=page.locator('#tabbar').bounding_box()
    if width>=1000:assert menu_box['x']>=nav_box['x']+nav_box['width']
    else:assert menu_box['y']+menu_box['height']<=nav_box['y']
    card.get_by_role('button',name='Archived terminal output',exact=True).click()
    dialog=page.get_by_role('dialog',name='Archived terminal output');expect(dialog.locator('pre')).to_contain_text('ARCHIVE HISTORY PROOF')
    assert dialog.locator('script').count()==0
    assert dialog.evaluate('(el)=>el.scrollWidth<=el.clientWidth+1')
    page.screenshot(path=f'/tmp/lectern-archive-{width}.png')
    dialog.get_by_role('button',name='Close archived output').click()
    card.locator('summary').first.click()
    card.get_by_role('button',name='Unarchive record',exact=True).click()
    expect(page.locator('.scard')).to_have_count(0);assert gone(t)
    page.locator('#sess-scope').select_option('all');expect(page.locator('.scard',has_text='Real terminal')).to_have_count(1)


def test_console_archive_view_output_and_unarchive(real_terminal):
    t=real_terminal;seed(t);d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('m');d.wait('Actions')
        choose_console_action(d,'Stop and archive');d.wait('Stop and archive?');d.send('n');assert not gone(t)
        d.send('m');d.wait('Actions')
        choose_console_action(d,'Stop and archive');d.wait('Stop and archive?');d.send('y');d.wait('Stop and archive completed')
        d.send('A');d.wait('archive');d.wait('Real terminal');d.send('m');d.wait('Unarchive record')
        d.send('\x1b[B\r');d.wait('ARCHIVE HISTORY PROOF')
        d.send('m');d.wait('Unarchive record');d.send('\r');d.wait('Unarchive record completed');assert gone(t)
        d.send('z');d.wait('Real terminal');d.quit()
    finally:d.close()
