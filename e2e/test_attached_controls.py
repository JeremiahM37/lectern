"""Controls consume keys locally; the attached process and its input survive."""
import hashlib
import shlex
import subprocess
import signal
import termios
import time

import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal, open_terminal
from test_terminal_dashboard import Dashboard
from test_remote_acceptance import remote_terminal, _ssh


def record_input(t):
    script = t['root'] / 'record.py'
    script.write_text("import os,tty\nfrom pathlib import Path\ntty.setraw(0)\nprint('AGENT_READY',flush=True)\nwith open('agent-input','ab',buffering=0) as f:\n while True:\n  b=os.read(0,1024)\n  if not b: break\n  f.write(b)\n")
    subprocess.run(['tmux','send-keys','-t','=terminal-test:',
                    'python3 '+shlex.quote(str(script)), 'Enter'],env=t['env'],check=True)
    output=t['root']/'agent-input'
    deadline=time.monotonic()+5
    while not output.exists() and time.monotonic()<deadline:time.sleep(.05)
    assert output.exists()
    return output


def wait_bytes(path, expected):
    deadline=time.monotonic()+5
    while time.monotonic()<deadline:
        if path.read_bytes()==expected:return
        time.sleep(.05)
    assert path.read_bytes()==expected


def test_browser_controls_hint_and_input_isolation(page,real_terminal):
    t=real_terminal
    open_terminal(page,t)
    output=record_input(t)
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('AGENT_READY')
    expect(page.locator('#terminal-tools-summary')).to_contain_text('Ctrl+] then m')
    page.keyboard.type('unfinished draft')
    page.keyboard.press('Control+]');page.keyboard.press('m')
    expect(page.locator('#terminal-tools')).to_have_attribute('open','')
    expect(page.locator('.terminal-controls-help')).to_be_visible()
    page.keyboard.type('menukeys')
    wait_bytes(output,b'unfinished draft')
    page.keyboard.press('Escape')
    expect(page.locator('#terminal-tools')).not_to_have_attribute('open','')
    page.keyboard.type(' continued')
    page.keyboard.press('Control+]');page.keyboard.press('Control+]')
    wait_bytes(output,b'unfinished draft continued\x1d')
    page.set_viewport_size({'width':390,'height':844})
    expect(page.locator('.terminal-controls-hint')).not_to_be_visible()
    page.locator('#terminal-tools-summary').click()
    expect(page.locator('.terminal-controls-help')).not_to_be_visible()
    assert page.locator('#terminal-tools-summary').bounding_box()['width'] < 100


@pytest.mark.parametrize('outer_tmux',[False,True])
def test_native_attached_controls_preserve_agent_and_draft(real_terminal,outer_tmux):
    t=real_terminal;output=record_input(t)
    pid=subprocess.check_output(['tmux','display-message','-p','-t','=terminal-test:','#{pane_pid}'],env=t['env'])
    d=Dashboard(t,outer_tmux=outer_tmux)
    try:
        d.wait('Real terminal');d.send('\r');d.wait('AGENT_READY')
        d.wait('Ctrl+]')
        d.send('unfinished draft');wait_bytes(output,b'unfinished draft')
        d.send('\x1dm');d.wait('Rename')
        # AGENT_READY never left the screen, so it cannot show the popup has
        # closed; typing before it has sends the next keys to the popup.
        d.send('\x1b');d.wait_gone('Rename');d.wait('AGENT_READY')
        d.send(' continued');d.send('\x1d\x1d')
        wait_bytes(output,b'unfinished draft continued\x1d')
        d.resize(90,27);d.wait('Ctrl+]')
        assert subprocess.check_output(['tmux','display-message','-p','-t','=terminal-test:','#{pane_pid}'],env=t['env'])==pid
        d.send('\x02d');d.wait('Detached. Session keeps running.')
        d.quit()
    finally:d.close()


