"""The terminal walkthroughs of the 2026-09-28 usability audit, re-run against
the simpler dashboard (docs/design/simple-tui.md): a real server, real tmux,
the dashboard in a real PTY, and a stub agent that asks for approval through
the same PermissionRequest hook Claude Code uses. Each test counts the keys a
person presses, so a regression back to menu-diving shows up as a number.
"""
import json
import time
from pathlib import Path

import pytest

from conftest import CHORD_GAP
from test_terminal_dashboard import Dashboard
from test_terminal_workspace import real_terminal  # noqa: F401  (fixture)

# Asks once, through the session's own hook, then idles at a prompt.
ASKING_AGENT = '''#!/bin/bash
echo "stub agent ready"
python3 - <<'PYEOF'
import json, os, urllib.request
body = json.dumps({"hook_event_name": "PermissionRequest", "tool_name": "Bash",
                   "tool_input": {"command": "echo hi > NOTES.md"}}).encode()
req = urllib.request.Request(os.environ["LECTERN_HOOK_URL"].rstrip("/") + "/PermissionRequest", data=body,
    headers={"Authorization": "Bearer " + os.environ.get("LECTERN_HOOK_TOKEN", ""), "Content-Type": "application/json"},
    method="POST")
try:
    with urllib.request.urlopen(req, timeout=60) as resp:
        data = resp.read()
except Exception as e:
    data = json.dumps({"error": str(e)}).encode()
open(os.path.join(os.getcwd(), "hook-response.json"), "wb").write(data)
print("GOT_RESPONSE")
PYEOF
while IFS= read -r line; do echo "you said: $line"; done
'''

QUIET_AGENT = '''#!/bin/bash
echo "quiet agent ready"
while IFS= read -r line; do echo "you said: $line"; done
'''


class Counted(Dashboard):
    """A Dashboard that counts keystrokes the way the audit did: each chord or
    named key is one, each typed character is one."""
    keys = 0

    def press(self, *keys):
        for k in keys:
            self.keys += 1 if len(k) == 1 or k.startswith('\x1b[') or k in ('\r', '\x1b') else len(k)
            self.send(k)


def ask_session(t, name):
    return t['api']('/sessions', {'target_id': t['target_id'], 'workdir': str(t['root']), 'agent': 'claude',
                                  'name': name, 'permission_mode': 'ask'})


def hook_decision(t):
    path = Path(t['root']) / 'hook-response.json'
    deadline = time.time() + 20
    while time.time() < deadline and not path.exists():
        time.sleep(.2)
    assert path.exists(), 'the agent never got an answer'
    return json.loads(path.read_text())['hookSpecificOutput']['decision']


# (c) Approve something an agent asks. Before: ~50 keys through a 16-item menu
# and a second confirmation. After: the dashboard says who needs you and y
# allows it from the session row.
@pytest.mark.parametrize('real_terminal', [{'agent_script': ASKING_AGENT}], indirect=True)
def test_walkthrough_c_approve_from_the_session_row(real_terminal):
    t = real_terminal
    ask_session(t, 'Asking agent')
    d = Counted(t)
    try:
        d.wait('Asking agent')
        d.wait('2 Approvals (1)', timeout=25)
        d.wait('needs you')
        d.press('/', *'Asking', '\r')
        d.wait('Needs you')
        d.wait('y allow once')
        d.press('y')
        d.wait('Allowed once')
        assert hook_decision(t)['behavior'] == 'allow'
        assert d.keys <= 10, d.keys
        d.wait_gone('2 Approvals (1)')
        d.quit()
    finally:
        d.close()


# The Approvals pane answers with one key each, and Enter opens the session.
@pytest.mark.parametrize('real_terminal', [{'agent_script': ASKING_AGENT}], indirect=True)
def test_approvals_pane_denies_with_n(real_terminal):
    t = real_terminal
    ask_session(t, 'Denied agent')
    d = Counted(t)
    try:
        d.wait('2 Approvals (1)', timeout=25)
        d.press('2')
        d.wait('Denied agent asks to use Bash')
        d.wait('n deny')
        d.press('n')
        d.wait('Denied:')
        assert hook_decision(t)['behavior'] == 'deny'
        assert d.keys == 2
        d.wait('Nothing needs you right now')
        d.quit()
    finally:
        d.close()


