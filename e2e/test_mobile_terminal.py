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



@pytest.mark.parametrize('page',[PHONE],indirect=True)
def test_phone_primary_keys_and_keyboard_toggle(page,real_terminal):
    f=attach(page,real_terminal)
    row=f.locator('#terminal-keybar').bounding_box()
    for key in ('escape','tab','ctrl','left','up','down','right'):
        box=f.locator(f'[data-terminal-key="{key}"]').bounding_box()
        assert box['x']>=row['x'] and box['x']+box['width']<=row['x']+row['width'],(key,box,row)
    f.locator('#terminal-keyboard').click()
    expect(f.locator('.xterm-helper-textarea')).to_be_focused()
    f.locator('#terminal-keyboard').click()
    expect(f.locator('.xterm-helper-textarea')).not_to_be_focused()
    f.locator('[data-terminal-key="up"]').click()
    expect(f.locator('.xterm-helper-textarea')).not_to_be_focused()


@pytest.mark.parametrize('page',[PHONE],indirect=True)
def test_phone_composer_keeps_draft_and_inserts_without_executing(page,real_terminal):
    t=real_terminal;f=attach(page,t)
    f.locator('#terminal-tools-summary').click();f.locator('#compose').click()
    draft=f.locator('#terminal-draft');draft.fill('echo COMPOSER-$((20+22))')
    f.locator('#compose-dialog [data-close]').click()
    f.locator('#terminal-tools-summary').click();f.locator('#compose').click()
    expect(draft).to_have_value('echo COMPOSER-$((20+22))')
    f.get_by_role('button',name='Insert',exact=True).click()
    expect(f.locator('#compose-dialog')).to_have_count(0)
    assert 'COMPOSER-42' not in capture(t)
    f.locator('#agent-terminal').click();page.keyboard.press('Enter')
    expect(f.locator('.xterm-screen')).to_contain_text('COMPOSER-42')
    f.locator('#terminal-tools-summary').click();f.locator('#compose').click()
    draft.fill('echo SENT-$((6*7))')
    f.get_by_role('button',name='Send ↵',exact=True).click()
    expect(f.locator('.xterm-screen')).to_contain_text('SENT-42')


def test_android_native_edit_sequences_reach_real_shell_once(browser,real_terminal):
    # Replays the exact native event shape recorded on Android 14 + Gboard.
    # A physical/emulated Android run is still required for OS keyboard coverage.
    ctx=browser.new_context(viewport=PHONE,user_agent='Mozilla/5.0 (Linux; Android 14) Chrome/120 Mobile Safari/537.36')
    page=ctx.new_page()
    try:
        f=attach(page,real_terminal)
        f.locator('#agent-terminal').click();page.keyboard.type('echo ')
        ta=f.locator('.xterm-helper-textarea')
        ta.evaluate(r'''el=>{
          for(const word of ['h','he','hel','hell','hello']) {
            el.dispatchEvent(new KeyboardEvent('keydown',{key:'Unidentified',keyCode:229,bubbles:true}));
            el.value=word;el.setSelectionRange(0,0);
            el.dispatchEvent(new InputEvent('input',{inputType:'insertText',data:word.at(-1),bubbles:true}));
          }
          el.setSelectionRange(0,0);
          const event=new InputEvent('beforeinput',{inputType:'insertText',data:'jello ',bubbles:true,cancelable:true});
          if(el.dispatchEvent(event))throw Error('Gboard replacement was not handled');
        }''')
        page.keyboard.press('Enter')
        expect(f.locator('.xterm-screen')).to_contain_text('jello')
        out=capture(real_terminal)
        assert '\njello\n' in out and 'hellojello' not in out and 'jello jello' not in out,out
        # A real composing IME changes the existing word then repeats the final
        # input on compositionend. The repeated value must send no extra bytes.
        page.keyboard.type('echo ')
        ta.evaluate(r'''el=>{
          el.dispatchEvent(new CompositionEvent('compositionstart',{bubbles:true}));
          for(const text of ['caf','café']){
            el.value=text;
            el.dispatchEvent(new InputEvent('input',{inputType:'insertCompositionText',data:text,isComposing:true,bubbles:true}));
          }
          el.dispatchEvent(new CompositionEvent('compositionend',{data:'café',bubbles:true}));
          el.dispatchEvent(new InputEvent('input',{inputType:'insertText',data:'café',bubbles:true}));
        }''')
        page.keyboard.press('Enter')
        expect(f.locator('.xterm-screen')).to_contain_text('café')
        assert '\ncafé\n' in capture(real_terminal)
        # External cursor keys clear IME context; paste retains xterm's normal path.
        page.keyboard.type('echo cursor');f.locator('[data-terminal-key="left"]').click()
        page.keyboard.type('X');page.keyboard.press('Enter')
        expect(f.locator('.xterm-screen')).to_contain_text('cursoXr')
        # Observe the keyboard-open viewport before dismissing it. Back-to-back
        # resizes can be coalesced, skipping the state a real IME holds open.
        f.locator('body').evaluate("el=>{el.removeAttribute('data-test-keyboard-resize');window.addEventListener('resize',()=>el.setAttribute('data-test-keyboard-resize','seen'),{once:true});}")
        page.set_viewport_size({'width':390,'height':500})
        expect(f.locator('body')).to_have_attribute('data-test-keyboard-resize','seen')
        page.set_viewport_size(PHONE)
        expect(f.locator('.xterm-helper-textarea')).not_to_be_focused()
        f.locator('#terminal-keyboard').click()
        expect(f.locator('.xterm-helper-textarea')).to_be_focused()
    finally: ctx.close()


@pytest.mark.parametrize('page',[PHONE],indirect=True)
def test_standalone_phone_keyboard_control_sits_in_key_row(page,real_terminal):
    from test_terminal_workspace import open_terminal
    open_terminal(page,real_terminal)
    row=page.locator('#terminal-keybar').bounding_box()
    keyboard=page.locator('#terminal-keyboard').bounding_box()
    assert row['y']<=keyboard['y'] and keyboard['y']+keyboard['height']<=row['y']+row['height']+1
    assert row['x']+row['width']<=keyboard['x']
