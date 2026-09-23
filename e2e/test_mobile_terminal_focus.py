"""Mobile reading layout and soft keys against real ttyd/tmux."""
import subprocess
import re
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal,open_terminal
from test_terminal_tabs import attach,frame,ready

def assert_compact_controls_clear_viewport(one):
    viewport = one.locator('#agent-terminal .xterm-viewport').bounding_box()
    tools = one.locator('#terminal-tools-summary').bounding_box()
    status = one.locator('#compact-status').bounding_box()
    assert viewport and tools and status
    for control in (tools, status):
        assert control['x'] + control['width'] <= viewport['x'] or control['x'] >= viewport['x'] + viewport['width'] or \
            control['y'] + control['height'] <= viewport['y'] or control['y'] >= viewport['y'] + viewport['height'], \
            (viewport, control)


def test_mobile_focus_gains_space_preserves_connection_and_allows_navigation(page,real_terminal):
    t=real_terminal
    with page.context.browser.new_context(viewport={'width':390,'height':900},is_mobile=True,has_touch=True) as context:
        phone=context.new_page();sockets=[];phone.on('websocket',lambda ws:sockets.append(ws.url))
        phone.goto(t['url']+'/#sessions');attach(phone,'Real terminal');one=frame(phone,t['id']);ready(one)
        expect(phone.get_by_role('button',name='Show navigation')).to_be_visible()
        expect(phone.locator('#tabbar')).not_to_be_visible()
        expect(one.locator('#terminal-keybar')).to_be_visible()
        one.locator('body').evaluate('()=>window.focusIdentity="same"')
        focused=one.locator('#agent-terminal').bounding_box()['height']
        assert focused >= 730,focused
        phone.screenshot(path='/tmp/lectern-mobile-focused.png')
        phone.get_by_role('button',name='Show navigation').tap()
        expect(phone.locator('#tabbar')).to_be_visible()
        expect(one.locator('body')).not_to_have_class(re.compile('.*compact-terminal.*'))
        expect(one.locator('#terminal-keybar')).to_be_visible()
        phone.wait_for_function("document.querySelector('#terminal-workspace').getBoundingClientRect().top >= document.querySelector('#topbar').getBoundingClientRect().bottom - 1")
        expanded=one.locator('#agent-terminal').bounding_box()['height']
        assert focused-expanded >= 100,(focused,expanded)
        phone.get_by_role('button',name='Focus terminal').tap()
        expect(one.locator('body')).to_have_class(re.compile('.*compact-chrome.*'))
        assert_compact_controls_clear_viewport(one)
        # Files, desktop launch and global search remain available while focused.
        # In compact mode the direct toolbar button is folded into Tools.
        one.locator('#terminal-tools-summary').tap();one.locator('#compact-files').tap()
        one.get_by_role('button',name='hello.txt',exact=True).tap()
        expect(one.locator('#preview-body')).to_contain_text('A useful artifact')
        one.locator('#preview-dialog [data-close]').click();one.locator('#files-dialog [data-close]').click()
        one.locator('#terminal-tools-summary').tap();expect(one.locator('#compact-desktop')).to_be_visible()
        assert one.locator('#compact-desktop').get_attribute('href')==one.locator('#desktop').get_attribute('href')
        one.locator('#terminal-tools-summary').tap()
        phone.get_by_role('button',name='Search sessions and actions').tap()
        dialog=phone.get_by_role('dialog',name='Search Lectern');dialog.get_by_role('combobox').fill('task board');dialog.get_by_role('option').tap()
        expect(phone.locator('#tabbar')).to_be_visible()
        phone.locator('.tab[data-tab="terminals"]').tap();expect(phone.locator('#tabbar')).not_to_be_visible()
        assert one.locator('body').evaluate('()=>window.focusIdentity')=='same'
        assert len(sockets)==1,sockets
        for size in [{'width':844,'height':390},{'width':1440,'height':900},{'width':390,'height':550},{'width':390,'height':900}]:
            phone.set_viewport_size(size)
            if size['width']<1024:expect(one.locator('#terminal-keybar')).to_be_visible()
            else:expect(one.locator('#terminal-keybar')).not_to_be_visible()
            expect(one.locator('#agent-terminal')).to_be_visible()
            assert one.locator('#agent-terminal').bounding_box()['height']>200
            if size['width']<1024 and size['height'] in (640, 550, 900):
                assert_compact_controls_clear_viewport(one)
            assert phone.evaluate('document.documentElement.scrollWidth<=innerWidth')
        # Store the preference, restore on reload, then return to focus mode.
        phone.get_by_role('button',name='Show navigation').tap();phone.reload();ready(frame(phone,t['id']))
        expect(phone.get_by_role('button',name='Focus terminal')).to_be_visible()
        expect(phone.locator('#tabbar')).to_be_visible()
        phone.get_by_role('button',name='Focus terminal').tap()
        assert len(sockets)==2 # Only the deliberate page reload reconnects.


