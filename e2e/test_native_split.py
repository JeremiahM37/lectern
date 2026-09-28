"""Splitting a native attachment opens a shell where the session is
(docs/terminal-client.md): on the session's machine, in the directory its
agent pane is in — through Ctrl+] %, and through tmux's right-click menu."""
import os
import shlex

import pytest

from test_attached_controls import remote_terminal  # noqa: F401
from test_remote_acceptance import _ssh  # noqa: F401
from test_terminal_dashboard import Dashboard
from test_terminal_workspace import real_terminal  # noqa: F401


def right_click(d, text):
    for y, row in enumerate(d.screen.display):
        if text in row:
            x = row.index(text) + 1
            os.write(d.master, f'\x1b[<2;{x};{y + 1}M'.encode())
            os.write(d.master, f'\x1b[<2;{x};{y + 1}m'.encode())
            d.pump(.3)
            return
    raise AssertionError(f'{text!r} is not on screen:\n{d.text}')


def split_and_check(d, where, home):
    # The pane is narrow, so it answers in a word: the quotes keep the answer
    # itself out of the typed command.
    check = '[ "$PWD" = %s ] && echo SPLIT-HE""RE' % shlex.quote(where)
    if home:
        check += '; [ "$HOME" = %s ] && echo HOME-THE""RE' % shlex.quote(home)
    d.send(check + '\r')
    d.wait('SPLIT-HERE', timeout=20)
    if home:
        d.wait('HOME-THERE')


@pytest.mark.parametrize('remote', [False, True], ids=['local', 'ssh'])
def test_split_opens_a_shell_in_the_agents_directory_on_its_machine(request, remote, tmp_path):
    t = request.getfixturevalue('remote_terminal' if remote else 'real_terminal')
    root = t['remote_root'] if remote else t['root']
    # A local target is this machine, so only a remote one has another home.
    home = str(t['remote_home']) if remote else ''
    where = root / 'deep dir'
    if remote:
        _ssh(t, 'mkdir -p ' + shlex.quote(str(where)))
    else:
        where.mkdir()
    # The operator's own machine has a different home, as a laptop would.
    laptop = tmp_path / 'laptop-home'
    laptop.mkdir()
    d = Dashboard(dict(t, env={**t['env'], 'HOME': str(laptop)}), args=('attach', 'session', str(t['id'])))
    try:
        d.wait('Ctrl+]')
        d.send('cd ' + shlex.quote(str(where)) + " && clear && printf 'AGENT-READY\\n'\r")
        d.wait('AGENT-READY')
        # Ctrl+] % splits the attachment side by side.
        d.send('\x1d%')
        split_and_check(d, str(where), home)
        d.send('exit\r')
        d.wait_gone('SPLIT-HERE')
        # tmux's right-click menu, off any link: Horizontal Split.
        right_click(d, 'AGENT-READY')
        d.wait('Horizontal Split')
        d.send('h')
        split_and_check(d, str(where), home)
        assert str(laptop) not in d.text
    finally:
        d.close()
