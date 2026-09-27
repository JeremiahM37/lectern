"""The floating terminal, find in scrollback, OSC 52 copy and quick commands,
on real ttyd + tmux, at desk and phone widths."""
import base64
import json
import urllib.request

import pytest
from playwright.sync_api import expect
from conftest import PHONE
from test_terminal_workspace import real_terminal, capture  # noqa: F401
from test_terminal_split import attach, frame, ready


def floating_frame(page):
    return page.frame_locator('#floating-terminal .floating-frame:not([hidden]) iframe')


def test_floating_terminal_has_its_own_tabs_and_survives_hiding(page, real_terminal):
    t = real_terminal; errors = []; page.on('pageerror', lambda e: errors.append(str(e)))
    page.goto(t['url'] + '/#board')
    expect(page.locator('#board')).to_be_visible()
    panel = page.locator('#floating-terminal')
    expect(panel).to_be_hidden()
    page.keyboard.press('Control+Backquote')
    expect(panel).to_be_visible(timeout=15000)
    shell = floating_frame(page)
    expect(shell.locator('#connection')).to_have_text('Connected', timeout=15000)
    shell.locator('#agent-terminal').click()
    page.keyboard.type('echo FLOATING-PROOF'); page.keyboard.press('Enter')
    expect(shell.locator('#agent-terminal .xterm-screen')).to_contain_text('FLOATING-PROOF')
    shell.locator('body').evaluate('() => { window.floatIdentity = "same-shell" }')
    # It is over whatever view is open, not part of the workspace.
    expect(page.locator('#board')).to_be_visible()
    expect(page.locator('.terminal-tablist .terminal-tab')).to_have_count(0)
    # The chord hides it even from inside the shell, and brings back the same one.
    page.keyboard.press('Control+Backquote')
    expect(panel).to_be_hidden()
    page.keyboard.press('Control+Backquote')
    expect(panel).to_be_visible()
    assert floating_frame(page).locator('body').evaluate('() => window.floatIdentity') == 'same-shell'
    # Its own tabs.
    panel.get_by_role('button', name='New floating terminal').click()
    expect(panel.get_by_role('tab')).to_have_count(2, timeout=15000)
    expect(floating_frame(page).locator('#connection')).to_have_text('Connected', timeout=15000)
    # Docking moves the visible shell into the workspace.
    panel.get_by_role('button', name='Move this terminal into the workspace').click()
    expect(panel.get_by_role('tab')).to_have_count(1)
    expect(page.locator('.terminal-tablist .terminal-tab')).to_have_count(1)
    # Reload: the floating shell is remembered.
    page.reload()
    page.keyboard.press('Control+Backquote')
    expect(panel).to_be_visible()
    expect(floating_frame(page).locator('#agent-terminal .xterm-screen')).to_contain_text('FLOATING-PROOF', timeout=15000)
    assert not errors, errors


@pytest.mark.parametrize('page', [PHONE], indirect=True)
def test_floating_terminal_is_a_bottom_sheet_on_a_phone(page, real_terminal):
    t = real_terminal
    page.goto(t['url'] + '/#sessions')
    page.keyboard.press('Control+Backquote')
    panel = page.locator('#floating-terminal')
    expect(panel).to_be_visible(timeout=15000)
    expect(floating_frame(page).locator('#connection')).to_have_text('Connected', timeout=15000)
    box = panel.bounding_box()
    assert box['x'] <= 1 and box['width'] >= PHONE['width'] - 2, box
    assert abs(box['y'] + box['height'] - PHONE['height']) <= 2, box
    panel.get_by_role('button', name='Hide the floating terminal').click()
    expect(panel).to_be_hidden()


