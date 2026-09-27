"""Workspace layouts on real ttyd + tmux: drag a tab to an edge to split,
nest splits, resize, maximize, restore on reload, saved layouts, the recent-tab
switcher, a chat pane beside a terminal, and the single-pane phone layout."""
import json
import urllib.request

import pytest
from playwright.sync_api import expect
from conftest import PHONE
from test_terminal_workspace import real_terminal, capture  # noqa: F401
from test_terminal_split import attach, frame, ready, second_session


def shown(page):
    return page.locator('#terminal-workspace .terminal-tabpanel:not([hidden])')


def pane(page, id):
    return page.locator(f'.terminal-tabpanel:has(iframe[src="/terminal/session/{id}?embed=1"])')


def drag(page, source, x, y):
    box = source.bounding_box()
    page.mouse.move(box['x'] + box['width'] / 2, box['y'] + box['height'] / 2)
    page.mouse.down()
    page.mouse.move(box['x'] + box['width'] / 2 + 12, box['y'] + box['height'] / 2 + 12, steps=3)
    page.mouse.move(x, y, steps=8)
    page.mouse.move(x + 1, y + 1)
    page.mouse.up()


def prefs(t):
    return json.load(urllib.request.urlopen(t['url'] + '/api/ui/prefs', timeout=10))['prefs']


def three(page, t):
    second = second_session(t)
    third = second_session(t, 'Third terminal', 'third-terminal')
    page.goto(t['url'] + '/#sessions')
    attach(page, 'Real terminal'); ready(frame(page, t['id']))
    attach(page, 'Second terminal'); ready(frame(page, second['id']))
    attach(page, 'Third terminal'); ready(frame(page, third['id']))
    return second, third


def test_drag_to_edges_nests_splits_resizes_maximizes_and_restores(page, real_terminal):
    t = real_terminal; errors = []; page.on('pageerror', lambda e: errors.append(str(e)))
    second, third = three(page, t)
    frame(page, t['id']).locator('body').evaluate('() => { window.frameIdentity = "keep-me" }')
    area = page.locator('.terminal-panels').bounding_box()
    # Real terminal onto the right edge: a side-by-side split.
    drag(page, page.get_by_role('tab', name='Real terminal', exact=True), area['x'] + area['width'] - 20, area['y'] + area['height'] / 2)
    expect(shown(page)).to_have_count(2)
    real = pane(page, t['id'])
    assert real.bounding_box()['x'] > area['x'] + area['width'] / 3, real.bounding_box()
    # Second terminal onto the bottom edge of that right pane: a nested split.
    box = real.bounding_box()
    drag(page, page.get_by_role('tab', name='Second terminal', exact=True), box['x'] + box['width'] / 2, box['y'] + box['height'] - 15)
    expect(shown(page)).to_have_count(3)
    r, s, th = real.bounding_box(), pane(page, second['id']).bounding_box(), pane(page, third['id']).bounding_box()
    assert abs(r['x'] - s['x']) <= 2 and s['y'] > r['y'] + r['height'] - 4, (r, s)
    assert th['x'] + th['width'] <= r['x'] + 3, (th, r)
    # Moving panes around never reloaded a terminal.
    assert frame(page, t['id']).locator('body').evaluate('() => window.frameIdentity') == 'keep-me'
    one = frame(page, t['id'])
    one.locator('#agent-terminal').click(); page.keyboard.type('echo AFTER-MOVES'); page.keyboard.press('Enter')
    expect(one.locator('#agent-terminal .xterm-screen')).to_contain_text('AFTER-MOVES')
    # Resize the side-by-side boundary with the pointer.
    handle = page.locator('.ws-handle-row')
    hb = handle.bounding_box(); before = pane(page, third['id']).bounding_box()['width']
    # Grab it away from the junction with the nested boundary.
    page.mouse.move(hb['x'] + hb['width'] / 2, hb['y'] + 80); page.mouse.down()
    page.mouse.move(hb['x'] - 150, hb['y'] + 80, steps=6); page.mouse.up()
    after = pane(page, third['id']).bounding_box()['width']
    assert before - after > 100, (before, after)
    # ...and with the keyboard.
    handle.focus(); page.keyboard.press('ArrowRight')
    assert pane(page, third['id']).bounding_box()['width'] > after
    # Maximize one pane, then bring the others back.
    page.locator('.ws-group-head', has=page.get_by_role('button', name='Second terminal', exact=True)).get_by_role('button', name='Maximize this pane').click()
    expect(shown(page)).to_have_count(1)
    expect(pane(page, second['id'])).to_be_visible()
    page.get_by_role('button', name='Show all panes').click()
    expect(shown(page)).to_have_count(3)
    geometry = {id: pane(page, id).bounding_box() for id in (t['id'], second['id'], third['id'])}
    # A reload restores the same arrangement, reconnected.
    page.reload()
    for id in (t['id'], second['id'], third['id']):
        ready(frame(page, id))
    expect(shown(page)).to_have_count(3)
    for id, was in geometry.items():
        now = pane(page, id).bounding_box()
        assert all(abs(now[k] - was[k]) <= 3 for k in ('x', 'y', 'width', 'height')), (id, was, now)
    expect(frame(page, t['id']).locator('#agent-terminal .xterm-screen')).to_contain_text('AFTER-MOVES')
    assert not errors, errors


