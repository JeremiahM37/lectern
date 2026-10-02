"""The browser terminal on the PTY-host session backend (docs/ptyhost.md):
no tmux and no ttyd anywhere in the path, and a server restart that ends
nothing."""
import json
import os
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path

import pytest
from playwright.sync_api import expect

from conftest import OUTSIDE_WORLD, _binary, _unused_port


def _start(env, root, log_path):
    log = open(log_path, 'a')
    return subprocess.Popen([_binary(), "serve"], cwd=root, env=env, stdout=log, stderr=log), log


@pytest.fixture()
def pty_server(tmp_path):
    socket_dir = tempfile.TemporaryDirectory(prefix='lpty-', dir='/tmp')
    socket = str(Path(socket_dir.name) / 's.sock')
    port = _unused_port()
    url = f'http://127.0.0.1:{port}'
    root = tmp_path / 'workspace'
    root.mkdir()
    subprocess.run(['git', 'init', '-q', str(root)], check=True)
    env = {**os.environ, **OUTSIDE_WORLD, 'LECTERN_MOCK': '0', 'LECTERN_PORT': str(port),
           'LECTERN_HOST': '127.0.0.1', 'LECTERN_DB': str(tmp_path / 'test.db'),
           'LECTERN_AUTH_TOKEN': '', 'LECTERN_SESSION_POLL': '1', 'LECTERN_TICK': '0.25',
           'LECTERN_SESSION_BACKEND': 'pty', 'LECTERN_PTYHOST_SOCKET': socket,
           'LECTERN_SCRATCH_ROOT': str(tmp_path / 'scratch'), 'LECTERN_API': url,
           'XDG_STATE_HOME': str(tmp_path / 'state'), 'TMUX': ''}
    (tmp_path / 'scratch').mkdir()
    state = {'proc': None, 'log': None}

    def api(path, data=None):
        req = urllib.request.Request(url + '/api' + path, data=json.dumps(data).encode() if data is not None else None,
                                     headers={'Content-Type': 'application/json'})
        return json.load(urllib.request.urlopen(req, timeout=20))

    def start():
        state['proc'], state['log'] = _start(env, root, tmp_path / 'server.log')
        for _ in range(150):
            try:
                api('/health')
                return
            except Exception:
                time.sleep(.1)
        raise RuntimeError('server did not start: ' + (tmp_path / 'server.log').read_text()[-4000:])

    def stop():
        state['proc'].kill()
        state['proc'].wait(timeout=15)
        state['log'].close()

    def pty(*args):
        return subprocess.run([_binary(), 'pty', *args], env=env, capture_output=True, text=True)

    start()
    try:
        target = api('/targets', {'name': 'pty-local', 'kind': 'local'})
        checked = api(f'/targets/{target["id"]}/check', {})
        assert json.loads(checked['info_json'])['session_backend'] == 'pty', checked
        yield dict(url=url, api=api, start=start, stop=stop, pty=pty, target_id=target['id'], env=env)
    finally:
        try:
            stop()
        except Exception:
            pass
        subprocess.run([_binary(), 'ptyhost', 'stop', '--force', '--socket', socket], env=env, capture_output=True)
        socket_dir.cleanup()


def _shell_frame(page, t):
    before = {s['id'] for s in t['api']('/sessions')}
    page.goto(t['url'] + '/#terminals')
    page.locator('.terminal-empty').get_by_role('button', name='New terminal').click()
    expect(page.get_by_role('tab')).to_have_count(1, timeout=15000)
    shell = [s for s in t['api']('/sessions') if s['id'] not in before][0]
    frame = page.frame_locator(f'iframe[src="/terminal/session/{shell["id"]}?embed=1"]')
    expect(frame.locator('#connection')).to_have_text('Connected', timeout=20000)
    return shell, frame


def test_a_new_terminal_runs_on_the_pty_host(page, pty_server):
    t = pty_server
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    shell, f = _shell_frame(page, t)
    f.locator('#agent-terminal').click()
    page.keyboard.type('echo PTY-SHELL-$((6*7))')
    page.keyboard.press('Enter')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('PTY-SHELL-42', timeout=10000)
    # The host, not tmux, holds it: its own capture shows the same screen.
    out = t['pty']('capture-pane', '-p', '-t', '=' + shell['tmux_session'] + ':')
    assert out.returncode == 0 and 'PTY-SHELL-42' in out.stdout, out
    # Text sent through the API arrives as typed input.
    t['api'](f'/sessions/{shell["id"]}/send', {'text': 'echo SENT-$((40+2))'})
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('SENT-42', timeout=10000)
    # A reload attaches again and is drawn from the host's snapshot.
    page.reload()
    f = page.frame_locator(f'iframe[src="/terminal/session/{shell["id"]}?embed=1"]')
    expect(f.locator('#connection')).to_have_text('Connected', timeout=20000)
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('SENT-42', timeout=10000)
    assert not errors, errors


