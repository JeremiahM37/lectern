"""Settings search, the app-wide light theme, accent and zoom, and remapping a
shortcut with conflict detection — all stored per person on the server."""
import json
import urllib.request

import pytest
from playwright.sync_api import expect
from conftest import PHONE
from test_terminal_workspace import real_terminal  # noqa: F401
from test_terminal_split import attach, frame, ready
from session_sheet import nav_to


def prefs(t):
    return json.load(urllib.request.urlopen(t['url'] + '/api/ui/prefs', timeout=10))['prefs']


def luminance(rgb):
    parts = [int(x) for x in rgb[rgb.index('(') + 1:rgb.index(')')].split(',')[:3]]
    return sum(parts) / 3


@pytest.mark.parametrize('width', [390, 1440])
def test_settings_search_finds_individual_controls(page, real_terminal, width):
    t = real_terminal
    page.set_viewport_size({'width': width, 'height': 900})
    page.goto(t['url'] + '/#targets')
    search = page.get_by_role('combobox', name='Search settings')
    search.fill('accent')
    hit = page.locator('#settings-hit-0')
    expect(hit.locator('strong')).to_have_text('Accent colour')
    page.keyboard.press('Enter')
    expect(page.locator('[data-settings="appearance"]')).to_have_attribute('aria-selected', 'true')
    control = page.locator('[data-setting="appearance.accent"]')
    expect(control).to_be_in_viewport()
    expect(control).to_have_class('personal-row setting-hit')
    # A control in an older section is found by its label.
    search.fill('ssh port')
    page.locator('.settings-hit', has=page.locator('strong', has_text='SSH port')).click()
    expect(page.locator('[data-settings="machines"]')).to_have_attribute('aria-selected', 'true')
    # Shortcuts are settings too.
    search.fill('split right')
    page.locator('.settings-hit', has=page.locator('strong', has_text='Split right')).first.click()
    expect(page.locator('[data-settings="shortcuts"]')).to_have_attribute('aria-selected', 'true')
    expect(page.locator('[data-shortcut="workspace.splitRight"]')).to_be_visible()
    expect(page.locator('.shortcut-table tr')).to_have_count(1)
    # The command palette reaches the same controls.
    page.keyboard.press('Escape')
    page.keyboard.press('Control+k')
    page.get_by_role('combobox', name='Search sessions, tasks, and actions').fill('zoom')
    page.locator('#command-results .command-result:has(strong:text-is("Zoom"))').click()
    expect(page.locator('[data-setting="appearance.zoom"]')).to_have_class('personal-row setting-hit')


def test_light_theme_accent_and_zoom_apply_everywhere_and_persist(page, real_terminal):
    t = real_terminal; errors = []; page.on('pageerror', lambda e: errors.append(str(e)))
    page.goto(t['url'] + '/#targets')
    page.locator('[data-settings="appearance"]').click()
    page.locator('.segmented label', has_text='Light').click()
    expect(page.locator('html')).to_have_attribute('data-theme', 'light')
    assert luminance(page.evaluate('getComputedStyle(document.body).backgroundColor')) > 200
    # The chrome, not just a panel: the top bar and tab bar are light too.
    for selector in ('#topbar', '#tabbar'):
        colour = page.locator(selector).evaluate('el => getComputedStyle(el).backgroundColor')
        assert 'rgba(0, 0, 0, 0)' in colour or luminance(colour) > 200, (selector, colour)
    page.get_by_role('button', name='Teal').click()
    assert page.evaluate("getComputedStyle(document.documentElement).getPropertyValue('--accent').trim()") == '#14b8a6'
    page.get_by_role('combobox', name='Zoom').select_option('1.25')
    assert page.evaluate("getComputedStyle(document.body).zoom") == '1.25'
    page.wait_for_timeout(600)
    assert prefs(t)['appearance'] == {'theme': 'light', 'accent': '#14b8a6', 'zoom': 1.25, 'language': '', 'preset': ''}, prefs(t)
    # Zoomed, the terminal view still starts right under the top bar and the
    # page does not scroll sideways.
    attach(page, 'Real terminal'); f = frame(page, t['id']); ready(f)
    top = page.locator('#topbar').bounding_box(); ws = page.locator('#terminal-workspace').bounding_box()
    assert abs(ws['y'] - (top['y'] + top['height'])) <= 2, (top, ws)
    assert page.evaluate('document.documentElement.scrollWidth') <= 1441
    # The terminal page follows the app theme for its own chrome.
    page.locator('.tab[data-tab="settings"]').click()
    page.locator('[data-settings="appearance"]').click()
    page.get_by_role('combobox', name='Zoom').select_option('1')
    nav_to(page, 'terminals')
    expect(f.locator('html')).to_have_attribute('data-theme', 'light')
    assert luminance(f.locator('body').evaluate('el => getComputedStyle(el).backgroundColor')) > 200
    # Reload: still light, before and after the server answers.
    page.reload()
    expect(page.locator('html')).to_have_attribute('data-theme', 'light')
    # The keyboard toggle flips it back.
    page.locator('body').click(position={'x': 5, 'y': 5})
    page.keyboard.press('Control+Shift+l')
    expect(page.locator('html')).to_have_attribute('data-theme', 'dark')
    assert not errors, errors