def test_saved_layouts_are_kept_server_side_and_restore(page, real_terminal):
    t = real_terminal
    second, third = three(page, t)
    page.get_by_role('button', name='◫ Split').click()
    expect(shown(page)).to_have_count(2)
    page.locator('.ws-layouts > summary').click()
    page.get_by_role('textbox', name='Layout name').fill('Pair of terminals')
    page.get_by_role('button', name='Save current layout').click()
    expect(page.locator('#toasts')).to_contain_text('Layout saved: Pair of terminals')
    # Saved for the person, not the device: the server holds it.
    page.wait_for_timeout(600)
    saved = prefs(t)['workspace.layouts']
    assert saved[0]['name'] == 'Pair of terminals' and len(saved[0]['layout']['panes']) == 3, saved
    page.get_by_role('button', name='◫ Unsplit').click()
    expect(shown(page)).to_have_count(1)
    page.locator('.ws-layouts > summary').click()
    page.locator('.ws-layout-restore', has_text='Pair of terminals').click()
    expect(shown(page)).to_have_count(2)
    expect(page.locator('#toasts')).to_contain_text('Layout restored: Pair of terminals')
    page.wait_for_timeout(2000)
    # Another device with no layout of its own opens the one used last.
    other = page.context.browser.new_context(viewport={'width': 1440, 'height': 900}).new_page()
    other.goto(t['url'] + '/#terminals')
    expect(shown(other)).to_have_count(2, timeout=15000)
    other.context.close()


def test_recent_tab_switcher_and_tab_reorder(page, real_terminal):
    t = real_terminal
    second, third = three(page, t)
    # Third is showing; the most recent other tab is Second.
    page.get_by_role('tab', name='Third terminal', exact=True).focus()
    page.keyboard.down('Alt'); page.keyboard.press('Backquote')
    switcher = page.get_by_role('dialog', name='Recent tabs')
    expect(switcher).to_be_visible()
    expect(switcher.locator('li[aria-selected="true"]')).to_contain_text('Second terminal')
    page.keyboard.press('Backquote')
    expect(switcher.locator('li[aria-selected="true"]')).to_contain_text('Real terminal')
    page.keyboard.up('Alt')
    expect(switcher).to_have_count(0)
    expect(page.get_by_role('tab', name='Real terminal', exact=True)).to_have_attribute('aria-selected', 'true')
    # The same chord works from inside a terminal: the frame hands it to the app.
    one = frame(page, t['id'])
    one.locator('#agent-terminal').click()
    page.keyboard.down('Alt'); page.keyboard.press('Backquote')
    expect(switcher).to_be_visible()
    page.keyboard.up('Alt')
    expect(page.get_by_role('tab', name='Real terminal', exact=True)).to_have_attribute('aria-selected', 'false')
    # Reorder by dragging a tab in the strip.
    names = lambda: page.locator('.terminal-tablist .terminal-tab').all_inner_texts()
    assert names() == ['Real terminal', 'Second terminal', 'Third terminal'], names()
    first = page.get_by_role('tab', name='Real terminal', exact=True).bounding_box()
    drag(page, page.get_by_role('tab', name='Third terminal', exact=True), first['x'] + 6, first['y'] + first['height'] / 2)
    expect(page.locator('.terminal-tablist .terminal-tab').first).to_have_text('Third terminal')
    # And with the keyboard: Alt+Shift+Arrow moves the focused tab.
    page.get_by_role('tab', name='Third terminal', exact=True).focus()
    page.keyboard.press('Alt+Shift+ArrowRight')
    expect(page.locator('.terminal-tablist .terminal-tab').nth(1)).to_have_text('Third terminal')


def test_chat_pane_beside_a_terminal(page, real_terminal):
    t = real_terminal
    page.goto(t['url'] + '/#sessions')
    attach(page, 'Real terminal'); ready(frame(page, t['id']))
    page.locator('.terminal-actions > summary').click()
    page.get_by_role('menuitem', name='Open chat beside').click()
    chat = page.locator('.terminal-tabpanel[data-pane-kind="chat"]')
    expect(chat).to_be_visible()
    expect(chat.locator('dialog.conversation')).to_be_visible()
    term, box = pane(page, t['id']).bounding_box(), chat.bounding_box()
    assert box['x'] >= term['x'] + term['width'] - 3, (term, box)
    inner = chat.locator('dialog.conversation').bounding_box()
    assert inner['x'] >= box['x'] - 1 and inner['x'] + inner['width'] <= box['x'] + box['width'] + 1, (inner, box)
    # The terminal next to it is still usable: the chat is not a modal.
    one = frame(page, t['id'])
    one.locator('#agent-terminal').click(); page.keyboard.type('echo BESIDE-CHAT'); page.keyboard.press('Enter')
    expect(one.locator('#agent-terminal .xterm-screen')).to_contain_text('BESIDE-CHAT')
    page.get_by_role('button', name='Close terminal view: Real terminal', exact=True).click()
    expect(chat).to_be_visible()


@pytest.mark.parametrize('page', [PHONE], indirect=True)
def test_phone_collapses_the_layout_to_one_pane_with_a_switcher(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({'width': 1440, 'height': 900})
    second, third = three(page, t)
    page.get_by_role('button', name='◫ Split').click()
    expect(shown(page)).to_have_count(2)
    page.set_viewport_size(PHONE)
    expect(shown(page)).to_have_count(1)
    expect(page.locator('.ws-handle')).to_have_count(0)
    page.get_by_role('tab', name='Real terminal', exact=True).click()
    expect(pane(page, t['id'])).to_be_visible()
    box = pane(page, t['id']).bounding_box()
    assert box['width'] >= 380, box
    page.get_by_role('tab', name='Second terminal', exact=True).click()
    expect(pane(page, second['id'])).to_be_visible()
    expect(shown(page)).to_have_count(1)
