"""The terminal's menus on a phone: bottom sheets over a dimmed page.

Tools (inside the terminal frame), the overflow ⋯ and the new-terminal caret
each open as a sheet that is wholly on screen, leave the terminal underneath
where it was, and close on a tap on the dimming, on their close button, and on
Android's Back (window.__lecternBack) before Back leaves the terminal."""
import re

import pytest
from playwright.sync_api import expect

from test_terminal_workspace import real_terminal
from test_terminal_tabs import attach, frame, ready

PHONE = {'width': 412, 'height': 839}


@pytest.fixture()
def phone(browser, real_terminal):
    t = real_terminal
    # A project makes the new-terminal caret appear.
    t['api']('/projects', {'name': 'Sheet project', 'target_id': t['target_id'],
                           'repo_path': str(t['root']), 'default_agent': 'claude'})
    with browser.new_context(viewport=PHONE, is_mobile=True, has_touch=True) as context:
        page = context.new_page()
        errors = []
        page.on('pageerror', lambda error: errors.append(str(error)))
        page.goto(t['url'] + '/#sessions')
        attach(page, 'Real terminal')
        one = frame(page, t['id'])
        ready(one)
        yield page, one
        assert not errors, errors


def boxes(one):
    """Where the terminal and its status sit, in the frame's coordinates."""
    return one.locator('body').evaluate('''() => {
      const at = s => { const e = document.querySelector(s); if (!e) return null;
        const b = e.getBoundingClientRect(); return [b.left, b.top, b.width, b.height].map(Math.round); };
      return {terminal: at('#agent-terminal'), status: at('#compact-status'), keys: at('#terminal-keybar')};
    }''')


SHEET_REPORT = '''(panel) => {
  const box = panel.getBoundingClientRect(), view = visualViewport;
  const rows = [...panel.querySelectorAll(':scope > button, :scope > a, :scope > .menu-sheet-head button')]
    .filter((row) => row.getClientRects().length);
  const clipped = rows.filter((row) => { const r = row.getBoundingClientRect();
    return r.left < box.left - 1 || r.right > box.right + 1; }).map((row) => row.textContent);
  const small = rows.filter((row) => row.getBoundingClientRect().height < 44).map((row) => row.textContent);
  // Scrolled to its end, the lowest row is wholly inside the sheet.
  panel.scrollTop = panel.scrollHeight;
  const last = rows.map((row) => row.getBoundingClientRect()).reduce((a, b) => (b.bottom > a.bottom ? b : a));
  const report = {left: box.left, right: box.right, top: box.top, bottom: box.bottom,
    width: innerWidth, height: view.height + view.offsetTop, clipped, small,
    sideways: panel.scrollWidth > panel.clientWidth + 1,
    lastInside: last.bottom <= box.bottom + 1 && last.top >= box.top - 1,
    rows: rows.length};
  panel.scrollTop = 0;
  return report;
}'''


def assert_sheet(panel, bottom=None):
    report = panel.evaluate(SHEET_REPORT)
    assert report['rows'] > 0, report
    assert report['left'] >= -1 and report['right'] <= report['width'] + 1, report
    assert report['right'] - report['left'] >= report['width'] - 2, ('a sheet spans the screen', report)
    assert report['top'] >= -1 and report['bottom'] <= (bottom or report['height']) + 1, report
    assert not report['clipped'] and not report['sideways'], report
    assert not report['small'], ('rows under 44px', report)
    assert report['lastInside'], report
    return report


