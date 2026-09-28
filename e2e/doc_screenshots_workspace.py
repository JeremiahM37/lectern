from navigation import navigate
"""Screenshots for docs/workspace.md, taken from a real run.

Not collected by the normal suite (no test_ prefix). Run it through the
isolated runner and extract the images from its output:

    ADK_TEST_MODE=e2e ADK_TEST_E2E_ARGS="-q -s e2e/doc_screenshots_workspace.py" \\
      tools/run-isolated-tests.sh . > shots.log
    python3 e2e/doc_screenshots_workspace.py shots.log docs/media/workspace

The sandbox keeps nothing it writes, so each image is printed as one line.
"""
import base64
import sys
from pathlib import Path

from playwright.sync_api import expect
from conftest import PHONE
from test_terminal_workspace import real_terminal  # noqa: F401
from test_terminal_split import attach, frame, ready, second_session
from test_workspace_layout import drag, pane, shown


def emit(page, name, **options):
    data = page.screenshot(**options)
    print(f"\nSHOT {name} {base64.b64encode(data).decode()}", flush=True)


def type_in(page, f, text):
    f.locator('#agent-terminal').click()
    page.keyboard.type(text)
    page.keyboard.press('Enter')


def test_desktop_shots(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({'width': 1440, 'height': 900})
    (t['root'] / 'notes.md').write_text('# Plan\n\n- split panes\n- themes\n')
    second = second_session(t, 'API server', 'api-server')
    page.goto(t['url'] + '/#sessions')
    attach(page, 'Real terminal'); one = frame(page, t['id']); ready(one)
    type_in(page, one, "printf '\\e[1;32m%s\\e[0m\\n' 'build ok' 'tests: 42 passed'; ls")
    attach(page, 'API server'); two = frame(page, second['id']); ready(two)
    type_in(page, two, "printf 'GET /health 200\\nGET /api/tasks 200\\nPOST /api/tasks 201\\n'")
    # Terminal + chat + changes in one tree.
    page.get_by_role('tab', name='Real terminal', exact=True).click()
    page.locator('.terminal-actions > summary').click()
    page.get_by_role('menuitem', name='Open chat beside').click()
    expect(page.locator('.terminal-tabpanel[data-pane-kind="chat"]')).to_be_visible()
    page.get_by_role('tab', name='Real terminal', exact=True).click()
    page.locator('.terminal-actions > summary').click()
    page.get_by_role('menuitem', name='Open changes beside').click()
    diff = page.locator('.terminal-tabpanel[data-pane-kind="diff"]')
    expect(diff).to_be_visible()
    chat_pane = page.locator('.terminal-tabpanel[data-pane-kind="chat"]').bounding_box()
    # The changes pane goes under the chat.
    drag(page, page.get_by_role('tab').filter(has=page.locator('.ws-tab-icon')).last,
         chat_pane['x'] + chat_pane['width'] / 2, chat_pane['y'] + chat_pane['height'] - 20)
    page.wait_for_timeout(800)
    emit(page, 'desktop-split-terminal-chat-diff.png')
    # A drag in progress shows where the pane will land.
    area = pane(page, t['id']).bounding_box()
    source = page.get_by_role('tab', name='API server', exact=True).bounding_box()
    page.mouse.move(source['x'] + 20, source['y'] + 10); page.mouse.down()
    page.mouse.move(source['x'] + 40, source['y'] + 30, steps=3)
    page.mouse.move(area['x'] + area['width'] / 2, area['y'] + area['height'] - 30, steps=8)
    page.wait_for_timeout(300)
    emit(page, 'desktop-drag-to-split.png')
    page.mouse.up()
    page.wait_for_timeout(500)
    # Recent-tab switcher.
    page.get_by_role('tab', name='API server', exact=True).focus()
    page.keyboard.down('Alt'); page.keyboard.press('Backquote')
    page.wait_for_timeout(200)
    emit(page, 'desktop-recent-tabs.png')
    page.keyboard.press('Escape'); page.keyboard.up('Alt')
    # Find in scrollback.
    one = frame(page, t['id'])
    one.locator('#agent-terminal').click()
    page.keyboard.press('Control+f')
    one.locator('#search-input').fill('passed')
    one.get_by_role('button', name='Regular expression').click()
    one.locator('#search-input').fill('tests: \\d+')
    page.wait_for_timeout(300)
    emit(page, 'desktop-terminal-find.png')
    page.keyboard.press('Escape')
    # Layouts menu.
    page.locator('.ws-layouts > summary').click()
    page.get_by_role('textbox', name='Layout name').fill('Review setup')
    page.get_by_role('button', name='Save current layout').click()
    page.locator('.ws-layouts > summary').click()
    page.wait_for_timeout(200)
    emit(page, 'desktop-saved-layouts.png')
    page.keyboard.press('Escape')
    # Floating terminal over the board.
    navigate(page, "tasks")
    page.keyboard.press('Control+Backquote')
    float_frame = page.frame_locator('#floating-terminal .floating-frame:not([hidden]) iframe')
    expect(float_frame.locator('#connection')).to_have_text('Connected', timeout=15000)
    float_frame.locator('#agent-terminal').click()
    page.keyboard.type('git status --short'); page.keyboard.press('Enter')
    page.wait_for_timeout(500)
    emit(page, 'desktop-floating-terminal.png')
    page.keyboard.press('Control+Backquote')
    # Command palette with shortcuts.
    page.locator('body').click(position={'x': 700, 'y': 400})
    page.keyboard.press('Control+k')
    page.get_by_role('combobox', name='Search sessions, tasks, and actions').fill('theme')
    page.wait_for_timeout(300)
    emit(page, 'desktop-palette.png')
    page.keyboard.press('Escape')
    # Settings: search, shortcuts with a conflict, workspace & terminal, light theme.
    page.goto(t['url'] + '/#targets')
    page.get_by_role('combobox', name='Search settings').fill('split')
    page.wait_for_timeout(200)
    emit(page, 'desktop-settings-search.png')
    page.get_by_role('combobox', name='Search settings').fill('')
    page.locator('[data-settings="shortcuts"]').click()
    page.get_by_role('searchbox', name='Search shortcuts').fill('theme')
    page.get_by_role('button', name='Add a key for Toggle light and dark theme').click()
    page.keyboard.press('Control+k')
    emit(page, 'desktop-shortcuts-conflict.png')
    page.get_by_role('button', name='Cancel').click()
    page.locator('[data-settings="workspace"]').click()
    page.locator('[data-setting="workspace.terminalTheme"]').scroll_into_view_if_needed()
    emit(page, 'desktop-settings-workspace.png')
    page.locator('[data-settings="appearance"]').click()
    page.locator('.segmented label', has_text='Light').click()
    page.wait_for_timeout(300)
    emit(page, 'desktop-light-appearance.png')
    page.locator('.tab[data-tab="sessions"]').click()
    page.wait_for_timeout(500)
    emit(page, 'desktop-light-sessions.png')
    navigate(page, "terminals")
    page.wait_for_timeout(800)
    emit(page, 'desktop-light-workspace.png')
    navigate(page, "tasks")
    page.wait_for_timeout(300)
    emit(page, 'desktop-light-board.png')


def test_phone_shots(page, real_terminal):
    t = real_terminal
    page.set_viewport_size(PHONE)
    second = second_session(t, 'API server', 'api-server')
    page.goto(t['url'] + '/#sessions')
    attach(page, 'Real terminal'); one = frame(page, t['id']); ready(one)
    type_in(page, one, "printf 'tests: 42 passed\\nlint: clean\\n'")
    page.get_by_role('button', name='Show navigation').click()
    attach(page, 'API server'); ready(frame(page, second['id']))
    page.get_by_role('tab', name='Real terminal', exact=True).click()
    page.wait_for_timeout(500)
    emit(page, 'phone-workspace.png')
    one.locator('[data-terminal-key="find"]').click()
    one.locator('#search-input').fill('passed')
    page.wait_for_timeout(300)
    emit(page, 'phone-terminal-find.png')
    one.locator('#search-close').click()
    if page.get_by_role('button', name='Show navigation').is_visible():
        page.get_by_role('button', name='Show navigation').click()
    page.keyboard.press('Control+Backquote')
    float_frame = page.frame_locator('#floating-terminal .floating-frame:not([hidden]) iframe')
    expect(float_frame.locator('#connection')).to_have_text('Connected', timeout=15000)
    page.wait_for_timeout(400)
    emit(page, 'phone-floating-terminal.png')
    page.get_by_role('button', name='Hide the floating terminal').click()
    page.goto(t['url'] + '/#targets')
    page.get_by_role('combobox', name='Search settings').fill('theme')
    page.wait_for_timeout(200)
    emit(page, 'phone-settings-search.png')
    page.get_by_role('combobox', name='Search settings').fill('')
    page.locator('[data-settings="appearance"]').click()
    page.locator('.segmented label', has_text='Light').click()
    page.wait_for_timeout(300)
    emit(page, 'phone-light-appearance.png')
    page.locator('.tab[data-tab="sessions"]').click()
    page.wait_for_timeout(500)
    emit(page, 'phone-light-sessions.png')



def test_language_shots(browser, real_terminal):
    t = real_terminal
    for locale, name in (("ja-JP", "phone-japanese-sessions.png"), ("fr-FR", "phone-french-sessions.png")):
        context = browser.new_context(viewport=PHONE, locale=locale)
        page = context.new_page()
        page.goto(t['url'] + '/#sessions')
        page.wait_for_timeout(1200)
        emit(page, name)
        context.close()
    context = browser.new_context(viewport={'width': 1440, 'height': 900}, locale='zh-CN')
    page = context.new_page()
    page.goto(t['url'] + '/#targets')
    page.locator('[data-settings="appearance"]').click()
    page.wait_for_timeout(600)
    emit(page, 'desktop-chinese-appearance.png')
    context.close()


if __name__ == '__main__':
    log, out = Path(sys.argv[1]), Path(sys.argv[2])
    out.mkdir(parents=True, exist_ok=True)
    count = 0
    for line in log.read_text(errors='replace').splitlines():
        if line.startswith('SHOT '):
            _, name, data = line.split(' ', 2)
            (out / name).write_bytes(base64.b64decode(data))
            count += 1
    print(f'wrote {count} screenshots to {out}')
