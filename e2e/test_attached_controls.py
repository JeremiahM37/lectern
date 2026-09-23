"""Controls consume keys locally; the attached process and its input survive."""
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
        d.send('\x1b');d.wait('AGENT_READY')
        d.send(' continued');d.send('\x1d\x1d')
        wait_bytes(output,b'unfinished draft continued\x1d')
        d.resize(90,27);d.wait('Ctrl+]')
        assert subprocess.check_output(['tmux','display-message','-p','-t','=terminal-test:','#{pane_pid}'],env=t['env'])==pid
        d.send('\x02d');d.wait('Detached. Session keeps running.')
        d.quit()
    finally:d.close()


def test_native_controls_upload_to_ssh_target(remote_terminal):
    t=remote_terminal
    source=t['root']/"context 'sample.txt"
    source.write_bytes(b'Laptop context via controls popup\x00\xff\n')
    d=Dashboard(t,args=('attach','session',str(t['id'])))
    try:
        d.wait('Ctrl+]')
        d.send('\x1du');d.wait('Local file path')
        d.send(str(source)+'\x13');d.wait('Uploaded:',timeout=20)
        files=list((t['remote_root']/'.lectern/context').rglob('*sample.txt'))
        assert len(files)==1
        assert files[0].read_bytes()==source.read_bytes()
        assert not (t['root']/'.lectern/context').exists()
        d.send('\x1b');d.wait('Ctrl+]')
        d.send("printf 'POPUP-%s-RETURN\\n' SSH\r")
        d.wait('POPUP-SSH-RETURN')
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
