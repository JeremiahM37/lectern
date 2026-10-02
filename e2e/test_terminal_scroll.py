"""Scrolling has to reach both ordinary scrollback and mouse-driven TUIs."""
import time
import subprocess
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal, open_terminal, type_command, terminal_tool


def test_mouse_application_receives_scroll(page,real_terminal):
    t=real_terminal
    page.add_init_script('''let Constructor;
      Object.defineProperty(window,'Terminal',{configurable:true,
        get(){return Constructor},set(Base){Constructor=class extends Base {
          constructor(...args){super(...args);window.testTerminal=this;}
        };}});''')
    (t['root']/'mouse-app.py').write_text('''import os,tty,sys,re
old=None
tty.setraw(sys.stdin.fileno())
os.write(1,b'\\x1b[?1049h\\x1b[?1000h\\x1b[?1006h\\x1b[2J\\x1b[HMOUSE-APP-READY')
while True:
 data=os.read(0,4096)
 with open('mouse-input.log','ab') as f:f.write(data)
 if b'[<64;' in data:os.write(1,b'\\x1b[2;1HSCROLLED-OLDER-CONTENT')
''')
    open_terminal(page,t);type_command(page,'python3 mouse-app.py')
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('MOUSE-APP-READY')
    box=page.locator('#agent-terminal').bounding_box()
    page.mouse.move(box['x']+box['width']/2,box['y']+box['height']/2)
    page.mouse.wheel(0,-350)
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('SCROLLED-OLDER-CONTENT',timeout=3000)

    # Middle-button autoscroll must use that protocol too, without sending a
    # middle click/paste to the application. Escape stops it without app input.
    x=box['x']+box['width']/2; y=box['y']+box['height']/2
    before=(t['root']/'mouse-input.log').read_bytes().count(b'[<64;')
    page.mouse.click(x,y,button='middle');page.mouse.move(x,y-120)
    page.wait_for_timeout(400)
    assert (t['root']/'mouse-input.log').read_bytes().count(b'[<64;')>before
    page.keyboard.press('Escape')
    expect(page.locator('.terminal-autoscroll-marker')).to_have_count(0)
    page.wait_for_timeout(150)
    stopped=(t['root']/'mouse-input.log').read_bytes()
    page.wait_for_timeout(200)
    assert (t['root']/'mouse-input.log').read_bytes()==stopped
    assert b'[<1;' not in stopped

    # Finger swipes must generate the same negotiated mouse-wheel reports.
    before=(t['root']/'mouse-input.log').read_bytes().count(b'[<64;')
    client=page.context.new_cdp_session(page)
    x=box['x']+box['width']/2; y=box['y']+box['height']/2
    client.send('Input.dispatchTouchEvent',{'type':'touchStart','touchPoints':[{'x':x,'y':y}]})
    for delta in [15,30,45,60]:
        client.send('Input.dispatchTouchEvent',{'type':'touchMove','touchPoints':[{'x':x,'y':y+delta}]})
        page.wait_for_timeout(30)
    client.send('Input.dispatchTouchEvent',{'type':'touchEnd','touchPoints':[]})
    deadline=time.time()+3
    while time.time()<deadline:
        if (t['root']/'mouse-input.log').read_bytes().count(b'[<64;')>before:break
        page.wait_for_timeout(50)
    assert (t['root']/'mouse-input.log').read_bytes().count(b'[<64;')>before
    client.detach()


