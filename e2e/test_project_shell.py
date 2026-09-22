"""Project Enter opens files in a shell even with an agent tmux default."""
import shlex
import subprocess
import time

from test_terminal_dashboard import Dashboard
from test_terminal_workspace import real_terminal


def test_project_enter_opens_shell_without_starting_agent(real_terminal):
    t = real_terminal
    project = t['api']('/projects', {
        'name': 'Open project files', 'target_id': t['target_id'],
        'repo_path': str(t['root']), 'default_agent': 'claude',
    })
    sessions_before = t['api']('/sessions')
    sentinel = t['root'] / 'agent-started'
    subprocess.run(['tmux', 'set-option', '-g', 'default-command',
                    f'touch {shlex.quote(str(sentinel))}; sleep 60'],
                   env=t['env'], check=True)
    d = Dashboard(t)
    tmux_name = f"lec-sh{project['id']}"
    try:
        d.wait('Real terminal')
        d.send('4')
        d.wait('Open project files')
        d.wait('Enter open project shell')
        d.send('\r')
        deadline = time.monotonic() + 12
        while time.monotonic() < deadline:
            d.pump()
            clients = subprocess.run(
                ['tmux', 'list-clients', '-t', tmux_name, '-F', '#{client_name}'],
                env=t['env'], text=True, capture_output=True)
            if clients.returncode == 0 and clients.stdout.strip():
                break
        else:
            raise AssertionError(f'Project shell did not attach:\n{d.text}')
        d.send('pwd > project-cwd.txt; cat hello.txt > project-read.txt\r')
        deadline = time.monotonic() + 5
        while time.monotonic() < deadline and not (t['root'] / 'project-read.txt').exists():
            d.pump()
        assert (t['root'] / 'project-cwd.txt').read_text().strip() == str(t['root'])
        assert (t['root'] / 'project-read.txt').read_bytes() == (t['root'] / 'hello.txt').read_bytes()
        assert not sentinel.exists(), 'tmux default-command launched an agent'
        assert {s['id'] for s in t['api']('/sessions')} == {s['id'] for s in sessions_before}
        d.send('\x02d')
        d.wait('Detached. Session keeps running.')
        d.wait('Open project files')
        d.quit()
    finally:
        d.close()
        subprocess.run(['tmux', 'set-option', '-gu', 'default-command'], env=t['env'], check=True)
        subprocess.run(['tmux', 'kill-session', '-t', '=' + tmux_name], env=t['env'], capture_output=True)
