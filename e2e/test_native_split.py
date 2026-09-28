"""A shell beside an attached agent, on the agent's machine, in the agent's
directory (docs/terminal-client.md): through Ctrl+] | and Ctrl+] -, Ctrl+] %,
the right-click menu's "Split: shell in project", and `lectern split` run from
a new window of the operator's own terminal. Each shell is a tracked Lectern
shell session on the session's target."""
import os
import shlex
import time

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


def check_here(d, where, home, mark):
    """The new pane's shell reports where it is into a file, which is read
    here: more robust than the screen, which a narrow pane wraps."""
    if mark.exists():
        mark.unlink()
    check = '[ "$PWD" = %s ] && echo here > %s' % (shlex.quote(where), shlex.quote(str(mark)))
    if home:
        check += '; [ "$HOME" = %s ] && echo home >> %s' % (shlex.quote(home), shlex.quote(str(mark)))
    d.send(check + '\r')
    want = 'here\nhome\n' if home else 'here\n'
    deadline = time.monotonic() + 20
    while time.monotonic() < deadline:
        d.pump(.2)
        if mark.exists() and mark.read_text() == want:
            return
    raise AssertionError(f'the shell is not in {where}: {mark.read_text() if mark.exists() else "no answer"}\n{d.text}')


def close_shell(d):
    d.send('exit\r')
    d.pump(1)


@pytest.mark.parametrize('remote', [False, True], ids=['local', 'ssh'])
def test_split_opens_a_tracked_shell_in_the_agents_directory_on_its_machine(request, remote, tmp_path):
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
    mark = tmp_path / 'answer'
    env = {**t['env'], 'HOME': str(laptop), 'XDG_RUNTIME_DIR': str(tmp_path / 'run')}
    (tmp_path / 'run').mkdir(mode=0o700)
    d = Dashboard(dict(t, env=env), args=('attach', 'session', str(t['id'])))
    try:
        d.wait('Ctrl+] menu')
        d.send('cd ' + shlex.quote(str(where)) + " && clear && printf 'AGENT-READY\\n'\r")
        d.wait('AGENT-READY')
        for keys in ('\x1d|', '\x1d-', '\x1d%'):
            d.send(keys)
            check_here(d, str(where), home, mark)
            close_shell(d)
        # The right-click menu, off any link.
        right_click(d, 'AGENT-READY')
        d.wait('Split: shell in project')
        d.send('|')
        check_here(d, str(where), home, mark)
        assert str(laptop) not in d.text
        # Every split was a tracked shell session there.
        shells = [s for s in t['api']('/sessions') if s.get('agent') == 'shell' and s.get('workdir') == str(where)]
        assert len(shells) == 4, shells

        # `lectern split` in a new window of the operator's terminal finds the
        # attachment next door and opens the same kind of shell.
        split = Dashboard(dict(t, env=env), args=('split',))
        try:
            split.wait('Ctrl+]')
            split.wait(where.name + '$')  # the shell is ready for input
            check_here(split, str(where), home, mark)
            split.send("[ -n \"$LECTERN_SESSION_ID\" ] && echo TRACK\"\"ED\r")
            split.wait('TRACKED')
        finally:
            split.close()
        # And with --dir workdir, the session's workdir instead.
        split = Dashboard(dict(t, env=env), args=('split', '--session', str(t['id']), '--dir', 'workdir'))
        try:
            split.wait('Ctrl+]')
            split.wait(root.name + '$')
            check_here(split, str(root), home, mark)
        finally:
            split.close()
    finally:
        d.close()