def test_tools_is_a_grouped_sheet_that_closes_every_way(phone):
    page, one = phone
    before = boxes(one)
    tools = one.locator('#terminal-tools')
    summary = one.locator('#terminal-tools-summary')

    summary.tap()
    expect(tools).to_have_attribute('open', '')
    expect(tools).to_have_class(re.compile(r'\bmenu-sheet\b'))
    panel = tools.locator('.action-menu-panel')
    # It rests on the key row, which stays where it was, as does the terminal.
    assert_sheet(panel, bottom=before['keys'][1])
    assert boxes(one) == before, (before, boxes(one))
    expect(tools.locator('.menu-sheet-head')).to_contain_text('Tools')
    for heading in ('Files', 'Text & replies', 'Conversations', 'This session', 'Keyboard & settings'):
        expect(tools.locator('.menu-sheet-group', has_text=heading)).to_be_visible()
    # Open in terminal reads as a row like the others, not a filled block.
    desktop = one.locator('#compact-desktop')
    expect(desktop).to_be_visible()
    assert desktop.evaluate('e => getComputedStyle(e).alignItems') == 'center'
    # The page around the frame dims with it.
    expect(page.locator('.terminal-frame-scrim')).to_be_visible()

    # A tap on the dimming over the terminal closes it.
    owner = one.owner.bounding_box()
    page.touchscreen.tap(owner['x'] + 200, owner['y'] + 8)
    expect(tools).not_to_have_attribute('open', '')
    expect(page.locator('.terminal-frame-scrim')).to_have_count(0)

    # So does a tap on the page's dimming outside the frame (the tab names).
    summary.tap()
    expect(tools).to_have_attribute('open', '')
    tabs = page.locator('.terminal-tablist').bounding_box()
    page.touchscreen.tap(tabs['x'] + tabs['width'] - 20, tabs['y'] + tabs['height'] / 2)
    expect(tools).not_to_have_attribute('open', '')

    # Its close button, and a second tap on Tools itself.
    summary.tap()
    tools.locator('.menu-sheet-close').tap()
    expect(tools).not_to_have_attribute('open', '')
    summary.tap()
    expect(tools).to_have_attribute('open', '')
    summary.tap()
    expect(tools).not_to_have_attribute('open', '')

    # Android's Back closes the sheet first and stays on the terminal.
    summary.tap()
    expect(tools).to_have_attribute('open', '')
    hash_before = page.evaluate('location.hash')
    assert page.evaluate('window.__lecternBack()') is True
    expect(tools).not_to_have_attribute('open', '')
    expect(page.locator('.terminal-frame-scrim')).to_have_count(0)
    assert page.evaluate('location.hash') == hash_before
    assert boxes(one) == before, (before, boxes(one))

    # An item still does its job from the sheet.
    summary.tap()
    one.locator('#compose').tap()
    expect(one.locator('#compose-dialog')).to_be_visible()
    expect(tools).not_to_have_attribute('open', '')


@pytest.mark.parametrize('menu', ['.terminal-actions', '.terminal-tabbar .terminal-new-machines'])
def test_tab_bar_menus_are_sheets_that_close_every_way(phone, menu):
    page, one = phone
    before = boxes(one)
    details = page.locator(menu)
    summary = details.locator(':scope > summary')
    panel = details.locator(':scope > .terminal-actions-panel')

    summary.tap()
    expect(details).to_have_attribute('open', '')
    expect(details).to_have_class(re.compile(r'\bmenu-sheet\b'))
    report = assert_sheet(panel)
    assert report['bottom'] >= PHONE['height'] - 2, ('rests on the bottom edge', report)
    expect(details.locator('.menu-sheet-head')).to_be_visible()
    assert boxes(one) == before, (before, boxes(one))

    # The dimming covers the terminal; a tap there closes the sheet without
    # reaching the terminal underneath.
    page.touchscreen.tap(200, report['top'] / 2)
    expect(details).not_to_have_attribute('open', '')

    details.locator(':scope > summary').tap()
    details.locator('.menu-sheet-close').tap()
    expect(details).not_to_have_attribute('open', '')

    summary.tap()
    summary.tap()
    expect(details).not_to_have_attribute('open', '')

    summary.tap()
    expect(details).to_have_attribute('open', '')
    hash_before = page.evaluate('location.hash')
    assert page.evaluate('window.__lecternBack()') is True
    expect(details).not_to_have_attribute('open', '')
    assert page.evaluate('location.hash') == hash_before


def test_desk_menus_stay_dropdowns(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({'width': 1440, 'height': 900})
    page.goto(t['url'] + '/#sessions')
    attach(page, 'Real terminal')
    one = frame(page, t['id'])
    ready(one)
    page.locator('.terminal-actions > summary').click()
    expect(page.locator('.terminal-actions')).to_have_attribute('open', '')
    expect(page.locator('.terminal-actions')).not_to_have_class(re.compile(r'\bmenu-sheet\b'))
    expect(page.locator('.terminal-actions .menu-sheet-head')).to_be_hidden()
    assert page.locator('.terminal-actions-panel').bounding_box()['width'] <= 260
    page.keyboard.press('Escape')
    one.locator('#terminal-tools-summary').click()
    tools = one.locator('#terminal-tools')
    expect(tools).to_have_attribute('open', '')
    expect(tools).not_to_have_class(re.compile(r'\bmenu-sheet\b'))
    expect(tools.locator('.menu-sheet-group').first).to_be_hidden()
    assert tools.locator('.action-menu-panel').bounding_box()['width'] <= 260
    expect(page.locator('.terminal-frame-scrim')).to_have_count(0)
