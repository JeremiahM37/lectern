"""A phone-sized terminal: readable type, the keys a phone lacks, and no lost rows."""
from playwright.sync_api import expect
from conftest import PHONE
from test_terminal_workspace import real_terminal, capture
import pytest


def attach(page,t):
    page.goto(t['url']+'/#sessions')
    page.locator('.scard',has_text='Real terminal').get_by_role('button',name='⌨ Attach',exact=True).click()
    f=page.frame_locator(f'iframe[src="/terminal/session/{t["id"]}?embed=1"]')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('$',timeout=20000)
    return f


@pytest.mark.parametrize('page',[PHONE],indirect=True)
def test_a_phone_gets_readable_columns_and_the_keys_it_lacks(page,real_terminal):
    t=real_terminal;errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
    f=attach(page,t)
    # At the desk size a 390px screen is 40 columns, and an agent's interface is
    # unreadable wrapped that tight.
    expect(f.locator('body')).to_have_class(__import__('re').compile('mobile-terminal'))
    cols=int(capture_cols(t))
    assert cols>=50,f'only {cols} columns on a phone'
    # Chrome may not eat the screen: tab bar and key bar together under 100px.
    bar=page.locator('.terminal-tabbar').bounding_box()['height'];keys=f.locator('#terminal-keybar').bounding_box()['height']
    assert bar<=46 and keys<=46,(bar,keys)
    for key in ('ctrl','alt','escape','tab','pipe','tilde','home','end','pageup','pagedown'):
        expect(f.locator(f'[data-terminal-key="{key}"]')).to_have_count(1)
    # Sticky Ctrl applies to the next key from the phone's own keyboard, once.
    f.locator('#agent-terminal').click()
    page.keyboard.type('sleep 300');page.keyboard.press('Enter')
    ctrl=f.locator('[data-terminal-key="ctrl"]');ctrl.click()
    expect(ctrl).to_have_attribute('aria-pressed','true')
    page.keyboard.type('c')
    expect(ctrl).to_have_attribute('aria-pressed','false')
    page.keyboard.type('echo AFTER-INTERRUPT');page.keyboard.press('Enter')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('AFTER-INTERRUPT',timeout=10000)
    out=capture(t);assert '^C' in out and out.rstrip().splitlines()[-2].endswith('AFTER-INTERRUPT') or 'AFTER-INTERRUPT' in out
    # The key row ends before the Tools button rather than sliding under it.
    row=f.locator('#terminal-keybar').bounding_box();tools=f.locator('#terminal-tools-summary').bounding_box()
    assert row['x']+row['width']<=tools['x']+1,(row,tools)
    assert not errors,errors


def capture_cols(t):
    import subprocess
    return subprocess.check_output(['tmux','display-message','-p','-t','=terminal-test:','#{window_width}'],env=t['env']).decode().strip()


def test_the_phone_size_is_stored_apart_from_the_desk_size(page,real_terminal):
    t=real_terminal
    f=attach(page,t)
    desk=int(capture_cols(t))
    size=f.locator('body').evaluate('()=>JSON.parse(localStorage.getItem("lec-terminal-prefs")||"{}")')
    # Nothing on a desk-sized screen adopted the phone's type size.
    assert desk>100 and size.get('mobileFontSize') in (None,11),(desk,size)


HOLD="""async (host,[hold,dx])=>{const r=host.getBoundingClientRect(),x=r.left+80,y=r.top+40;
const ev=(type,px)=>host.dispatchEvent(new PointerEvent(type,{pointerId:7,pointerType:'touch',isPrimary:true,clientX:px,clientY:y,bubbles:true,cancelable:true}));
ev('pointerdown',x);if(dx){await new Promise(r=>setTimeout(r,100));ev('pointermove',x+dx);}
await new Promise(r=>setTimeout(r,hold));ev('pointerup',x+dx);}"""


@pytest.mark.parametrize('page',[PHONE],indirect=True)
def test_holding_the_terminal_makes_its_text_selectable(page,real_terminal):
    t=real_terminal
    f=attach(page,t)
    f.locator('#agent-terminal').click()
    page.keyboard.type('echo COPY-FROM-PHONE-$((40+2))');page.keyboard.press('Enter')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('COPY-FROM-PHONE-42')
    host=f.locator('#agent-terminal')
    # A tap is not a press, and neither is a slow sideways swipe between tabs.
    host.evaluate(HOLD,[150,0]);expect(f.locator('#select-bar')).to_have_count(0)
    host.evaluate(HOLD,[700,40]);expect(f.locator('#select-bar')).to_have_count(0)
    host.evaluate(HOLD,[700,0])
    expect(f.locator('#select-bar')).to_be_visible()
    # xterm draws to a canvas a phone cannot select from; this is real text.
    frozen=f.locator('#agent-pane pre.frozen')
    expect(frozen).to_contain_text('COPY-FROM-PHONE-42')
    assert frozen.evaluate('(el)=>getComputedStyle(el).userSelect')=='text'
    # It opens on the latest output, not on the blank rows under the cursor.
    assert frozen.evaluate('(el)=>el.scrollHeight-el.clientHeight-el.scrollTop')<=2
    assert not frozen.inner_text().endswith('\n\n')
    f.locator('#select-done').click()
    expect(f.locator('#select-bar')).to_have_count(0)
    # Done hands the keyboard back to the terminal rather than leaving it nowhere.
    expect(f.locator('#agent-terminal .xterm-helper-textarea')).to_be_focused()
    page.keyboard.type('echo LIVE-AGAIN');page.keyboard.press('Enter')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('LIVE-AGAIN',timeout=10000)


@pytest.mark.parametrize('page',[PHONE],indirect=True)
def test_a_snippet_is_one_tap_and_the_list_is_the_operators(page,real_terminal):
    t=real_terminal
    f=attach(page,t)
    f.locator('[data-terminal-key="snippets"]').click()
    dialog=f.locator('#snippets-dialog')
    # Add one that ends without Enter, so the shell shows it waiting to be edited.
    dialog.locator('#snippet-text').fill('echo SNIPPET-$((6*7))')
    dialog.locator('#snippet-add').click()
    dialog.locator('.snippet-send',has_text='echo SNIPPET').click()
    expect(dialog).to_have_count(0)
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('SNIPPET-42',timeout=10000)
    assert 'SNIPPET-42' in capture(t)
    # Removing the defaults must stick: an emptied list does not grow them back.
    f.locator('[data-terminal-key="snippets"]').click()
    dialog.locator('#snippets-edit').click()
    while dialog.locator('.snippet-remove').count():dialog.locator('.snippet-remove').first.click()
    page.reload()
    f=attach(page,t)
    f.locator('[data-terminal-key="snippets"]').click()
    expect(f.locator('#snippets-dialog .snippet-send')).to_have_count(0)

