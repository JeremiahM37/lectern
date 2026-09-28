"""Double-click and right-click on paths and links in a native attachment
(docs/terminal-client.md): a real `lectern attach` in a real PTY, wrapping an
SSH attachment to another target, with the desktop's opener replaced by a
recorder. The screen is the exact bytes Codex printed for the owner's link;
the PDF it names lives on the SSH target, outside the workspace."""
import glob
import os
import subprocess
import time
from pathlib import Path

from test_attached_controls import remote_terminal  # noqa: F401
from test_remote_acceptance import _ssh  # noqa: F401
from test_terminal_dashboard import Dashboard
from test_terminal_file_links import FIXTURES, OWNER_PDF, owner_files  # noqa: F401
from test_terminal_workspace import real_terminal  # noqa: F401


def laptop(t, tmp_path):
    """The operator's machine: a desktop session whose opener records what it
    is asked to open (and keeps a copy, since opened files are swept later)."""
    home = tmp_path / 'laptop'
    bin_dir = home / 'bin'
    bin_dir.mkdir(parents=True)
    log = home / 'opened.log'
    opener = bin_dir / 'xdg-open'
    opener.write_text(f'#!/bin/sh\nprintf "%s\\n" "$1" >> {log}\n'
                      f'if [ -f "$1" ]; then cp "$1" {log}.copy; stat -c %a "$(dirname "$1")" >> {log}; fi\n')
    opener.chmod(0o755)
    env = {k: v for k, v in t['env'].items() if not k.startswith('SSH_')}
    env.update(HOME=str(home), DISPLAY=':99', PATH=f"{bin_dir}:{env['PATH']}")
    return dict(t, env=env), home, log


def cell(d, text, offset):
    for y, row in enumerate(d.screen.display):
        if text in row:
            return row.index(text) + offset + 1, y + 1
    raise AssertionError(f'{text!r} is not on screen:\n{d.text}')


def click(d, x, y, button=0, times=1):
    # One write per report, as a terminal sends them.
    for _ in range(times):
        os.write(d.master, f'\x1b[<{button};{x};{y}M'.encode())
        os.write(d.master, f'\x1b[<{button};{x};{y}m'.encode())
    d.pump(.3)


def wait_lines(log, count, timeout=15):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if log.exists() and len(log.read_text().splitlines()) >= count:
            return log.read_text().splitlines()
        time.sleep(.1)
    raise AssertionError(f'the opener ran {log.read_text() if log.exists() else "never"}')


def private_buffer():
    socks = glob.glob('/tmp/lectern-attach-*/sock') + glob.glob(os.path.join(os.environ.get('TMPDIR', '/tmp'), 'lectern-attach-*/sock'))
    for sock in socks:
        out = subprocess.run(['tmux', '-S', sock, 'show-buffer'], capture_output=True, text=True)
        if out.returncode == 0:
            return out.stdout
    return None


def test_native_double_and_right_click_open_on_the_operators_machine(remote_terminal, owner_files, tmp_path):
    t, home, log = laptop(remote_terminal, tmp_path)
    fake = '\\033[<0;12;18M\\033[<0;12;18m'
    screen = FIXTURES / 'codex-markdown-link.bin'
    d = Dashboard(t, args=('attach', 'session', str(t['id'])))
    try:
        d.wait('double-click')
        # The agent's output also carries what a double-click would send.
        d.send(f"clear; cat {screen}; printf '{fake}{fake}'; echo\r")
        d.wait('Jeremiah_Mackey_Cerebras.pdf)')
        d.pump(1)
        assert not log.exists(), 'printed mouse reports opened something'

        # Every row of the wrapped path opens the owner's PDF here, fetched
        # from the SSH target into a private folder.
        for n, (text, offset) in enumerate((('(/home/admin/.formwork/', 6), ('application-testing-20260927/', 4), ('Jeremiah_Mackey_Cerebras.pdf)', 5)), 1):
            x, y = cell(d, text, offset)
            click(d, x, y, times=2)
            lines = wait_lines(log, 2 * n)
            assert Path(lines[-2]).name == 'Jeremiah_Mackey_Cerebras.pdf', lines
            assert lines[-1] == '700', lines
            assert Path(f'{log}.copy').read_bytes() == owner_files
            d.wait('Opened Jeremiah_Mackey_Cerebras.pdf')

        # A web address opens in the browser.
        x, y = cell(d, 'https://example.com/docs/page', 9)
        click(d, x, y, times=2)
        assert wait_lines(log, 7)[-1] == 'https://example.com/docs/page'

        # A word is still a word: tmux selects and copies it.
        x, y = cell(d, 'Both', 1)
        click(d, x, y, times=2)
        deadline = time.monotonic() + 5
        while private_buffer() != 'Both' and time.monotonic() < deadline:
            time.sleep(.1)
        assert private_buffer() == 'Both'
        assert len(log.read_text().splitlines()) == 7

        # Right-click: the link menu. Download lands in ~/Downloads.
        x, y = cell(d, 'application-testing-20260927/', 4)
        click(d, x, y, button=2)
        d.wait('Open on this machine')
        d.wait('Send path to the agent')
        d.send('d')
        d.wait('Downloaded to ~/Downloads/Jeremiah_Mackey_Cerebras.pdf')
        assert (home / 'Downloads' / 'Jeremiah_Mackey_Cerebras.pdf').read_bytes() == owner_files
        # Send path to the agent types it, quoted, without pressing Enter.
        click(d, x, y, button=2)
        d.wait('Send path to the agent')
        d.send('s')
        d.wait(f'{OWNER_PDF}')
        d.send('\x03')
        # Off any link, tmux's own menu.
        x, y = cell(d, 'review:', 1)
        click(d, x, y, button=2)
        d.wait('Horizontal Split')
        d.send('\x1b')
        d.wait_gone('Horizontal Split')
    finally:
        d.close()
