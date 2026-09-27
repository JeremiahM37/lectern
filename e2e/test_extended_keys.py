"""Extended keys end to end: real key presses in the browser terminal reach a
program as the kitty keyboard protocol or modifyOtherKeys asks, through the
real tmux + ttyd attachment and straight from xterm.js without tmux."""
import json
import re
import subprocess
import time

import pytest
from playwright.sync_api import expect
from conftest import PHONE
from test_terminal_workspace import real_terminal, open_terminal  # noqa: F401

# Prints each read as hex, one line per read, after asking for a key mode.
KEYECHO = r'''
import os, sys, termios, tty, select
mode, arg, log = sys.argv[1], sys.argv[2], sys.argv[3]
fd = sys.stdin.fileno(); old = termios.tcgetattr(fd); tty.setraw(fd)
out = sys.stdout
if mode == "kitty": out.write("\x1b[>%su" % arg)
elif mode == "mok": out.write("\x1b[>4;%sm" % arg)
out.write("KEYECHO-READY\r\n"); out.flush()
try:
    while True:
        select.select([fd], [], [])
        data = os.read(fd, 1024)
        with open(log, "a") as f: f.write(data.hex() + "\n")
        out.write("got %d\r\n" % len(data)); out.flush()
        if data in (b"q", b"\x1b[113u"): break
finally:
    if mode == "kitty": out.write("\x1b[<u")
    elif mode == "mok": out.write("\x1b[>4m")
    out.flush(); termios.tcsetattr(fd, termios.TCSADRAIN, old)
'''


def start_keyecho(page, t, mode, arg):
    script = t['root'] / 'keyecho.py'
    script.write_text(KEYECHO)
    log = t['root'] / f'keys-{mode}-{arg}.log'
    subprocess.run(['tmux', 'send-keys', '-t', '=terminal-test:', '-l',
                    f'clear; python3 {script} {mode} {arg} {log}'], env=t['env'], check=True)
    subprocess.run(['tmux', 'send-keys', '-t', '=terminal-test:', 'Enter'], env=t['env'], check=True)
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('KEYECHO-READY', timeout=15000)
    return log


def reads(log, count, timeout=10):
    """The first `count` reads the program made, as bytes."""
    end = time.time() + timeout
    while time.time() < end:
        lines = log.read_text().split() if log.exists() else []
        if len(lines) >= count:
            return [bytes.fromhex(line) for line in lines[:count]]
        time.sleep(.05)
    raise AssertionError(f'expected {count} reads, got {log.read_text() if log.exists() else "none"}')


def tmux_version(t):
    return subprocess.check_output(['tmux', '-V'], env=t['env']).decode().strip()


def extended_tmux(t):
    """Whether Lectern turns extended keys on for this tmux: 3.5 and later.
    3.2-3.4 drop Shift+Enter meant for a shell (internal/tmuxkeys)."""
    match = re.match(r'tmux (\d+)\.(\d+)', tmux_version(t))
    return not match or (int(match[1]), int(match[2])) >= (3, 5)


# Ctrl+Shift+A is Lectern's own Select all (remappable), so D stands in.
CHORDS = ['Shift+Enter', 'Enter', 'Control+Enter', 'Control+Shift+KeyD', 'Alt+Enter', 'Control+KeyI', 'Tab']


def test_through_tmux_a_program_asking_for_extended_keys_gets_them(page, real_terminal):
    t = real_terminal
    open_terminal(page, t)
    options = subprocess.check_output(['tmux', 'show-options', '-s'], env=t['env']).decode()
    extended = extended_tmux(t)
    if extended:
        # The browser attachment declared extkeys and turned extended keys on.
        assert 'extended-keys on' in options and 'extended-keys-format csi-u' in options, options
    else:
        # An older tmux is left exactly as it was, and so are the keys.
        assert 'extended-keys on' not in options, (tmux_version(t), options)
    log = start_keyecho(page, t, 'mok', '2')
    # Legacy has no Ctrl+Shift+D at all; the browser keeps it.
    chords = CHORDS if extended else [c for c in CHORDS if c != 'Control+Shift+KeyD']
    for chord in chords:
        page.keyboard.press(chord)
        time.sleep(.05)
    got = reads(log, len(chords))
    if extended:
        assert got == [b'\x1b[13;2u', b'\r', b'\x1b[13;5u', b'\x1b[68;6u', b'\x1b[13;3u', b'\x1b[105;5u', b'\t'], got
    else:
        assert got == [b'\r', b'\r', b'\r', b'\x1b\r', b'\t', b'\t'], (tmux_version(t), got)
    page.keyboard.press('q')