# (d) End a session and bring it back. Before: m, then about twenty arrow
# presses, and menu search for "end" ran Send message. After: x, y, then r.
@pytest.mark.parametrize('real_terminal', [{'agent_script': QUIET_AGENT}], indirect=True)
def test_walkthrough_d_end_and_restore(real_terminal):
    t = real_terminal
    sess = ask_session(t, 'Ending agent')
    d = Counted(t)
    try:
        d.wait('Ending agent')
        d.press('/', *'Ending', '\r')
        # The palette finds End session for "end", never Send message first.
        d.send(':end')
        d.wait('End session')
        top = [line for line in d.text.splitlines() if '›' in line]
        assert top and 'End session' in top[0], d.text
        # Esc clears the search, a second Esc closes the palette.
        d.send('\x1b'); d.pump(.3); d.send('\x1b')
        d.wait_gone(': end')
        d.keys = 0
        d.press('x')
        d.wait('End session “Ending agent”?')
        d.wait('r (Restore) brings it back')
        d.press('y')
        d.wait('Ended "Ending agent". r brings it back.')
        ended = [s for s in t['api']('/sessions?all=true') if s['id'] == sess['id']][0]
        assert ended['ended_at'] is not None
        assert d.keys == 2, d.keys  # was m, about 32 arrows, Enter and y
        d.press('\x1b')
        d.press('r')
        d.wait('Restore')
        d.wait('Ending agent')
        d.press('\x1b', '\x1b')
        d.quit()
    finally:
        d.close()


# (f) Review what changed. q goes back instead of quitting the app, and c
# offers to commit.
def test_walkthrough_f_review_q_goes_back_and_c_commits(real_terminal):
    t = real_terminal
    (Path(t['root']) / 'change.txt').write_text('changed\n')
    d = Counted(t)
    try:
        d.wait('Real terminal')
        d.press('v')
        d.wait('c commit')
        d.press('c')
        d.wait('Commit message')
        d.press('\x1b')
        d.wait('c commit')
        d.press('q')
        d.wait('Real terminal')
        assert d.proc.poll() is None, 'q in the review quit the dashboard'
        d.quit()
    finally:
        d.close()


# (b) See what agents are doing, at 80x24: the key bar keeps quit and help,
# help scrolls instead of closing on the first key, and Esc never quits.
def test_walkthrough_b_key_bar_and_help_at_80_columns(real_terminal):
    t = real_terminal
    d = Counted(t)
    try:
        d.wait('Real terminal')
        d.resize(80, 24)
        d.wait('q quit')
        d.wait('? keys')
        d.wait('1 Sessions')
        d.send('?')
        d.wait('This view: Sessions')
        d.send('\x1b[6~')
        d.wait('Everywhere')
        assert 'Keyboard shortcuts' in d.text
        d.send('/restore')
        d.wait('restore')
        d.send('\r'); d.send('\x1b'); d.pump(.3); d.send('\x1b')
        d.wait_gone('This view')
        d.send('\x1b')
        d.pump(.5)
        assert d.proc.poll() is None, 'Esc quit the dashboard'
        d.quit()
    finally:
        d.close()


# New session: one screen, an installed agent by default, and the session
# starts on the last question.
@pytest.mark.parametrize('real_terminal', [{'agent_script': QUIET_AGENT}], indirect=True)
def test_new_session_is_one_screen_with_an_installed_agent(real_terminal):
    t = real_terminal
    d = Counted(t)
    try:
        d.wait('Real terminal')
        d.press('n')
        d.wait('New session')
        d.wait('Agent: claude')
        d.wait('Approvals: Ask before running commands')
        d.wait('More options…')
        assert 'Worktree base' not in d.text
        d.press('\x1b')
        d.wait_gone('New session')
        d.quit()
    finally:
        d.close()


# The attach bar in plain words at 80 columns: the leave key is never cut
# off, Ctrl+] shows what can follow it, and Ctrl+] d comes back.
def test_attach_bar_keeps_the_leave_key_at_80_columns(real_terminal):
    t = real_terminal
    d = Counted(t)
    try:
        d.wait('Real terminal')
        d.resize(80, 24)
        d.send('\r')
        d.wait('Ctrl+] d leave')
        d.wait('Ctrl+] menu')
        d.send('\x1d'); d.pump(CHORD_GAP)
        d.wait('Ctrl+] then')
        d.send('d')
        d.wait('Detached. Session keeps running.')
        d.quit()
    finally:
        d.close()
