"""Real Claude Code launched by Lectern leaves the mouse to the terminal.

Claude Code's fullscreen renderer ("tui": "fullscreen") turns on full mouse
tracking, so the terminal forwards every click to it and native selection,
links and Lectern's clickable paths stop working. Lectern launches Claude Code
with CLAUDE_CODE_DISABLE_MOUSE=1 unless the setting is off. This drives the
real CLI through Lectern's own launch path, offline, and reads tmux's record of
what the program asked for. Needs the optional isolated Claude binary mount.
"""
import json, shutil, subprocess, time
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_session_restore import request


def pane(t, session, fmt):
    return subprocess.check_output(['tmux', 'display', '-p', '-t', f'={session}:', fmt], env=t['env']).decode().strip()


def screen(t, session):
    return subprocess.run(['tmux', 'capture-pane', '-p', '-t', f'={session}:'], env=t['env'], capture_output=True, text=True).stdout


def wait_for(t, session, text, timeout=30):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        if text in screen(t, session):
            return
        time.sleep(.2)
    raise AssertionError(f'{text!r} never appeared in {session}:\n{screen(t, session)}')


def launch_to_prompt(t, project, name):
    sess = t['api']('/sessions', {'project_id': project['id'], 'target_id': t['target_id'], 'name': name, 'agent': 'claude', 'yolo': False})
    tmux = sess['tmux_session']
    # Offline. Claude may ask once whether to use the custom key; say yes.
    end = time.monotonic() + 40
    while time.monotonic() < end:
        text = screen(t, tmux)
        if 'Claude Code v' in text and '❯ ' in text and 'custom API key' not in text:
            break
        if 'custom API key' in text:
            time.sleep(1)
            subprocess.run(['tmux', 'send-keys', '-t', f'={tmux}:', 'Up'], env=t['env'], check=True)
            time.sleep(.5)
            subprocess.run(['tmux', 'send-keys', '-t', f'={tmux}:', 'Enter'], env=t['env'], check=True)
        time.sleep(.3)
    wait_for(t, tmux, 'Claude Code v')
    time.sleep(2)
    return tmux


@pytest.mark.skipif(not shutil.which('claude'), reason='explicit Claude CLI audit only')
def test_real_claude_fullscreen_leaves_the_mouse_to_the_terminal(real_terminal):
    t = real_terminal
    home = t['root'] / 'claude-mouse-home'; home.mkdir()
    (home / '.claude.json').write_text(json.dumps({'hasCompletedOnboarding': True, 'theme': 'dark', 'numStartups': 1,
        'customApiKeyResponses': {'approved': [], 'rejected': []}}))
    (home / 'settings.json').write_text(json.dumps({'tui': 'fullscreen'}))
    project = t['api']('/projects', {'name': 'Mouse', 'target_id': t['target_id'], 'repo_path': str(t['root']), 'env': {
        'CLAUDE_CONFIG_DIR': str(home), 'ANTHROPIC_API_KEY': 'lectern-mouse-audit', 'ANTHROPIC_BASE_URL': 'http://127.0.0.1:1',
        'DISABLE_AUTOUPDATER': '1', 'CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC': '1'}})

    # Default: on. Fullscreen still draws, the mouse stays with the terminal.
    left = launch_to_prompt(t, project, 'mouse left to terminal')
    assert pane(t, left, '#{alternate_on}') == '1', 'fullscreen did not engage'
    assert pane(t, left, '#{mouse_any_flag}#{mouse_all_flag}') == '00', screen(t, left)

    # Off: Claude Code captures the mouse again, which is the behaviour the
    # setting exists to avoid. This is the control that proves the check sees it.
    assert request(t, 'PUT', '/settings', {'claude_terminal_mouse': '0'})[0] == 200
    captured = launch_to_prompt(t, project, 'mouse captured by Claude')
    assert pane(t, captured, '#{alternate_on}') == '1'
    assert pane(t, captured, '#{mouse_any_flag}#{mouse_all_flag}') == '11', screen(t, captured)


def test_mouse_setting_and_project_override_in_settings(page, real_terminal):
    t = real_terminal
    project = t['api']('/projects', {'name': 'Mouse override', 'target_id': t['target_id'], 'repo_path': str(t['root'])})
    page.goto(t['url'] + '/#targets')
    page.locator('[data-settings="workspace"]').click()
    toggle = page.locator('[data-setting="workspace.claudeMouse"] input[type=checkbox]')
    expect(toggle).to_be_checked()
    toggle.click()
    expect(toggle).not_to_be_checked()
    for _ in range(50):
        if t['api']('/settings').get('claude_terminal_mouse') == '0': break
        time.sleep(.1)
    assert t['api']('/settings')['claude_terminal_mouse'] == '0'
    page.locator('[data-settings="projects"]').click()
    page.locator(f'#project-{project["id"]}').click()
    override = page.locator('[data-setting="projects.claudeMouse"] select')
    expect(override).to_have_value('')
    override.select_option('1')
    for _ in range(50):
        if any(p['id'] == project['id'] and p['claude_terminal_mouse'] == '1' for p in t['api']('/projects')): break
        time.sleep(.1)
    assert [p['claude_terminal_mouse'] for p in t['api']('/projects') if p['id'] == project['id']] == ['1']