def test_through_tmux_a_program_that_asks_for_nothing_gets_legacy_keys(page, real_terminal):
    t = real_terminal
    open_terminal(page, t)
    log = start_keyecho(page, t, 'none', '0')
    for chord in ['Shift+Enter', 'Control+Enter', 'Control+KeyC', 'Alt+KeyA', 'Control+KeyI']:
        page.keyboard.press(chord)
        time.sleep(.05)
    got = reads(log, 5)
    # Ctrl+C is still the interrupt a shell expects.
    assert got == [b'\r', b'\r', b'\x03', b'\x1ba', b'\t'], got
    page.keyboard.press('q')


RECORDER = '''(() => {
  if (window.__keys) return;
  window.__keys = []; window.__sockets = [];
  const Native = window.WebSocket;
  window.WebSocket = class extends Native {
    constructor(...args) { super(...args); window.__sockets.push(this); }
    send(data) {
      if (data instanceof Uint8Array && data[0] === 48)
        window.__keys.push(new TextDecoder().decode(data.subarray(1)));
      return super.send(data);
    }
  };
})()'''


def program_says(page, text):
    """Feeds `text` to the terminal as if a program wrote it, with no tmux in between."""
    page.evaluate('''(text) => {
      const ws = window.__sockets[window.__sockets.length - 1];
      const bytes = new TextEncoder().encode("0" + text);
      ws.onmessage({ data: bytes.buffer });
    }''', text)


def sent(page):
    return page.evaluate('() => window.__keys.splice(0)')


def test_without_tmux_xterm_speaks_the_kitty_protocol_itself(page, real_terminal):
    t = real_terminal
    page.add_init_script(RECORDER)
    open_terminal(page, t)
    # A program talking to the terminal directly: a stand-alone tmux-less
    # stream, fed to the same xterm.js the browser shows.
    subprocess.run(['tmux', 'send-keys', '-t', '=terminal-test:', '-l', 'cat >/dev/null'], env=t['env'], check=True)
    subprocess.run(['tmux', 'send-keys', '-t', '=terminal-test:', 'Enter'], env=t['env'], check=True)
    time.sleep(.3)
    # tmux runs on the alternate screen and asked for modifyOtherKeys when it
    # attached. A program of our own starts from the main screen and nothing.
    program_says(page, '\x1b[?1049l\x1b[>4m')
    sent(page)

    def press(*chords):
        for chord in chords:
            page.keyboard.press(chord)
        page.wait_for_timeout(100)
        return [k for k in sent(page) if k]

    program_says(page, '\x1b[?u')
    page.wait_for_timeout(100)
    assert sent(page) == ['\x1b[?0u'], 'the query is answered'
    program_says(page, '\x1b[>1u\x1b[?u')
    page.wait_for_timeout(100)
    assert sent(page) == ['\x1b[?1u']
    assert press('Shift+Enter', 'Enter', 'Escape', 'Control+KeyI', 'Tab', 'Control+Shift+KeyD', 'Alt+KeyX') == [
        '\x1b[13;2u', '\r', '\x1b[27u', '\x1b[105;5u', '\t', '\x1b[100;6u', '\x1b[120;3u']
    # Plain text is still typed, and a key release is reported only on request.
    assert press('KeyA', 'Shift+KeyB') == ['a', 'B']
    program_says(page, '\x1b[=3;1u')
    page.keyboard.down('Control'); page.keyboard.down('KeyA'); page.keyboard.up('KeyA'); page.keyboard.up('Control')
    page.wait_for_timeout(100)
    assert [k for k in sent(page) if k] == ['\x1b[97;5u', '\x1b[97;5:3u']
    # The alternate screen has its own stack.
    program_says(page, '\x1b[?1049h\x1b[?u')
    page.wait_for_timeout(100)
    assert sent(page) == ['\x1b[?0u']
    # Back on the main screen its flags are as they were; one pop undoes the push.
    program_says(page, '\x1b[?1049l\x1b[?u')
    page.wait_for_timeout(100)
    assert sent(page) == ['\x1b[?3u']
    program_says(page, '\x1b[<u')
    assert press('Shift+Enter', 'Escape') == ['\r', '\x1b']
    # modifyOtherKeys, asked for and answered.
    program_says(page, '\x1b[>4;2m\x1b[?4m')
    page.wait_for_timeout(100)
    assert sent(page) == ['\x1b[>4;2m']
    assert press('Shift+Enter', 'Control+KeyC') == ['\x1b[27;2;13~', '\x1b[27;5;99~']
    program_says(page, '\x1b[>4m')
    assert press('Shift+Enter') == ['\r']


