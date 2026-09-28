"""The owner's desktop configuration: `lectern attach` from a machine that
reaches the server through an SSH alias (LECTERN_ATTACH_HOST), with no
LECTERN_AUTH_TOKEN (identity comes from the connection, as with Tailscale).
The private server there must still carry the link bindings, and a
double-click on the owner's wrapped PDF path must fetch it and hand it to the
desktop's opener."""
import os
import time
from pathlib import Path

from test_native_terminal_links import cell, click, wait_lines
from test_terminal_dashboard import Dashboard
from test_terminal_file_links import FIXTURES, owner_files  # noqa: F401
from test_terminal_workspace import real_terminal  # noqa: F401


def test_attach_through_an_ssh_alias_without_a_token_opens_links(real_terminal, owner_files, tmp_path):
    t = real_terminal
    home = tmp_path / 'desktop'
    tools = home / 'bin'
    tools.mkdir(parents=True)
    # The alias: this stand-in for ssh reaches the session's tmux, as
    # `ssh -tt agentdeck lectern --hosted-attach attach …` does.
    (tools / 'ssh').write_text('#!/bin/sh\nexec tmux attach -t =terminal-test\n')
    log = home / 'opened.log'
    (tools / 'xdg-open').write_text(f'#!/bin/sh\nprintf "%s\\n" "$1" >> {log}\ncp "$1" {log}.copy 2>/dev/null; true\n')
    for tool in tools.iterdir():
        tool.chmod(0o755)
    env = {k: v for k, v in t['env'].items() if not k.startswith('SSH_') and k != 'LECTERN_AUTH_TOKEN'}
    env.update(HOME=str(home), DISPLAY=':99', PATH=f"{tools}:{env['PATH']}", LECTERN_ATTACH_HOST='desktop-alias')
    d = Dashboard(dict(t, env=env), args=('attach', 'session', str(t['id'])))
    try:
        d.wait('double-click')
        d.send(f"clear; cat {FIXTURES / 'codex-markdown-link.bin'}; echo\r")
        d.wait('Jeremiah_Mackey_Cerebras.pdf)')
        socks = [p for p in Path(os.environ.get('TMPDIR', '/tmp')).glob('lectern-attach-*/sock')]
        assert socks, 'no private server'
        import subprocess
        keys = subprocess.run(['tmux', '-S', str(socks[-1]), 'list-keys', '-T', 'root'], capture_output=True, text=True).stdout
        assert 'link.sh click' in keys and 'link.sh menu' in keys, keys
        x, y = cell(d, 'application-testing-20260927/', 4)
        click(d, x, y, times=2)
        lines = wait_lines(log, 1)
        assert Path(lines[0]).name == 'Jeremiah_Mackey_Cerebras.pdf', lines
        assert Path(f'{log}.copy').read_bytes() == owner_files
        d.wait('Opened Jeremiah_Mackey_Cerebras.pdf')
    finally:
        d.close()