@pytest.mark.parametrize('real_terminal', [{}, {'no_alternate_screen':True}], indirect=True, ids=['tmux-default','tmux-normal-screen'])
@pytest.mark.parametrize('return_gesture', ['wheel', 'touch'])
def test_scroll_fetches_retained_history_from_before_attach(page,real_terminal,return_gesture):
    t=real_terminal
    subprocess.run(['tmux','send-keys','-t','=terminal-test:',"for i in $(seq 1 180); do echo OLD-LINE-$i; done",'Enter'],env=t['env'],check=True)
    time.sleep(.2)
    open_terminal(page,t)
    box=page.locator('#agent-terminal').bounding_box()
    page.mouse.move(box['x']+box['width']/2,box['y']+box['height']/2)
    page.mouse.wheel(0,-350)
    frozen=page.locator('.frozen')
    expect(frozen).to_be_visible(timeout=10000)
    expect(frozen).to_contain_text('OLD-LINE-1')
    assert frozen.evaluate('(el)=>el.scrollTop<el.scrollHeight-el.clientHeight')
    expect(page.locator('#pause')).to_have_text('Pause view')
    # Returning to the bottom resumes naturally, with no button or blocked input.
    if return_gesture == 'wheel':
        page.mouse.wheel(0,10000)
    else:
        client=page.context.new_cdp_session(page)
        x=box['x']+box['width']/2; y=box['y']+box['height']-50
        client.send('Input.dispatchTouchEvent',{'type':'touchStart','touchPoints':[{'x':x,'y':y}]})
        for delta in range(30,540,30):
            client.send('Input.dispatchTouchEvent',{'type':'touchMove','touchPoints':[{'x':x,'y':max(box['y']+10,y-delta)}]})
            page.wait_for_timeout(20)
        client.send('Input.dispatchTouchEvent',{'type':'touchEnd','touchPoints':[]})
        client.detach()
    expect(frozen).not_to_be_visible()
    page.locator('#agent-terminal').click();type_command(page,'echo BACK-AT-LIVE-PROMPT')
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('BACK-AT-LIVE-PROMPT')
    page.wait_for_timeout(1100)
    page.mouse.wheel(0,-10000)
    page.wait_for_timeout(200)
    page.mouse.wheel(0,-10000)
    expect(frozen).to_be_visible()
    page.keyboard.type('echo TYPING-RETURNS-TO-LIVE')
    page.keyboard.press('Enter')
    expect(frozen).not_to_be_visible()
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('TYPING-RETURNS-TO-LIVE')
    # Only explicitly pressing Pause should require Resume.
    terminal_tool(page,'#pause')
    expect(frozen).to_be_visible()
    page.mouse.move(box['x']+box['width']/2,box['y']+box['height']/2)
    page.mouse.wheel(0,10000)
    expect(page.locator('#pause')).to_have_text('Resume view')
    expect(frozen).to_be_visible()

@pytest.mark.parametrize('cancel',['Escape','click'])
def test_middle_autoscroll_reads_history_and_returns_live(page,real_terminal,cancel):
    t=real_terminal
    subprocess.run(['tmux','send-keys','-t','=terminal-test:',
      "for i in $(seq 1 300); do echo AUTO-HISTORY-$i; done",'Enter'],env=t['env'],check=True)
    time.sleep(.2)
    open_terminal(page,t)
    box=page.locator('#agent-terminal').bounding_box()
    x=box['x']+box['width']/2;y=box['y']+box['height']/2
    page.mouse.click(x,y,button='middle')
    expect(page.locator('.terminal-autoscroll-marker')).to_be_visible()
    page.mouse.move(x,y-120)
    history=page.locator('.frozen')
    expect(history).to_be_visible()
    before=history.evaluate('(el)=>el.scrollTop')
    page.wait_for_timeout(400)
    assert history.evaluate('(el)=>el.scrollTop')<before
    assert history.evaluate('(el)=>getComputedStyle(el).scrollbarWidth')=='thin'
    assert page.locator('.xterm-viewport').evaluate('(el)=>getComputedStyle(el).scrollbarWidth')=='thin'
    # Reversing direction reaches the live view without a Resume button.
    page.mouse.move(x,y+220)
    expect(history).not_to_be_visible(timeout=10000)
    if cancel=='Escape':page.keyboard.press('Escape')
    else:page.mouse.click(x,y,button='middle')
    expect(page.locator('.terminal-autoscroll-marker')).to_have_count(0)
    page.locator('#agent-terminal').click();type_command(page,'echo AUTO-RETURNED-LIVE')
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('AUTO-RETURNED-LIVE')
    page.mouse.click(x,y,button='middle')
    page.evaluate("window.dispatchEvent(new Event('blur'))")
    expect(page.locator('.terminal-autoscroll-marker')).to_have_count(0)

