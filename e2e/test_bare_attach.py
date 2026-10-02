"""Attaching with no tmux anywhere (docs/terminal-client.md): sessions on the
PTY host, the native client drawing its own key bar. A real server, a real
PTY host, a stub agent that speaks Lectern's hook protocol, and the client in
a real terminal with tmux removed from PATH. Re-audit N1: this used to be a
trap with no key bar, no Ctrl+] menu, a Ctrl+\\ that killed the agent, and a
dead agent's pane that showed its hook token.
"""
import json
import os
import shutil
import signal
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path

import pytest

from conftest import OUTSIDE_WORLD, _binary, _unused_port, CHORD_GAP
from test_terminal_dashboard import Dashboard

# Writes its own hook token where the test can read it, echoes what it is
# told, and asks for approval (the PermissionRequest hook) on "ask".
AGENT = r'''#!/bin/bash
printf '%s' "$LECTERN_HOOK_TOKEN" > "$PWD/agent-token"
echo "$$" > "$PWD/agent-pid"
echo "stub agent ready, see notes.txt"
while IFS= read -r line; do
  if [ "$line" = ask ]; then
    python3 - <<'PYEOF'
import json, os, urllib.request
body = json.dumps({"hook_event_name": "PermissionRequest", "tool_name": "Bash",
                   "tool_input": {"command": "echo hi > NOTES.md"}}).encode()
req = urllib.request.Request(os.environ["LECTERN_HOOK_URL"].rstrip("/") + "/PermissionRequest", data=body,
    headers={"Authorization": "Bearer " + os.environ.get("LECTERN_HOOK_TOKEN", ""), "Content-Type": "application/json"},
    method="POST")
with urllib.request.urlopen(req, timeout=60) as resp:
    data = resp.read()
open(os.path.join(os.getcwd(), "hook-response.json"), "wb").write(data)
print("GOT_ANSWER")
PYEOF
  else
    echo "you said: $line"
  fi
done
'''


@pytest.fixture()
def bare(tmp_path):
    socket_dir = tempfile.TemporaryDirectory(prefix='lbare-', dir='/tmp')
    socket = str(Path(socket_dir.name) / 's.sock')
    port = _unused_port()
    url = f'http://127.0.0.1:{port}'
    root = tmp_path / 'workspace'
    root.mkdir()
    subprocess.run(['git', 'init', '-q', str(root)], check=True)
    (root / 'notes.txt').write_text('NOTES FILE CONTENT\n')
    agent = tmp_path / 'stub-agent'
    agent.write_text(AGENT)
    agent.chmod(0o755)
    # A PATH with every program on it but tmux, for the server and the
    # client alike.
    nobin = tmp_path / 'bin'
    nobin.mkdir()
    for folder in os.environ.get('PATH', '/usr/bin:/bin').split(os.pathsep):
        if not os.path.isdir(folder):
            continue
        for entry in os.listdir(folder):
            # No browser either: a double-click must show the file here.
            skip = entry in ('tmux', 'xdg-open', 'x-www-browser', 'sensible-browser', 'gio')
            if not skip and not os.path.lexists(nobin / entry):
                (nobin / entry).symlink_to(os.path.join(folder, entry))
    assert shutil.which('tmux', path=str(nobin)) is None
    home = tmp_path / 'home'
    home.mkdir()
    env = {**os.environ, **OUTSIDE_WORLD, 'LECTERN_MOCK': '0', 'LECTERN_PORT': str(port),
           'LECTERN_HOST': '127.0.0.1', 'LECTERN_DB': str(tmp_path / 'test.db'),
           'LECTERN_AUTH_TOKEN': '', 'LECTERN_SESSION_POLL': '1', 'LECTERN_TICK': '0.25',
           'LECTERN_SESSION_BACKEND': 'pty', 'LECTERN_PTYHOST_SOCKET': socket,
           'LECTERN_SCRATCH_ROOT': str(tmp_path / 'scratch'), 'LECTERN_API': url,
           'LECTERN_CLAUDE_BIN': str(agent), 'HOME': str(home), 'DISPLAY': '', 'WAYLAND_DISPLAY': '',
           'XDG_STATE_HOME': str(tmp_path / 'state'), 'TMUX': '', 'PATH': str(nobin)}
    (tmp_path / 'scratch').mkdir()
    log = open(tmp_path / 'server.log', 'a')
    proc = subprocess.Popen([_binary(), 'serve'], cwd=root, env=env, stdout=log, stderr=log)

    def api(path, data=None):
        req = urllib.request.Request(url + '/api' + path, data=json.dumps(data).encode() if data is not None else None,
                                     headers={'Content-Type': 'application/json'})
        return json.load(urllib.request.urlopen(req, timeout=20))

    def pty(*args):
        return subprocess.run([_binary(), 'pty', *args], env=env, capture_output=True, text=True)

    try:
        for _ in range(150):
            try:
                api('/health')
                break
            except Exception:
                time.sleep(.1)
        target = api('/targets', {'name': 'bare-local', 'kind': 'local'})
        sess = api('/sessions', {'target_id': target['id'], 'workdir': str(root), 'agent': 'claude',
                                 'name': 'Bare agent', 'permission_mode': 'ask'})
        deadline = time.time() + 15
        while time.time() < deadline and not (root / 'agent-pid').exists():
            time.sleep(.1)
        yield dict(url=url, api=api, pty=pty, env=env, root=root, id=sess['id'], name=sess['tmux_session'], home=home)
    finally:
        proc.terminate()
        proc.wait(timeout=15)
        log.close()
        subprocess.run([_binary(), 'ptyhost', 'stop', '--force', '--socket', socket], env=env, capture_output=True)
        socket_dir.cleanup()