def test_the_setting_turns_extended_keys_off(page, real_terminal):
    t = real_terminal
    page.add_init_script(RECORDER)
    page.goto(t['url'] + '/#targets')
    page.locator('[data-settings="workspace"]').click()
    toggle = page.locator('[data-setting="workspace.extendedKeys"] input')
    expect(toggle).to_be_checked(timeout=15000)
    expect(page.locator('[data-setting="workspace.extendedKeys"]')).to_contain_text('Extended keyboard (kitty protocol)')
    toggle.uncheck()
    expect(toggle).not_to_be_checked()
    open_terminal(page, t)
    subprocess.run(['tmux', 'send-keys', '-t', '=terminal-test:', '-l', 'cat >/dev/null'], env=t['env'], check=True)
    subprocess.run(['tmux', 'send-keys', '-t', '=terminal-test:', 'Enter'], env=t['env'], check=True)
    time.sleep(.3)
    sent(page)
    program_says(page, '\x1b[>1u\x1b[?u\x1b[>4;2m')
    page.keyboard.press('Shift+Enter'); page.keyboard.press('Control+Enter')
    page.wait_for_timeout(150)
    # No answer to the query, and the keys are exactly what they always were.
    assert [k for k in sent(page) if k] == ['\r', '\r']


@pytest.mark.parametrize('page', [PHONE], indirect=True)
def test_phone_key_bar_and_sticky_modifiers_send_extended_keys(page, real_terminal):
    t = real_terminal
    page.goto(t['url'] + '/#sessions')
    page.locator('.scard', has_text='Real terminal').get_by_role('button', name='⌨ Attach', exact=True).click()
    f = page.frame_locator(f'iframe[src="/terminal/session/{t["id"]}?embed=1"]')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('$', timeout=20000)
    script = t['root'] / 'keyecho.py'; script.write_text(KEYECHO)
    log = t['root'] / 'keys-phone.log'
    subprocess.run(['tmux', 'send-keys', '-t', '=terminal-test:', '-l', f'clear; python3 {script} mok 2 {log}'], env=t['env'], check=True)
    subprocess.run(['tmux', 'send-keys', '-t', '=terminal-test:', 'Enter'], env=t['env'], check=True)
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('KEYECHO-READY', timeout=15000)
    f.locator('#agent-terminal').click()
    ctrl = f.locator('[data-terminal-key="ctrl"]')
    # Sticky Ctrl, then the phone's own keyboard: Ctrl+A.
    ctrl.click(); expect(ctrl).to_have_attribute('aria-pressed', 'true')
    page.keyboard.type('a')
    expect(ctrl).to_have_attribute('aria-pressed', 'false')
    # Sticky Ctrl, then the key bar's Tab: Ctrl+Tab, not Tab.
    ctrl.click(); f.locator('[data-terminal-key="tab"]').click()
    expect(ctrl).to_have_attribute('aria-pressed', 'false')
    # The ^C key is still an interrupt a program can tell apart.
    f.locator('[data-terminal-key="interrupt"]').click()
    got = reads(log, 3)
    if extended_tmux(t):
        assert got == [b'\x1b[97;5u', b'\x1b[9;5u', b'\x1b[99;5u'], got
    else:
        # No extended keys through an older tmux: the legacy bytes, as before.
        assert got == [b'\x01', b'\t', b'\x03'], (tmux_version(t), got)
    page.keyboard.type('q')