@pytest.mark.parametrize('entry', ['shortcut', 'menu'])
def test_native_controls_upload_to_ssh_target(remote_terminal, tmp_path, entry):
    """A native client wrapping SSH uploads from the laptop's own path, then
    inserts the returned remote path without submitting it. The upload runs
    both from the Ctrl-] u shortcut and from the Ctrl-] m controls menu, which
    must insert identically."""
    t=remote_terminal
    # The local file lives outside the control-plane repository, as a laptop
    # file would, and its name needs quoting when it reaches a shell.
    laptop=tmp_path/'laptop-home';laptop.mkdir(mode=0o700)
    source=laptop/'laptop notes.txt'
    payload=b'Laptop context via controls popup\x00\xff\n'
    source.write_bytes(payload)
    digest=hashlib.sha256(payload).hexdigest()
    # Uploading while the agent sits in a different remote directory proves the
    # inserted path is absolute and stays usable from anywhere.
    remote_cwd=t['remote_root']/'sub dir'
    _ssh(t,'mkdir -p '+shlex.quote(str(remote_cwd)))
    d=Dashboard(t,args=('attach','session',str(t['id'])))
    try:
        d.wait('Ctrl+]')
        d.send('cd '+shlex.quote(str(remote_cwd))+" && printf 'CWD-OK\\n'\r")
        d.wait('CWD-OK')
        # Type a command prefix first: a finished upload must not submit it.
        d.send('sha256sum ')
        if entry=='shortcut':
            d.send('\x1du')
        else:
            d.send('\x1dm');d.wait('Upload context file')
            d.send('j\r')
        d.wait('Local file path')
        d.send(str(source)+'\x13');d.wait('Uploaded:',timeout=20)
        files=list((t['remote_root']/'.lectern/context').rglob('*laptop notes.txt'))
        assert len(files)==1
        assert files[0].read_bytes()==payload
        # The bytes landed on the SSH target, not the control-plane host.
        assert not (t['root']/'.lectern/context').exists()
        d.send('\x1b');d.wait('Ctrl+]')
        # The quoted path was appended to the unsubmitted command line, and the
        # upload did not run anything yet.
        assert "sha256sum '" in d.text
        assert digest not in d.text
        d.send('\r')
        # Only after the operator submits does the remote digest appear, which
        # proves the inserted path names the uploaded file on the target.
        d.wait(digest,timeout=10)
        d.send('\x02d');d.proc.wait(timeout=10)
        assert d.proc.returncode==0
        env="env TMUX='' TMUX_TMPDIR="+shlex.quote(str(t['remote_tmux']))
        _ssh(t,env+' tmux has-session -t ='+shlex.quote(t['remote_session']['tmux_session']))
    finally:d.close()


def test_native_controls_interrupt_restores_tty(real_terminal):
    t=real_terminal
    d=Dashboard(t,args=('attach','session',str(t['id'])))
    try:
        d.wait('Ctrl+]')
        d.proc.send_signal(signal.SIGTERM)
        d.proc.wait(timeout=10)
        assert termios.tcgetattr(d.slave)==d.original
        subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)
    finally:d.close()


def test_hosted_attachment_keeps_controls_hint_visible_in_narrow_terminal(real_terminal):
    """Old clients and direct SSH launchers enter this exact server command."""
    t=real_terminal
    output=record_input(t)
    d=Dashboard(t,args=('--hosted-attach','attach','session',str(t['id'])))
    try:
        d.wait('AGENT_READY')
        d.wait('Ctrl+] m')
        assert 'Ctrl+] m' in d.screen.display[0]
        d.resize(45,24)
        d.wait('Ctrl+] m')
        assert 'Ctrl+] m' in d.screen.display[0]
        d.send('draft before controls')
        d.send('\x1dm');d.wait('Send message')
        d.send('\x1b');d.wait('Ctrl+] m')
        assert 'Ctrl+] m' in d.screen.display[0]
        d.send(' after');d.send('\x1d\x1d')
        wait_bytes(output,b'draft before controls after\x1d')
        d.send('\x02d');d.proc.wait(timeout=10)
        assert d.proc.returncode==0
        assert termios.tcgetattr(d.slave)==d.original
        subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)
    finally:d.close()


@pytest.mark.parametrize("outer_tmux", [False, True])
def test_native_wheel_reads_scrollback_without_changing_agent_draft(real_terminal, outer_tmux):
    """The controls wrapper must request mouse reports, not let an outer
    terminal turn its alternate-screen wheel events into Up/Down keys."""
    t=real_terminal
    d=Dashboard(t, outer_tmux=outer_tmux)
    received=[]
    feed=d.stream.feed
    def record(data):
        received.append(data)
        feed(data)
    d.stream.feed=record
    try:
        d.wait('Real terminal'); d.send('\r'); d.wait('Ctrl+]')
        # Output after attachment must remain readable in the private wrapper.
        d.send("for i in $(seq 1 100); do printf 'WHEEL-HISTORY-%03d\\n' $i; sleep .01; done\r")
        d.wait('WHEEL-HISTORY-100')
        output=record_input(t); d.wait('AGENT_READY')
        d.send('unchanged draft'); wait_bytes(output,b'unchanged draft')
        # Dashboard mouse mode was disabled before attachment. The last mode
        # command for the attached terminal must enable mouse reporting again.
        import re
        modes=re.findall(r'\x1b\[\?(?:1000|1002|1003)([hl])',''.join(received))
        assert modes and modes[-1]=='h', 'Attached terminal has no mouse reporting'
        for _ in range(20): d.send('\x1b[<64;30;10M')
        d.wait('WHEEL-HISTORY-001')
        wait_bytes(output,b'unchanged draft')
        for _ in range(25): d.send('\x1b[<65;30;10M')
        d.wait('AGENT_READY')
        d.send(' continued'); wait_bytes(output,b'unchanged draft continued')
        d.send('\x02d');d.wait('Detached. Session keeps running.');d.quit()
    finally:d.close()