def test_a_server_restart_leaves_the_shell_running(page, pty_server):
    t = pty_server
    shell, f = _shell_frame(page, t)
    f.locator('#agent-terminal').click()
    page.keyboard.type('export KEEP=kept-$((7*6)); echo BEFORE-RESTART')
    page.keyboard.press('Enter')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('BEFORE-RESTART', timeout=10000)
    t['stop']()
    expect(f.locator('#connection')).not_to_have_text('Connected', timeout=15000)
    t['start']()
    # The page reconnects by itself to the same shell, variables and all.
    expect(f.locator('#connection')).to_have_text('Connected', timeout=30000)
    f.locator('#agent-terminal').click()
    page.keyboard.type('echo AFTER-$KEEP')
    page.keyboard.press('Enter')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('AFTER-kept-42', timeout=10000)
    row = t['api'](f'/sessions/{shell["id"]}')
    assert row.get('ended_at') in (None, 0) and row['status'] != 'dead', row
    history = t['api'](f'/term/session/{shell["id"]}/history')
    assert 'BEFORE-RESTART' in history['text'], history


def test_demo_approval_writes_once_and_exit_is_stopped(page, pty_server, tmp_path):
    """A real built-in demo, Go helpers and PTY host, with no agent CLI."""
    t = pty_server
    work = tmp_path / 'demo-work'
    work.mkdir()
    row = t['api']('/sessions', {'target_id': t['target_id'], 'agent': 'demo',
                               'workdir': str(work), 'permission_mode': 'ask'})
    output = work / 'demo-notes.md'
    t['api'](f'/sessions/{row["id"]}/send', {'text': 'first real demo edit'})
    for _ in range(150):
        pending = [a for a in t['api']('/approvals?status=pending') if a.get('session_id') == row['id']]
        if pending:
            break
        time.sleep(.1)
    assert pending, t['pty']('capture-pane', '-p', '-t', '=' + row['tmux_session'] + ':').stdout
    assert not output.exists(), 'demo wrote before a human decided'
    t['api'](f'/approvals/{pending[0]["id"]}/decision', {'decision': 'approved'})
    for _ in range(100):
        if output.exists():
            break
        time.sleep(.1)
    assert output.read_text() == '- first real demo edit\n'
    t['api'](f'/sessions/{row["id"]}/send', {'text': 'second edit'})
    for _ in range(100):
        if 'second edit' in output.read_text():
            break
        time.sleep(.1)
    assert output.read_text() == '- first real demo edit\n- second edit\n'
    assert not [a for a in t['api']('/approvals?status=pending') if a.get('session_id') == row['id']]
    # EOF exits the agent while leaving its terminal shell alive.
    t['pty']('send-keys', '-t', '=' + row['tmux_session'] + ':', 'C-d')
    for _ in range(180):
        stopped = t['api'](f'/sessions/{row["id"]}')
        if stopped.get('agent_exited_at'):
            break
        time.sleep(.1)
    assert stopped.get('state_label') == 'Stopped', stopped
    assert stopped.get('ended_at') is None, stopped
    page.goto(t['url'] + '/#sessions')
    card = page.locator(f'.scard[data-session-id="{row["id"]}"]')
    expect(card.locator('.status-badge')).to_have_text('Stopped')
    expect(card.get_by_role('button', name='↻ Revive')).to_be_visible()


def test_missing_agent_creates_no_session_or_scratch_directory(pty_server):
    from urllib.error import HTTPError
    t = pty_server
    scratch = Path(t['env']['LECTERN_SCRATCH_ROOT'])
    before_dirs = set(scratch.iterdir())
    before_sessions = t['api']('/sessions')
    try:
        t['api']('/sessions', {'target_id': t['target_id'], 'agent': 'gemini', 'scratch': True})
    except HTTPError as error:
        body = error.read().decode()
        assert "isn't installed" in body, body
    else:
        raise AssertionError('missing agent was reported as successfully started')
    assert t['api']('/sessions') == before_sessions
    assert set(scratch.iterdir()) == before_dirs


def test_generated_session_names_are_distinct_but_explicit_names_are_kept(pty_server, tmp_path):
    t = pty_server
    work = tmp_path / 'named-project'
    work.mkdir()
    def launch(**extra):
        return t['api']('/sessions', {'target_id': t['target_id'], 'agent':'demo', 'workdir':str(work), **extra})
    first, second = launch(), launch()
    assert first['name'] == 'named-project', first
    assert second['name'] == 'named-project #2', second
    explicit = launch(name='My chosen name')
    assert explicit['name'] == 'My chosen name', explicit