@pytest.mark.skipif(not __import__('shutil').which('claude'), reason='explicit Claude CLI audit only')
def test_claude_code_takes_shift_enter_as_a_newline(page, real_terminal):
    t = real_terminal
    home = t['root'] / 'claude-audit'; home.mkdir()
    (home / '.claude.json').write_text(json.dumps({'hasCompletedOnboarding': True, 'theme': 'dark', 'numStartups': 1,
        'customApiKeyResponses': {'approved': ['lectern-keyboard-audit'], 'rejected': []}}))
    cmd = (f'env ANTHROPIC_API_KEY=lectern-keyboard-audit ANTHROPIC_BASE_URL=http://127.0.0.1:1 CLAUDE_CONFIG_DIR={home} '
           'DISABLE_AUTOUPDATER=1 CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1 claude --setting-sources ""')
    if not extended_tmux(t):
        pytest.skip(f'{tmux_version(t)}: extended keys need tmux 3.5')
    # Attach first: tmux drops a program's request for extended keys made
    # before they were turned on, and this session was not made by Lectern.
    open_terminal(page, t)
    subprocess.run(['tmux', 'send-keys', '-t', '=terminal-test:', '-l', cmd], env=t['env'], check=True)
    subprocess.run(['tmux', 'send-keys', '-t', '=terminal-test:', 'Enter'], env=t['env'], check=True)
    screen = page.locator('#agent-terminal .xterm-screen')
    expect(screen).to_contain_text('trust', timeout=30000)
    # Claude redraws its first dialog while it probes the terminal; a key
    # pressed in that moment is lost. Move, see the move, then confirm.
    page.wait_for_timeout(1500)
    page.keyboard.press('ArrowDown')
    expect(screen).to_contain_text('❯ Yes, I trust', timeout=10000)
    page.keyboard.press('Enter')
    expect(screen).to_contain_text('custom API key', timeout=15000)
    page.keyboard.press('ArrowUp')
    expect(screen).to_contain_text('❯ Yes', timeout=10000)
    page.keyboard.press('Enter')
    expect(screen).to_contain_text('Claude Code v', timeout=30000)
    page.wait_for_timeout(1500)
    # The agent asked tmux for extended keys, and tmux honoured it.
    mode = subprocess.check_output(['tmux', 'display', '-p', '-t', '=terminal-test:', '#{pane_key_mode}'], env=t['env']).decode()
    assert mode.strip() == 'Ext 2', mode
    page.keyboard.type('FIRST-LINE')
    page.keyboard.press('Shift+Enter')
    page.keyboard.type('SECOND-LINE')
    # Both lines are in the prompt, on separate rows, and nothing was sent.
    rows = page.locator('#agent-terminal .xterm-rows > div')
    expect(rows.filter(has_text='SECOND-LINE')).to_have_count(1, timeout=10000)
    first = rows.filter(has_text='FIRST-LINE')
    expect(first).to_have_count(1)
    assert 'SECOND-LINE' not in first.inner_text()
    capture = subprocess.check_output(['tmux', 'capture-pane', '-p', '-t', '=terminal-test:'], env=t['env']).decode()
    assert re.search(r'FIRST-LINE\s*\n.*SECOND-LINE', capture), capture
    assert 'API Error' not in capture and 'Connection error' not in capture, 'Shift+Enter submitted the prompt'
