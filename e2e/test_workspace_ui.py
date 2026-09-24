"""Feature access and state retention across the cleaned-up navigation."""
import pytest
from playwright.sync_api import expect
from conftest import PHONE, DESKTOP
from session_sheet import open_advanced
from test_terminal_workspace import real_terminal, open_terminal, terminal_tool

@pytest.mark.parametrize('page',[PHONE,DESKTOP],indirect=True)
def test_settings_sections_keep_drafts_and_keyboard_navigation(page,server):
    page.goto(server+'/#targets')
    projects=page.locator('[data-settings=projects]');projects.click()
    page.locator('#imp-root').fill('/tmp/unsubmitted-project-draft')
    page.locator('[data-settings=notifications]').click()
    expect(page.locator('#imp-root')).not_to_be_visible()
    projects.click();expect(page.locator('#imp-root')).to_have_value('/tmp/unsubmitted-project-draft')
    projects.focus();page.keyboard.press('ArrowRight')
    expect(page.locator('[data-settings=notifications]')).to_be_focused()
    expect(page.locator('[data-settings=notifications]')).to_have_attribute('aria-selected','true')
    expect(page.locator('#fab')).not_to_be_visible()
    assert not page.evaluate('document.documentElement.scrollWidth>innerWidth')

@pytest.mark.parametrize('page',[PHONE,DESKTOP],indirect=True)
def test_session_search_and_secondary_actions_survive_refresh(page,real_terminal):
    t=real_terminal;page.goto(t['url']+'/#sessions')
    search=page.locator('#sess-search');search.fill('Real terminal')
    expect(page.locator('.scard')).to_have_count(1)
    page.locator('.scard .action-menu>summary').click()
    expect(page.get_by_role('button',name='Stop tracking',exact=True)).to_be_visible()
    page.wait_for_timeout(5500)
    expect(page.get_by_role('button',name='Stop tracking',exact=True)).to_be_visible()
    page.keyboard.press('Escape');expect(page.locator('.scard .action-menu')).not_to_have_attribute('open','')
    search.fill('no match');expect(page.locator('.scard')).to_have_count(0)
    search.fill('Real terminal');expect(page.locator('.scard')).to_have_count(1)

@pytest.mark.parametrize('page',[PHONE,DESKTOP],indirect=True)
def test_desktop_platform_choices_and_terminal_tools(page,real_terminal):
    open_terminal(page,real_terminal)
    expect(page.locator('#desktop')).to_have_text('Open in terminal')
    expect(page.locator('#desktop')).to_have_attribute('href',f"lectern://attach/session/{real_terminal['id']}")
    terminal_tool(page,'#desktop-setup')
    expect(page.locator('#desktop-open')).to_have_text('Open in terminal')
    expect(page.locator('#desktop-dialog')).not_to_contain_text('Kitty')
    expect(page.locator('#desktop-dialog')).not_to_contain_text('WezTerm')
    expect(page.locator('#desktop-dialog')).to_contain_text('Linux')
    expect(page.locator('#desktop-dialog')).to_contain_text('Windows')
    expect(page.locator('a[href="/desktop/setup-lectern-terminal.sh"]')).to_be_visible()
    expect(page.locator('a[href="/desktop/setup-lectern.ps1"]')).to_be_visible()
    page.locator('#desktop-dialog [data-close]').click()
    expect(page.locator('#pause')).not_to_be_visible()
    page.locator('#terminal-tools>summary').click()
    for selector in ['#pause','#find','#history','#preferences','#shell','#reconnect']:
        expect(page.locator(selector)).to_be_visible()
    page.keyboard.press('Escape')
    expect(page.locator('#pause')).not_to_be_visible()
    assert not page.evaluate('document.documentElement.scrollWidth>innerWidth')

@pytest.mark.parametrize('link',['#desktop','#desktop-open'])
def test_open_in_terminal_keeps_browser_terminal_alive(page,real_terminal,link):
    t=real_terminal
    open_terminal(page,t)
    page.evaluate('''() => {
      window.departures = [];
      for (const type of ['beforeunload', 'pagehide'])
        window.addEventListener(type, () => window.departures.push(type));
    }''')
    if link == '#desktop-open':
        terminal_tool(page,'#desktop-setup')
    page.locator(link).click()
    page.wait_for_timeout(300)
    # Chromium attempts an external-protocol navigation even when the headless
    # machine has no handler. The document stays; its live terminal must too.
    assert 'beforeunload' in page.evaluate('window.departures')
    assert 'pagehide' not in page.evaluate('window.departures')
    if link == '#desktop-open':
        page.locator('#desktop-dialog [data-close]').click()
    expect(page.locator('#agent-terminal .xterm-screen')).to_be_visible()
    page.locator('#agent-terminal').click()
    page.keyboard.type('printf click-survived > click-proof.txt')
    page.keyboard.press('Enter')
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('click-proof.txt')
    import time
    for _ in range(50):
        if (t['root']/'click-proof.txt').exists():break
        time.sleep(.1)
    assert (t['root']/'click-proof.txt').read_text()=='click-survived'