def test_remap_a_shortcut_with_conflict_detection(page, real_terminal):
    t = real_terminal
    page.goto(t['url'] + '/#targets')
    page.locator('[data-settings="shortcuts"]').click()
    expect(page.locator('.shortcuts-panel')).to_contain_text('actions')
    page.get_by_role('searchbox', name='Search shortcuts').fill('toggle light')
    page.get_by_role('button', name='Add a key for Toggle light and dark theme').click()
    recorder = page.get_by_role('group', name='Press the new key combination')
    expect(recorder).to_be_focused()
    page.keyboard.press('Control+k')
    # The palette did not open: the recorder owns the keys while recording.
    expect(page.get_by_role('dialog', name='Search Lectern')).to_have_count(0)
    expect(recorder.get_by_role('alert')).to_have_text('Already used by: Open command palette.')
    recorder.get_by_role('button', name='Cancel').click()
    page.get_by_role('button', name='Add a key for Toggle light and dark theme').click()
    page.keyboard.press('Control+Alt+j')
    expect(recorder).to_contain_text('Ctrl+Alt+J')
    recorder.get_by_role('button', name='Save').click()
    row = page.locator('[data-shortcut="theme.toggle"]')
    expect(row.locator('kbd')).to_have_text(['Ctrl+Shift+L', 'Ctrl+Alt+J'])
    page.wait_for_timeout(600)
    assert prefs(t)['shortcuts'] == {'theme.toggle': ['Ctrl+Shift+L', 'Ctrl+Alt+J']}, prefs(t)
    before = page.locator('html').get_attribute('data-theme')
    page.locator('body').click(position={'x': 5, 'y': 5})
    page.keyboard.press('Control+Alt+j')
    expect(page.locator('html')).not_to_have_attribute('data-theme', before)
    # Remove the default chord; reset brings both defaults back.
    row.get_by_role('button', name='Remove Ctrl+Shift+L from Toggle light and dark theme').click()
    expect(row.locator('kbd')).to_have_text(['Ctrl+Alt+J'])
    row.get_by_role('button', name='Reset Toggle light and dark theme').click()
    expect(row.locator('kbd')).to_have_text(['Ctrl+Shift+L'])
    # A deliberate clash is listed at the top until it is resolved.
    page.get_by_role('button', name='Add a key for Toggle light and dark theme').click()
    page.keyboard.press('Control+k')
    recorder.get_by_role('button', name='Use anyway').click()
    expect(page.locator('.shortcut-conflicts')).to_contain_text('Open command palette')
    page.get_by_role('button', name='Reset all').click()
    expect(page.locator('.shortcut-conflicts')).to_have_count(0)


@pytest.mark.parametrize('page', [PHONE], indirect=True)
def test_personal_settings_fit_a_phone(page, real_terminal):
    t = real_terminal
    page.goto(t['url'] + '/#targets')
    for section in ('appearance', 'workspace', 'shortcuts'):
        page.locator(f'[data-settings="{section}"]').click()
        width = page.evaluate('document.documentElement.scrollWidth')
        assert width <= PHONE['width'] + 1, (section, width)


def test_language_follows_the_browser_and_can_be_chosen(browser, real_terminal):
    t = real_terminal
    # A French browser gets French without choosing anything.
    context = browser.new_context(viewport={'width': 390, 'height': 844}, locale='fr-FR')
    page = context.new_page()
    page.goto(t['url'] + '/#sessions')
    expect(page.locator('html')).to_have_attribute('lang', 'fr')
    expect(page.locator('.tab[data-tab="sessions"]')).to_contain_text('Sessions')
    expect(page.locator('.tab[data-tab="approvals"]')).to_contain_text('Approbations')
    # Longer French labels still fit a phone on the main screens.
    for tab in ('approvals', 'sessions'):
        page.locator(f'.tab[data-tab="{tab}"]').click()
        assert page.evaluate('document.documentElement.scrollWidth') <= 391, tab
    # Choosing Japanese in Settings changes the whole app at once, and is kept.
    page.goto(t['url'] + '/#targets')
    page.locator('[data-settings="appearance"]').click()
    page.locator('[data-setting="appearance.language"] select').select_option('ja')
    expect(page.locator('html')).to_have_attribute('lang', 'ja')
    expect(page.locator('.tab[data-tab="approvals"]')).to_contain_text('承認')
    expect(page.locator('[data-settings="appearance"]')).to_have_text('外観')
    page.wait_for_timeout(600)
    assert prefs(t)['appearance']['language'] == 'ja'
    page.reload()
    expect(page.locator('.tab[data-tab="approvals"]')).to_contain_text('承認')
    # The terminal page, a separate document, follows too.
    page.goto(t['url'] + f"/terminal/session/{t['id']}")
    expect(page.locator('html')).to_have_attribute('lang', 'ja')
    expect(page.locator('#upload')).to_have_text('ファイルを添付', timeout=15000)
    context.close()
