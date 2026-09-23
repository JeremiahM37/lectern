"""Explicit local Claude CLI input audit; requires optional isolated binary mount."""
import json, re, shutil, subprocess
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_mobile_terminal_experience import FAKE_KEYBOARD, RESTORE_VIEWPORT

@pytest.mark.skipif(not shutil.which('claude'), reason='explicit Claude CLI audit only')
@pytest.mark.parametrize("keyboard_mode", ["visual", "overlay"])
def test_claude_keyboard_probe(page, real_terminal, keyboard_mode):
    t=real_terminal
    home=t['root']/'claude-audit';home.mkdir()
    (home/'.claude.json').write_text(json.dumps({'hasCompletedOnboarding':True,'theme':'dark','numStartups':1,'customApiKeyResponses':{'approved':['lectern-keyboard-audit'],'rejected':[]}}))
    cmd=f'env ANTHROPIC_API_KEY=lectern-keyboard-audit ANTHROPIC_BASE_URL=http://127.0.0.1:1 CLAUDE_CONFIG_DIR={home} DISABLE_AUTOUPDATER=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 claude --setting-sources ""'
    subprocess.run(['tmux','send-keys','-t','=terminal-test:','-l',cmd],env=t['env'],check=True)
    subprocess.run(['tmux','send-keys','-t','=terminal-test:','Enter'],env=t['env'],check=True)
    page.set_viewport_size({'width':390,'height':844})
    page.goto(t['url']+f'/#terminals/session/{t["id"]}')
    frame=page.frame_locator('iframe')
    expect(frame.locator('#connection')).to_have_text('Connected',timeout=20000)
    expect(frame.locator('.xterm-screen')).to_contain_text('trust', timeout=20000)
    frame.locator('#agent-terminal').click()
    page.keyboard.press('ArrowDown')
    page.keyboard.press('Enter')
    expect(frame.locator('.xterm-screen')).to_contain_text('custom API key', timeout=10000)
    page.keyboard.press('ArrowUp');page.keyboard.press('Enter')
    expect(frame.locator('.xterm-screen')).to_contain_text('? for shortcuts', timeout=20000)
    page.keyboard.type('Unsent keyboard visibility probe ' * 20)
    if keyboard_mode == 'visual':
        page.evaluate(FAKE_KEYBOARD,400)
    else:
        page.evaluate('''() => {
          const keyboard = new EventTarget();
          keyboard.overlaysContent = true;
          keyboard.boundingRect = {top:400,left:0,width:innerWidth,height:innerHeight-400};
          Object.defineProperty(navigator, 'virtualKeyboard', {value:keyboard,configurable:true});
          // Resize invokes the normal handler; geometrychange has separate
          // coverage in test_mobile_terminal_experience.
          dispatchEvent(new Event('resize'));
        }''')
    expect(frame.locator('body')).to_have_class(re.compile('fitted-viewport'))
    page.keyboard.type(' END-OF-DRAFT')
    row=frame.locator('.xterm-rows > div').filter(has_text='END-OF-DRAFT')
    expect(row).to_be_visible()
    cursor=row.bounding_box()
    keys=frame.locator('#terminal-keybar').bounding_box()
    assert cursor and keys and cursor['y']+cursor['height']<=keys['y']+1
    assert keys['y']+keys['height']<=401

    if keyboard_mode == 'visual':
        page.evaluate(RESTORE_VIEWPORT)