@pytest.mark.parametrize('real_terminal',[{'no_alternate_screen':True}],indirect=True)
def test_middle_autoscroll_moves_normal_terminal_buffer(page,real_terminal):
    open_terminal(page,real_terminal)
    # Pace output so tmux sends scroll operations rather than coalescing the
    # whole burst into one screen redraw with little client-side scrollback.
    type_command(page,'for i in $(seq 1 120); do echo BUFFER-LINE-$i; sleep 0.02; done')
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('BUFFER-LINE-120')
    base=page.locator('#agent-terminal .xterm-viewport').evaluate('(viewport)=>viewport.scrollTop')
    assert base>200
    box=page.locator('#agent-terminal').bounding_box()
    x=box['x']+box['width']/2;y=box['y']+box['height']/2
    page.mouse.click(x,y,button='middle');page.mouse.move(x,y-150)
    page.wait_for_function('(base)=>document.querySelector("#agent-terminal .xterm-viewport").scrollTop<base-100',arg=base)
    page.mouse.move(x,y+200)
    page.wait_for_function('(()=>{const v=document.querySelector("#agent-terminal .xterm-viewport");return v.scrollTop>=v.scrollHeight-v.clientHeight-2})()')
    page.keyboard.press('Escape')
    expect(page.locator('.terminal-autoscroll-marker')).to_have_count(0)
    expect(page.locator('.frozen')).not_to_be_visible()


def test_fullscreen_app_without_mouse_scrolls_with_its_page_keys(page,real_terminal):
    """Claude Code started with its mouse off (CLAUDE_CODE_DISABLE_MOUSE=1) draws
    on the alternate screen and keeps its own transcript, so tmux has no history
    to give. The wheel and drags must reach it as PageUp/PageDown instead of
    ending at "no older output"."""
    t=real_terminal
    (t['root']/'page-app.py').write_text('''import os,tty,sys
tty.setraw(sys.stdin.fileno())
os.write(1,b'\\x1b[?1049h\\x1b[2J\\x1b[HPAGE-APP-READY')
up=down=0
while True:
 data=os.read(0,4096)
 with open('page-input.log','ab') as f:f.write(data)
 up+=data.count(b'\\x1b[5~');down+=data.count(b'\\x1b[6~')
 os.write(1,b'\\x1b[3;1H\\x1b[2KUP=%d DOWN=%d' % (up,down))
''')
    open_terminal(page,t);type_command(page,'python3 page-app.py')
    screen=page.locator('#agent-terminal .xterm-screen')
    expect(screen).to_contain_text('PAGE-APP-READY')
    box=page.locator('#agent-terminal').bounding_box()
    x=box['x']+box['width']/2; y=box['y']+box['height']/2
    page.mouse.move(x,y)
    page.mouse.wheel(0,-350)
    expect(screen).to_contain_text('UP=1',timeout=10000)
    expect(page.locator('.frozen')).not_to_be_visible()
    page.wait_for_timeout(1100)
    page.mouse.wheel(0,-240)
    expect(screen).to_contain_text('UP=3',timeout=3000)
    page.mouse.wheel(0,240)
    expect(screen).to_contain_text('DOWN=2',timeout=3000)
    # A finger drag pages the same way.
    client=page.context.new_cdp_session(page)
    client.send('Input.dispatchTouchEvent',{'type':'touchStart','touchPoints':[{'x':x,'y':y}]})
    for delta in range(20,260,20):
        client.send('Input.dispatchTouchEvent',{'type':'touchMove','touchPoints':[{'x':x,'y':y+delta}]})
        page.wait_for_timeout(20)
    client.send('Input.dispatchTouchEvent',{'type':'touchEnd','touchPoints':[]})
    client.detach()
    deadline=time.time()+3
    while time.time()<deadline and (t['root']/'page-input.log').read_bytes().count(b'\x1b[5~')<=3:
        page.wait_for_timeout(50)
    assert (t['root']/'page-input.log').read_bytes().count(b'\x1b[5~')>3
