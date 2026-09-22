import json
"""Internal terminal navigation with actual ttyd, tmux, input and output."""
import subprocess
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal, capture


def attach(page, name):
    page.locator('.tab[data-tab="sessions"]').click()
    page.locator('.scard',has_text=name).get_by_role('button',name='⌨ Attach',exact=True).click()
    expect(page.locator('.tab[data-tab="terminals"]')).to_have_class('tab on')


def frame(page, id):
    return page.frame_locator(f'iframe[src="/terminal/session/{id}?embed=1"]')


def ready(f):
    # Visibility inside a retained iframe does not establish that its parent
    # panel is visible. Wait for the actual frame before interacting with chrome.
    expect(f.owner).to_be_visible(timeout=15000)
    expect(f.locator('#agent-terminal .xterm-screen')).to_be_visible(timeout=15000)
    expect(f.locator('#connection')).to_have_text('Connected',timeout=15000)
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('$',timeout=10000)


def test_terminals_stay_connected_across_tabs_and_close_only_the_view(page,real_terminal):
    t=real_terminal;errors=[];sockets=[]
    page.on('pageerror',lambda e:errors.append(str(e)))
    page.on('websocket',lambda ws:sockets.append(ws.url))
    subprocess.run(['tmux','new-session','-d','-s','second-terminal','-c',str(t['root']),'bash --norc'],env=t['env'],check=True)
    second=t['api']('/sessions/adopt',{'target_id':t['target_id'],'tmux_session':'second-terminal','workdir':str(t['root']),'name':'Second terminal','agent':'claude'})
    page.goto(t['url']+'/#sessions')
    attach(page,'Real terminal');one=frame(page,t['id']);ready(one)
    one.locator('body').evaluate('(el)=>window.frameIdentity="keep-me"')
    one.locator('#agent-terminal').click()
    page.keyboard.type("sleep 1; echo OUTPUT-WHILE-AWAY")
    page.keyboard.press('Enter')
    page.locator('.tab[data-tab="board"]').click()
    expect(page.locator('#board')).to_be_visible()
    expect(page.locator('#terminal-workspace iframe')).to_have_count(1)
    page.locator('.tab[data-tab="terminals"]').click()
    expect(one.locator('#agent-terminal .xterm-screen')).to_contain_text('OUTPUT-WHILE-AWAY')
    assert one.locator('body').evaluate('(el)=>window.frameIdentity')=='keep-me'
    attach(page,'Second terminal');two=frame(page,second['id']);ready(two)
    two.locator('#agent-terminal').click();page.keyboard.type('echo SECOND-TAB-PROOF');page.keyboard.press('Enter')
    expect(two.locator('#agent-terminal .xterm-screen')).to_contain_text('SECOND-TAB-PROOF')
    page.get_by_role('tab',name='Real terminal',exact=True).click()
    one.locator('#agent-terminal').click();page.keyboard.type('echo FIRST-TAB-PROOF');page.keyboard.press('Enter')
    expect(one.locator('#agent-terminal .xterm-screen')).to_contain_text('FIRST-TAB-PROOF')
    assert 'FIRST-TAB-PROOF' in capture(t)
    assert 'FIRST-TAB-PROOF' not in capture(t,'second-terminal')
    # Reattaching reuses both the internal tab and its existing WebSocket.
    attach(page,'Real terminal')
    expect(page.get_by_role('tab',name='Real terminal',exact=True)).to_have_count(1)
    assert len(sockets)==2, sockets
    assert len(page.context.pages)==1
    with page.expect_popup() as popup:
        page.get_by_role('link',name='Pop out ↗').click()
    expect(popup.value.locator('#connection')).to_have_text('Connected',timeout=15000)
    popup.value.close()
    page.get_by_role('button',name='Close terminal view: Real terminal',exact=True).click()
    expect(page.get_by_role('tab',name='Second terminal',exact=True)).to_have_attribute('aria-selected','true')
    subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)
    page.get_by_role('button',name='Close terminal view: Second terminal',exact=True).click()
    expect(page.get_by_text('No open terminals',exact=True)).to_be_visible()
    subprocess.run(['tmux','has-session','-t','=second-terminal'],env=t['env'],check=True)
    assert not errors


def test_terminal_tabs_restore_and_fit_on_mobile(page,real_terminal):
    t=real_terminal;page.set_viewport_size({'width':390,'height':844})
    page.goto(t['url']+'/#sessions');attach(page,'Real terminal');one=frame(page,t['id']);ready(one)
    page.get_by_role('button',name='Show navigation',exact=True).click()
    one.locator('#agent-terminal').click();page.keyboard.type('echo BEFORE-PAGE-RELOAD');page.keyboard.press('Enter')
    expect(one.locator('#agent-terminal .xterm-screen')).to_contain_text('BEFORE-PAGE-RELOAD')
    page.reload()
    one=frame(page,t['id']);ready(one)
    expect(one.locator('#agent-terminal .xterm-screen')).to_contain_text('BEFORE-PAGE-RELOAD')
    expect(page.get_by_role('tab',name='Real terminal',exact=True)).to_have_attribute('aria-selected','true')
    # Opening the root in this same browser tab restores its retained terminal.
    page.goto(t['url'])
    one=frame(page,t['id']);ready(one)
    expect(one.locator('#agent-terminal .xterm-screen')).to_contain_text('BEFORE-PAGE-RELOAD')
    expect(page.get_by_role('tab',name='Real terminal',exact=True)).to_have_attribute('aria-selected','true')
    for size in [{'width':844,'height':390},{'width':390,'height':600},{'width':390,'height':844}]:
        page.set_viewport_size(size)
        page.locator('.tab[data-tab="board"]').click()
        page.locator('.tab[data-tab="terminals"]').click()
        expect(one.locator('#agent-terminal')).to_be_visible()
        expect(one.locator('#terminal-keybar')).to_be_visible()
        assert one.locator('#agent-terminal').bounding_box()['height']>80
        assert page.evaluate('document.documentElement.scrollWidth<=innerWidth')
        bounds=page.locator('#terminal-workspace').bounding_box()
        nav=page.locator('#tabbar').bounding_box()
        assert bounds['y']+bounds['height']<=nav['y']+1
    # File tools still work within the terminal frame.
    one.locator('#files').click()
    one.get_by_role('button',name='hello.txt',exact=True).click()
    expect(one.locator('#preview-body')).to_contain_text('A useful artifact')
    one.locator('#preview-dialog [data-close]').click()
    one.locator('#files-dialog [data-close]').click()
    page.screenshot(path='/tmp/lectern-in-app-terminal-mobile.png')


def test_terminal_tabs_restore_in_a_fresh_browser_context(browser, real_terminal):
    """A browser restart keeps the terminal views while the hosted tmux lives."""
    t = real_terminal
    seed = {"active": f"/terminal/session/{t['id']}",
            "tabs": [{"path": f"/terminal/session/{t['id']}", "label": "Real terminal"}]}
    with browser.new_context(viewport={"width": 1440, "height": 900}) as context:
        value = json.dumps(json.dumps(seed))
        context.add_init_script(
            f"localStorage.setItem('lec-terminal-tabs-durable-v1', {value});"
            "localStorage.setItem('lec-last-view', 'terminals');")
        page = context.new_page()
        page.goto(t['url'])
        expect(page.locator('.tab[data-tab="terminals"]')).to_have_class('tab on')
        expect(page.get_by_role('tab', name='Real terminal', exact=True)).to_have_attribute('aria-selected', 'true')
        f = frame(page, t['id'])
        ready(f)
        page.close()
