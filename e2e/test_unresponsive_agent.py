"""A hung agent: the process is alive and takes keystrokes but never draws.
The board and the connection banner both look healthy, so the terminal itself
has to notice — several keys with nothing back — and offer the restart that
resumes the same conversation."""
import subprocess
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal, open_terminal


def _end_cat(t):
    pane = subprocess.check_output(['tmux', 'list-panes', '-t', 'terminal-test', '-F', '#{pane_pid}'], env=t['env'], text=True).split()[0]
    subprocess.run(['pkill', '-TERM', '-P', pane, '-x', 'cat'], check=True)


def test_silent_keystrokes_raise_the_banner_and_output_clears_it(page, real_terminal):
    t = real_terminal
    open_terminal(page, t)
    screen = page.locator('#agent-terminal .xterm-screen')
    # A healthy shell echoes: typing never trips the watchdog.
    page.keyboard.type('echo ok')
    page.keyboard.press('Enter')
    expect(screen).to_contain_text('ok', timeout=10000)
    page.wait_for_timeout(2500)
    expect(page.locator('#unresponsive')).to_have_count(0)
    # Now the pane swallows input in raw mode without echo, like a TUI whose
    # render thread is stuck. tmux repaints its status line once for the first
    # key (the activity flag), which is output; the keys after it get nothing.
    subprocess.run(['tmux', 'send-keys', '-t', 'terminal-test', '-l', 'stty raw -echo; cat > /dev/null'], env=t['env'], check=True)
    subprocess.run(['tmux', 'send-keys', '-t', 'terminal-test', 'Enter'], env=t['env'], check=True)
    page.wait_for_timeout(600)
    page.keyboard.type('abcd', delay=120)
    banner = page.locator('#unresponsive')
    expect(banner).to_be_visible(timeout=5000)
    expect(banner).to_contain_text('not reacting to your keystrokes')
    expect(banner.get_by_role('button', name='Restart agent, keep conversation')).to_be_visible()
    # Dismiss hides it; the next silent keys do not nag again until it recovers.
    banner.get_by_role('button', name='Dismiss').click()
    expect(banner).to_have_count(0)
    # Recovery: in raw mode Ctrl-C and Ctrl-D are plain bytes, so end cat from
    # outside; the shell redraws its prompt and the state clears.
    _end_cat(t)
    subprocess.run(['tmux', 'send-keys', '-t', 'terminal-test', '-l', 'stty sane; echo back'], env=t['env'], check=True)
    subprocess.run(['tmux', 'send-keys', '-t', 'terminal-test', 'Enter'], env=t['env'], check=True)
    expect(screen).to_contain_text('back', timeout=10000)
    page.locator('#agent-terminal').click()  # Dismiss took the focus
    page.keyboard.type('echo again')
    page.keyboard.press('Enter')
    expect(screen).to_contain_text('again', timeout=10000)
    page.wait_for_timeout(2500)
    expect(page.locator('#unresponsive')).to_have_count(0)


def test_restart_is_refused_without_a_saved_conversation(page, real_terminal):
    t = real_terminal
    open_terminal(page, t)
    subprocess.run(['tmux', 'send-keys', '-t', 'terminal-test', '-l', 'stty raw -echo; cat > /dev/null'], env=t['env'], check=True)
    subprocess.run(['tmux', 'send-keys', '-t', 'terminal-test', 'Enter'], env=t['env'], check=True)
    page.wait_for_timeout(600)
    page.keyboard.type('wxyz', delay=120)
    banner = page.locator('#unresponsive')
    expect(banner).to_be_visible(timeout=5000)
    banner.get_by_role('button', name='Restart agent, keep conversation').click()
    expect(page.locator('#notice')).to_contain_text('Could not restart the agent', timeout=10000)
    # The pane is untouched: the session is still tracked and running.
    assert t['api'](f"/sessions/{t['id']}")['ended_at'] is None
    _end_cat(t)