@pytest.mark.parametrize('page', [PHONE, DESKTOP], indirect=True)
def test_root_remembers_view_but_explicit_links_win(page, server):
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    page.goto(server)
    page.locator('.tab[data-tab="sessions"]').click()
    expect(page.locator('#sess-search')).to_be_visible()
    page.goto(server)
    expect(page.locator('#sess-search')).to_be_visible()
    assert page.evaluate('location.hash') == ''
    page.goto(server + '/#board')
    expect(page.locator('#fab')).to_be_visible()
    page.goto(server)
    expect(page.locator('#sess-search')).to_be_visible()
    # Terminal frame history is per tab; a new tab still reaches useful work.
    page.evaluate("localStorage.setItem('lec-last-view', 'terminals')")
    other = page.context.new_page()
    try:
        other.goto(server)
        expect(other.locator('#sess-search')).to_be_visible()
    finally:
        other.close()
    page.evaluate("localStorage.setItem('lec-last-view', 'unknown-view')")
    page.goto(server)
    expect(page.locator('#fab')).to_be_visible()
    page.goto(server + '/#%E0%A4%A')
    expect(page.locator('#fab')).to_be_visible()
    assert not errors


@pytest.mark.parametrize('page', [PHONE, DESKTOP], indirect=True)
def test_session_sheet_keeps_keyboard_focus_and_returns_to_opener(page, server):
    page.goto(server + '/#sessions')
    opener = page.locator('#sess-new')
    opener.click()
    sheet = page.get_by_role('dialog', name='New session', exact=True)
    expect(sheet).to_be_visible()
    assert sheet.evaluate('(e)=>e.contains(document.activeElement)')
    close = sheet.get_by_role('button', name='Close new session', exact=True)
    close.focus()
    page.keyboard.press('Shift+Tab')
    expect(sheet.locator('#ns-go')).to_be_focused()
    page.keyboard.press('Tab')
    expect(close).to_be_focused()
    # Native modal dialogs make the background inert without an HTML attribute.
    page.locator('#tabbar .tab').first.evaluate('(e)=>e.focus()')
    assert sheet.evaluate('(e)=>e.contains(document.activeElement)')
    open_advanced(sheet)
    sheet.get_by_label('Name', exact=True).fill('Keep this draft')
    sheet.locator('#ns-manage-profiles').click()
    profiles = page.get_by_role('dialog', name='Launch profiles', exact=True)
    expect(profiles).to_be_visible()
    page.keyboard.press('Escape')
    expect(profiles).not_to_be_visible()
    expect(sheet.locator('#ns-manage-profiles')).to_be_focused()
    expect(sheet.get_by_label('Name', exact=True)).to_have_value('Keep this draft')
    # React retains the opener across live refreshes; the draft and focus
    # return must survive the same refresh interval.
    page.wait_for_timeout(5500)
    expect(sheet.get_by_label('Name', exact=True)).to_have_value('Keep this draft')
    page.keyboard.press('Escape')
    expect(sheet).not_to_be_visible()
    expect(opener).to_be_focused()
    assert not page.locator('#tabbar').evaluate('(e)=>e.inert')


@pytest.mark.parametrize('size', [(390,844),(390,450),(1440,900)])
def test_session_launch_stays_visible_through_long_form(page, server, size):
    page.set_viewport_size({'width':size[0],'height':size[1]})
    page.goto(server + '/#sessions')
    page.locator('#sess-new').click()
    sheet = page.get_by_role('dialog', name='New session', exact=True)
    # This test is about the long form: open Advanced up front so the sheet is
    # the full-height one the assertions below were written for.
    open_advanced(sheet)
    launch = sheet.locator('#ns-go')
    sheet.evaluate('(e)=>Promise.all(e.getAnimations().map(a=>a.finished))')
    def visible_action():
        box = launch.bounding_box()
        assert box and box['height'] >= 40
        assert 0 <= box['y'] and box['y'] + box['height'] <= size[1]
        assert size[1] - (box['y'] + box['height']) <= 16
        assert sheet.evaluate('(e)=>e.scrollWidth<=e.clientWidth')
    visible_action()
    page.screenshot(path=f'/tmp/lectern-launch-action-{size[0]}-{size[1]}.png',animations='disabled')
    sheet.get_by_label('Isolate in a new Git worktree',exact=True).check()
    sheet.locator('#ns-repositories summary').click()
    visible_action()
    sheet.get_by_label('First message (optional)',exact=True).fill('Keep every setting reachable')
    visible_action()
    sheet.evaluate('(e)=>e.scrollTop=e.scrollHeight')
    message = sheet.get_by_label('First message (optional)',exact=True).bounding_box()
    assert message['y'] + message['height'] <= launch.bounding_box()['y']
    sheet.evaluate('(e)=>e.scrollTop=0')
    visible_action()
    expect(sheet.get_by_label('First message (optional)',exact=True)).to_have_value('Keep every setting reachable')
