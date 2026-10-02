"""Side-by-side terminals and an unclipped tools menu, on real ttyd and tmux."""
import subprocess
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal, capture
from session_sheet import nav_to


def attach(page, name):
    page.locator('.tab[data-tab="sessions"]').click()
    page.locator('.scard',has_text=name).get_by_role('button',name='⌨ Terminal',exact=True).click()
    expect(page.locator('#terminal-workspace')).to_be_visible()


def frame(page, id):
    return page.frame_locator(f'iframe[src="/terminal/session/{id}?embed=1"]')


def ready(f):
    expect(f.owner).to_be_visible(timeout=15000)
    expect(f.locator('#connection')).to_have_text('Connected',timeout=15000)
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('$',timeout=10000)


def second_session(t, name='Second terminal', tmux='second-terminal'):
    subprocess.run(['tmux','new-session','-d','-s',tmux,'-c',str(t['root']),'bash --norc'],env=t['env'],check=True)
    return t['api']('/sessions/adopt',{'target_id':t['target_id'],'tmux_session':tmux,
                                       'workdir':str(t['root']),'name':name,'agent':'claude'})


def test_tools_menu_opens_over_the_terminal(page,real_terminal):
    t=real_terminal;errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
    page.goto(t['url']+'/#sessions')
    attach(page,'Real terminal');f=frame(page,t['id']);ready(f)
    f.locator('#terminal-tools-summary').click()
    panel=f.locator('#terminal-tools .action-menu-panel')
    expect(panel).to_be_visible()
    # The toolbar scrolls horizontally, which clips vertically too, so a menu
    # laid out inside it is invisible however "visible" its box looks. Hit-test
    # a real point instead: the terminal must not be painted over the menu.
    hit=panel.evaluate('''(panel)=>{const r=panel.getBoundingClientRect();
        const el=document.elementFromPoint(r.left+r.width/2,r.top+20);
        return {inside:!!el&&panel.contains(el),tag:el?el.tagName+'.'+el.className:null,
                bottom:r.bottom,height:r.height,viewport:innerHeight};}''')
    assert hit['inside'],f'tools menu is covered or clipped; point hit {hit["tag"]}'
    assert hit['height']>100,hit
    assert hit['bottom']<=hit['viewport']+1,f'menu runs past the viewport: {hit}'
    # Painted is not the same as usable, so drive one entry through to its effect.
    f.get_by_role('button',name='Find in terminal').click()
    expect(f.locator('#search-input')).to_be_visible()
    assert not errors,errors


def test_tools_menu_is_placed_during_activation(page, real_terminal):
    t = real_terminal
    page.goto(t['url'] + '/#sessions')
    attach(page, 'Real terminal'); f = frame(page, t['id']); ready(f)
    # Inspect the same activation turn: native <details> opens immediately,
    # before its deferred toggle event / React effect can position the panel.
    hit = f.locator('#terminal-tools-summary').evaluate('''summary => {
        summary.click();
        const panel = summary.parentElement.querySelector('.action-menu-panel');
        const r = panel.getBoundingClientRect();
        const top = document.elementFromPoint(r.left + r.width / 2, r.top + 20);
        return {inside: !!top && panel.contains(top), tag: top?.className};
    }''')
    assert hit['inside'], f'Tools must be usable on its first frame: {hit}'


def shown_panes(page):
    return page.locator('#terminal-workspace .terminal-tabpanel:not([hidden])')


def test_split_view_runs_two_agents_side_by_side(page,real_terminal):
    t=real_terminal;errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
    second=second_session(t)
    page.goto(t['url']+'/#sessions')
    attach(page,'Real terminal');one=frame(page,t['id']);ready(one)
    attach(page,'Second terminal');two=frame(page,second['id']);ready(two)
    split=page.get_by_role('button',name='◫ Split')
    expect(split).to_be_enabled()
    split.click()
    expect(shown_panes(page)).to_have_count(2)
    # The visible pane stays where it was; the most recent other tab joins it.
    left=page.locator('.terminal-tabpanel:has(iframe[src="/terminal/session/%d?embed=1"])'%second['id'])
    right=page.locator('.terminal-tabpanel:has(iframe[src="/terminal/session/%d?embed=1"])'%t['id'])
    a=left.bounding_box();b=right.bounding_box()
    assert a['width']>200 and b['width']>200,(a,b)
    assert abs(a['x']+a['width']-b['x'])<=2,(a,b)
    assert abs(a['y']-b['y'])<=1 and abs(a['height']-b['height'])<=1,(a,b)
    # Both panes stay live: each keystroke has to land in its own tmux session.
    two.locator('#agent-terminal').click()
    page.keyboard.type('echo RIGHT-PANE-PROOF');page.keyboard.press('Enter')
    expect(two.locator('#agent-terminal .xterm-screen')).to_contain_text('RIGHT-PANE-PROOF')
    one.locator('#agent-terminal').click()
    page.keyboard.type('echo LEFT-PANE-PROOF');page.keyboard.press('Enter')
    expect(one.locator('#agent-terminal .xterm-screen')).to_contain_text('LEFT-PANE-PROOF')
    assert 'LEFT-PANE-PROOF' in capture(t) and 'RIGHT-PANE-PROOF' not in capture(t)
    assert 'RIGHT-PANE-PROOF' in capture(t,'second-terminal')
    # Typing into a pane makes it the focused one.
    expect(right).to_have_attribute('data-focused','true')
    expect(left).to_have_attribute('data-focused','false')
    # Choosing a tab that is already on screen focuses its pane, never shows
    # the same session twice.
    page.get_by_role('tab',name='Second terminal',exact=True).click()
    expect(left).to_have_attribute('data-focused','true')
    expect(shown_panes(page)).to_have_count(2)
    page.get_by_role('button',name='◫ Unsplit').click()
    expect(shown_panes(page)).to_have_count(1)
    expect(page.locator('.terminal-panels.split')).to_have_count(0)
    expect(two.locator('#agent-terminal .xterm-screen')).to_contain_text('RIGHT-PANE-PROOF')
    assert not errors,errors


def test_split_button_needs_a_second_terminal_and_stays_off_when_narrow(page,real_terminal):
    t=real_terminal
    page.goto(t['url']+'/#sessions')
    attach(page,'Real terminal');ready(frame(page,t['id']))
    expect(page.get_by_role('button',name='◫ Split')).to_be_disabled()
    second=second_session(t)
    attach(page,'Second terminal');ready(frame(page,second['id']))
    page.get_by_role('button',name='◫ Split').click()
    expect(shown_panes(page)).to_have_count(2)
    # A phone has no room for two panes, so the layout shows one at a time and
    # keeps the split for when there is width again.
    page.set_viewport_size({'width':390,'height':844})
    expect(shown_panes(page)).to_have_count(1)
    expect(page.locator('.ws-group-head')).to_have_count(0)
    page.set_viewport_size({'width':1440,'height':900})
    expect(shown_panes(page)).to_have_count(2)