def attach(t):
    d = Dashboard(dict(url=t['url'], env=t['env']), args=('attach', 'session', str(t['id'])))
    d.wait('stub agent ready')
    d.wait('Ctrl+] d leave')
    return d


def chord(d, key):
    d.send('\x1d')
    d.pump(CHORD_GAP)
    d.send(key)


def test_key_bar_menu_and_leaving_without_tmux(bare):
    t = bare
    d = attach(t)
    try:
        assert 'Ctrl+] menu' in d.text and 'Ctrl+\\ send file' in d.text and 'double-click opens paths' in d.text
        d.send('hello there\r')
        d.wait('you said: hello there')
        d.send('\x1d')
        d.wait('Ctrl+] then')
        d.wait('d leave')
        d.send('?')
        d.wait('Attach keys')
        for item in ('Lectern actions for this session', 'Shell to the right', 'Leave (the session keeps running)'):
            d.wait(item)
        d.send('\x1b')
        d.wait_gone('Attach keys')
        # Ctrl-b is the agent's own key now; nothing pretends it detaches.
        d.send('\x02')
        chord(d, 'd')
        d.wait('Left the session; it keeps running')
        d.proc.wait(timeout=10)
        assert d.proc.returncode == 0
        assert t['pty']('has-session', '-t', '=' + t['name']).returncode == 0
    finally:
        d.close()


def test_ctrl_backslash_sends_a_file_and_never_kills_the_agent(bare, tmp_path):
    t = bare
    pid = int((t['root'] / 'agent-pid').read_text())
    d = attach(t)
    try:
        d.send('\x1c')
        d.wait('Local file path')
        os.kill(pid, 0)  # still alive: no SIGQUIT reached it
        upload = tmp_path / 'context.txt'
        upload.write_text('context for the agent\n')
        d.send(str(upload) + '\r')
        # The upload's path is typed into the agent's pane, Enter not pressed.
        d.wait('Ctrl+] d leave', timeout=15)
        d.send('\r')
        d.wait('you said: ')
        os.kill(pid, 0)
        # Esc from the send-file form goes straight back to the agent.
        d.send('\x1c')
        d.wait('Local file path')
        d.send('\x1b')
        d.wait('Ctrl+] d leave')
        d.send('still here\r')
        d.wait('you said: still here')
        chord(d, 'd')
        d.proc.wait(timeout=10)
    finally:
        d.close()


def test_needs_you_shows_and_ctrl_bracket_answers(bare):
    t = bare
    d = attach(t)
    try:
        d.send('ask\r')
        d.wait('⏸ Needs you · Ctrl+] y allow', timeout=20)
        chord(d, 'y')
        d.wait('GOT_ANSWER', timeout=20)
        decision = json.loads((t['root'] / 'hook-response.json').read_text())['hookSpecificOutput']['decision']
        assert decision['behavior'] == 'allow'
        (t['root'] / 'hook-response.json').unlink()
        d.wait_gone('⏸ Needs you', timeout=10)
        # Ctrl+] m leads with the answers and returns to the agent after one.
        d.send('ask\r')
        d.wait('⏸ Needs you', timeout=20)
        chord(d, 'm')
        d.wait('Allow once')
        d.send('y')
        d.wait('GOT_ANSWER', timeout=20)
        d.wait('Ctrl+] d leave')
        chord(d, 'd')
        d.proc.wait(timeout=10)
    finally:
        d.close()


def test_shell_beside_the_agent_and_links(bare):
    t = bare
    d = attach(t)
    try:
        chord(d, '|')
        d.wait('│', timeout=20)
        d.wait('shell 2/2')
        d.send('echo SPLIT-$((6*7))\r')
        d.wait('SPLIT-42')
        chord(d, 'x')
        d.wait_gone('shell 2/2')
        # Double-click the path the agent printed: with no desktop to open
        # it, it is shown here, then the session comes back.
        row = next(y for y, line in enumerate(d.screen.display) if 'notes.txt' in line)
        col = d.screen.display[row].index('notes.txt') + 2
        click = f'\x1b[<0;{col + 1};{row + 1}M\x1b[<0;{col + 1};{row + 1}m'
        d.send(click + click)
        d.wait('NOTES FILE CONTENT', timeout=20)
        d.send('q')
        d.wait('Ctrl+] d leave')
        # Ctrl+] e labels it instead.
        chord(d, 'e')
        d.wait('Type a label', timeout=15)
        d.send('\x1b')
        d.wait('Ctrl+] d leave')
        chord(d, 'd')
        d.proc.wait(timeout=10)
    finally:
        d.close()


def test_a_killed_agent_never_shows_its_secrets(bare):
    t = bare
    token = (t['root'] / 'agent-token').read_text()
    assert len(token) > 16
    pid = int((t['root'] / 'agent-pid').read_text())
    # Nothing on a command line: not the pane's shell, not the agent.
    for proc in Path('/proc').iterdir():
        if proc.name.isdigit():
            try:
                assert token.encode() not in (proc / 'cmdline').read_bytes(), proc
            except (FileNotFoundError, PermissionError, ProcessLookupError):
                pass
    # The private file that carried it is gone once read.
    assert not list((t['home'] / '.lectern' / 'hooks').glob('*.env'))
    os.kill(pid, signal.SIGQUIT)
    deadline = time.time() + 10
    screen = ''
    while time.time() < deadline:
        screen = t['pty']('capture-pane', '-p', '-t', '=' + t['name'] + ':', '-S', '-200').stdout
        if '$ ' in screen or '# ' in screen:
            break
        time.sleep(.2)
    assert token not in screen, screen
    assert 'LECTERN_HOOK' not in screen and 'Bearer' not in screen, screen