@pytest.mark.parametrize('embedded',[True,False],ids=['embedded','standalone'])
def test_mobile_terminal_keys_reach_real_application_cursor_mode(page,real_terminal,embedded):
    t=real_terminal;page.set_viewport_size({'width':390,'height':844})
    script=t['root']/'read-keys.py'
    script.write_text("""import os,sys,tty,termios
fd=sys.stdin.fileno();saved=termios.tcgetattr(fd)
try:
 tty.setraw(fd)
 sys.stdout.write('\\x1b[?1hKEYBAR READY\\r\\n');sys.stdout.flush()
 data=b''
 while len(data)<15:data+=os.read(fd,15-len(data))
 open('keybar-bytes','wb').write(data)
finally:
 sys.stdout.write('\\x1b[?1l');sys.stdout.flush();termios.tcsetattr(fd,termios.TCSADRAIN,saved)
""")
    if embedded:
        page.goto(t['url']+'/#sessions');attach(page,'Real terminal');one=frame(page,t['id']);ready(one)
    else:
        open_terminal(page,t);one=page
    one.locator('#terminal-tools-summary').click();one.locator('#pause').click()
    expect(one.get_by_role('button',name='Send Tab',exact=True)).to_be_disabled()
    one.locator('#terminal-tools-summary').click();one.locator('#pause').click()
    subprocess.run(['tmux','send-keys','-t','=terminal-test:','python3 read-keys.py','Enter'],env=t['env'],check=True)
    expect(one.locator('#agent-terminal .xterm-screen')).to_contain_text('KEYBAR READY')
    for key in ['Escape','Tab','Left arrow','Up arrow','Down arrow','Right arrow','Ctrl-C']:
        one.get_by_role('button',name='Send '+key,exact=True).click()
    proof=t['root']/'keybar-bytes'
    for _ in range(100):
        if proof.exists() and proof.stat().st_size == 15:break
        page.wait_for_timeout(50)
    assert proof.read_bytes()==b'\x1b\t\x1bOD\x1bOA\x1bOB\x1bOC\x03'
    expect(one.locator('#connection')).to_have_text('Connected')
    page.context.set_offline(True)
    one.locator('#terminal-tools-summary').click();one.locator('#reconnect').click()
    # A phone with no network is a state of its own, not a generic reconnect.
    expect(one.locator('#compact-status')).to_have_attribute('aria-label','Terminal offline')
    expect(one.locator('#agent-pane .pane-title')).to_be_visible()
    expect(one.get_by_role('button',name='Send Tab',exact=True)).to_be_disabled()
    page.context.set_offline(False)
    expect(one.locator('#compact-status')).to_have_attribute('aria-label','Terminal connected',timeout=15000)
    expect(one.get_by_role('button',name='Send Tab',exact=True)).to_be_enabled()