def test_find_in_scrollback_with_regex_case_and_navigation(page, real_terminal):
    t = real_terminal
    page.goto(t['url'] + '/#sessions')
    attach(page, 'Real terminal'); f = frame(page, t['id']); ready(f)
    f.locator('#agent-terminal').click()
    # The command line itself must not contain the words searched for.
    page.keyboard.type("printf '%s-%d\\n' alpha 1 beta 2 ALPHA 3 alpha 4"); page.keyboard.press('Enter')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('alpha-4')
    page.keyboard.press('Control+f')
    box = f.locator('#search-input')
    expect(box).to_be_focused()
    box.fill('alpha-')
    # Case-insensitive by default: ALPHA-3 counts.
    expect(f.locator('#matches')).to_have_text('1 / 3')
    f.get_by_role('button', name='Match case').click()
    expect(f.get_by_role('button', name='Match case')).to_have_attribute('aria-pressed', 'true')
    expect(f.locator('#matches')).to_contain_text(' / 2')
    first = f.locator('#matches').inner_text()
    box.focus(); page.keyboard.press('Enter')
    expect(f.locator('#matches')).not_to_have_text(first)
    page.keyboard.press('Shift+Enter')
    expect(f.locator('#matches')).to_have_text(first)
    f.get_by_role('button', name='Regular expression').click()
    box.fill('alpha-[14]$')
    expect(f.locator('#matches')).to_contain_text(' / 2')
    box.fill('alpha-[')
    expect(f.locator('#matches')).to_have_text('Not a valid regular expression')
    expect(box).to_have_attribute('aria-invalid', 'true')
    # The switches are remembered as this person's defaults.
    page.wait_for_timeout(600)
    stored = json.load(urllib.request.urlopen(t['url'] + '/api/ui/prefs', timeout=10))['prefs']['terminal']
    assert stored['findCase'] is True and stored['findRegex'] is True, stored
    page.keyboard.press('Escape')
    expect(f.locator('#searchbar')).to_have_count(0)
    expect(f.locator('#agent-terminal .xterm-helper-textarea')).to_be_focused()


@pytest.mark.parametrize('page', [PHONE], indirect=True)
def test_find_from_the_phone_key_bar(page, real_terminal):
    t = real_terminal
    page.goto(t['url'] + '/#sessions')
    attach(page, 'Real terminal'); f = frame(page, t['id']); ready(f)
    f.locator('#agent-terminal').click()
    page.keyboard.type('echo PHONE-FIND-ME'); page.keyboard.press('Enter')
    f.locator('[data-terminal-key="find"]').click()
    f.locator('#search-input').fill('PHONE-FIND')
    expect(f.locator('#matches')).to_contain_text(' / 2')
    bar = f.locator('#searchbar').bounding_box()
    assert bar['width'] <= PHONE['width'] + 1, bar


def test_osc52_copy_from_a_program(page, real_terminal):
    t = real_terminal
    page.context.grant_permissions(['clipboard-read', 'clipboard-write'], origin=t['url'])
    page.goto(t['url'] + '/#sessions')
    attach(page, 'Real terminal'); f = frame(page, t['id']); ready(f)
    f.locator('#agent-terminal').click()
    # Every Lectern terminal is tmux; tmux sends its own copies to the outer
    # terminal with OSC 52 (set-buffer -w is the same path as copy mode).
    page.keyboard.type('tmux set-buffer -w copied-by-osc52'); page.keyboard.press('Enter')
    expect(f.locator('#notice')).to_contain_text('copied 15 characters')
    assert f.locator('body').evaluate('() => navigator.clipboard.readText()') == 'copied-by-osc52'
    # Switched off in Settings, a program's copy is ignored.
    f.locator('#terminal-tools-summary').click()
    f.get_by_role('button', name='Appearance').click()
    f.get_by_role('checkbox', name='Let programs copy to the clipboard').uncheck()
    f.locator('#settings-dialog [data-close]').click()
    f.locator('body').evaluate('() => navigator.clipboard.writeText("unchanged")')
    f.locator('#agent-terminal').click()
    page.keyboard.type('tmux set-buffer -w second-copy; echo SECOND-DONE'); page.keyboard.press('Enter')
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('SECOND-DONE')
    page.wait_for_timeout(300)
    assert f.locator('body').evaluate('() => navigator.clipboard.readText()') == 'unchanged'


def test_quick_commands_follow_the_person_and_run_from_the_key(page, real_terminal):
    t = real_terminal
    page.goto(t['url'] + '/#targets')
    page.locator('[data-settings="workspace"]').click()
    page.get_by_role('textbox', name='New command text').fill('echo QUICK-$((6*7))')
    page.get_by_role('textbox', name='New command label').fill('Answer')
    page.locator('.quick-add').get_by_role('button', name='Add').click()
    expect(page.locator('.quick-list .quick-text').last).to_have_value('echo QUICK-$((6*7))')
    page.wait_for_timeout(600)
    stored = json.load(urllib.request.urlopen(t['url'] + '/api/ui/prefs', timeout=10))['prefs']['quick-commands']
    assert stored[-1]['text'] == 'echo QUICK-$((6*7))' and stored[-1]['label'] == 'Answer', stored
    attach(page, 'Real terminal'); f = frame(page, t['id']); ready(f)
    f.locator('#agent-terminal').click()
    page.keyboard.press('Control+Shift+Space')
    dialog = f.locator('#snippets-dialog')
    expect(dialog).to_be_visible()
    dialog.locator('.snippet-send', has_text='echo QUICK').click()
    expect(f.locator('#agent-terminal .xterm-screen')).to_contain_text('QUICK-42', timeout=10000)
