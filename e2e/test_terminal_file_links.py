"""Paths and links agents print in the web terminal (docs/files.md).

The owner's report: Codex printed a Markdown link to a PDF outside the
workspace, its TUI hard-wrapped the path across three rows, and a click
answered "No such file or directory". These replay the exact bytes Codex
0.157 and Claude Code 2.1 emitted for that link (captured from the real CLIs
against a local stand-in model; internal/filelinks/testdata/*.bin) into a real
terminal, with a real PDF at the owner's path, and click every row."""
import os
import re
import time
from pathlib import Path

import pytest
from playwright.sync_api import expect

from test_files_workbench import CHAR_POINT as _CHAR_POINT, PHONE
from test_terminal_workspace import real_terminal, open_terminal, capture, type_command  # noqa: F401

FIXTURES = Path(__file__).resolve().parent.parent / 'internal' / 'filelinks' / 'testdata'
OWNER_DIR = Path('/home/admin/.formwork/application-testing-20260927')
OWNER_PDF = OWNER_DIR / 'Jeremiah_Mackey_Cerebras.pdf'


# xterm draws a skipped cell (a TUI moves the cursor past it) as a no-break
# space; match the text as a person reads it.
CHAR_POINT = _CHAR_POINT.replace("r.textContent.includes(text)", "r.textContent.replace(/\\u00a0/g, ' ').includes(text)").replace(
    "row.textContent.indexOf(text)", "row.textContent.replace(/\\u00a0/g, ' ').indexOf(text)")
assert CHAR_POINT != _CHAR_POINT


def click_link(page, text, offset):
    # xterm finds a link when the pointer rests on it, then shows a pointer
    # cursor; clicking before that does nothing.
    point = page.evaluate(CHAR_POINT, [text, offset])
    assert point, f'{text!r} is not on screen'
    for nudge in (0, 1, 2, 3):
        page.mouse.move(point['x'] - 3 + nudge, point['y'])
        page.mouse.move(point['x'] + nudge, point['y'])
        try:
            page.wait_for_selector('#agent-terminal .xterm-cursor-pointer', timeout=3000)
            break
        except Exception:
            continue
    page.mouse.click(point['x'], point['y'])


def one_page_pdf():
    objects = [b'<< /Type /Catalog /Pages 2 0 R >>', b'<< /Type /Pages /Kids [3 0 R] /Count 1 >>',
               b'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] >>']
    pdf = b'%PDF-1.4\n'
    offsets = []
    for i, obj in enumerate(objects, 1):
        offsets.append(len(pdf))
        pdf += f'{i} 0 obj\n'.encode() + obj + b'\nendobj\n'
    xref = len(pdf)
    pdf += b'xref\n0 4\n0000000000 65535 f \n' + b''.join(f'{v:010d} 00000 n \n'.encode() for v in offsets)
    pdf += f'trailer\n<< /Root 1 0 R /Size 4 >>\nstartxref\n{xref}\n%%EOF\n'.encode()
    return pdf


@pytest.fixture
def owner_files():
    """The owner's real path, created only inside the isolated runner, where
    /home is private to the test; on a real machine it is somebody's home."""
    if os.environ.get('HOME') != '/tmp/home' or not os.access('/home', os.W_OK):
        pytest.skip("the owner's exact path is only created inside the isolated test runner")
    OWNER_DIR.mkdir(parents=True, exist_ok=True)
    pdf = one_page_pdf()
    OWNER_PDF.write_bytes(pdf)
    (OWNER_DIR.parent / 'Résumé plan.pdf').write_bytes(pdf)
    notes = Path(os.environ['HOME']) / 'notes'
    notes.mkdir(parents=True, exist_ok=True)
    (notes / 'todo.md').write_text('# Todo\n\n- ship it\n')
    return pdf


def replay(page, name):
    type_command(page, f"clear; cat {FIXTURES / name}; echo")


def expect_owner_pdf(page):
    dialog = page.locator('#preview-dialog')
    expect(dialog).to_be_visible(timeout=20000)
    expect(page.locator('#pdf-page')).to_have_text('Page 1 of 1', timeout=20000)
    expect(page.locator('.wb-doc-path')).to_have_text(str(OWNER_PDF))
    expect(page.locator('.wb-doc-state')).to_have_text('Outside workspace · read-only')
    expect(page.locator('#file-save')).to_have_count(0)
    expect(page.locator('#file-edit')).to_have_count(0)
    page.locator('#preview-dialog [data-close]').click()
    expect(dialog).to_be_hidden()


def close_files(page):
    """The file panel narrows the terminal; close it to get the full width back."""
    page.get_by_role('button', name='Close files').click()
    expect(page.locator('#files-dialog')).to_be_hidden()
    page.locator('#agent-terminal').click()


def no_link_at(page, text, offset):
    point = page.evaluate(CHAR_POINT, [text, offset])
    assert point, text
    page.mouse.move(point['x'] - 3, point['y'])
    page.mouse.move(point['x'], point['y'])
    time.sleep(1.5)  # the existence check has answered by now
    assert page.locator('#agent-terminal .xterm-cursor-pointer').count() == 0, text


def test_codex_wrapped_link_to_a_pdf_outside_the_workspace(page, real_terminal, owner_files):
    t = real_terminal
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    open_terminal(page, t)
    rows = page.locator('#agent-terminal .xterm-rows')
    # Every row of the wrapped path is the same link. The file panel narrows
    # the terminal, so each row is clicked on a fresh replay at full width.
    for text, offset in (('(/home/admin/.formwork/', 8), ('application-testing-20260927/', 3), ('Jeremiah_Mackey_Cerebras.pdf)', 4)):
        replay(page, 'codex-markdown-link.bin')
        expect(rows).to_contain_text('Jeremiah_Mackey_Cerebras.pdf)')
        click_link(page, text, offset)
        expect_owner_pdf(page)
        close_files(page)
    replay(page, 'codex-markdown-link.bin')
    expect(rows).to_contain_text('Jeremiah_Mackey_Cerebras.pdf)')
    # Hovering one row underlines the other two as well.
    point = page.evaluate(CHAR_POINT, ['application-testing-20260927/', 5])
    page.mouse.move(point['x'] - 2, point['y'])
    page.mouse.move(point['x'], point['y'])
    expect(page.locator('#agent-terminal .term-link-underline')).to_have_count(2)
    # The web link beside it is an ordinary link, not a file.
    page.evaluate('window.__opened = []; window.open = (url) => { window.__opened.push(url); return null; }; 0')
    click_link(page, '(https://example.com/docs/page', 8)
    page.wait_for_function('window.__opened.length === 1')
    assert page.evaluate('window.__opened') == ['https://example.com/docs/page']
    assert not errors, errors


def test_claude_code_hyperlinks_to_files_outside_the_workspace(page, real_terminal, owner_files):
    t = real_terminal
    open_terminal(page, t)
    # Claude Code links the label with OSC 8 (file:///…); tmux passes it on
    # because the browser's client declares hyperlinks.
    replay(page, 'claude-markdown-link.bin')
    expect(page.locator('#agent-terminal .xterm-rows')).to_contain_text('Cerebras résumé (PDF)')
    click_link(page, 'Cerebras résumé (PDF)', 3)
    expect_owner_pdf(page)
    close_files(page)
    # Percent-encoded non-ASCII, and a bare ~/ path as the link's target.
    replay(page, 'claude-markdown-link-encoded.bin')
    expect(page.locator('#agent-terminal .xterm-rows')).to_contain_text('Todo')
    click_link(page, 'Plan2', 2)
    expect(page.locator('#pdf-page')).to_have_text('Page 1 of 1', timeout=20000)
    expect(page.locator('.wb-doc-path')).to_have_text('/home/admin/.formwork/Résumé plan.pdf')
    page.locator('#preview-dialog [data-close]').click()
    close_files(page)
    replay(page, 'claude-markdown-link-encoded.bin')
    expect(page.locator('#agent-terminal .xterm-rows')).to_contain_text('Todo')
    click_link(page, 'Todo', 2)
    expect(page.locator('.wb-doc-path')).to_have_text('~/notes/todo.md')
    expect(page.locator('#preview-body')).to_contain_text('ship it', timeout=20000)
    expect(page.locator('.wb-doc-state')).to_have_text('Outside workspace · read-only')
    page.locator('#preview-dialog [data-close]').click()


def test_bare_names_must_exist_and_misses_say_which_path(page, real_terminal, owner_files):
    t = real_terminal
    open_terminal(page, t)
    # hello.txt is in the workspace; the report is not, so it is not a link.
    type_command(page, "clear; echo 'wrote hello.txt and Missing_Report.pdf'; echo 'see /tmp/nowhere/gone.pdf'")
    expect(page.locator('#agent-terminal .xterm-rows')).to_contain_text('see /tmp/nowhere/gone.pdf')
    no_link_at(page, 'Missing_Report.pdf', 3)
    click_link(page, 'wrote hello.txt', 8)
    expect(page.locator('.wb-doc-path')).to_have_text('hello.txt')
    page.locator('#preview-dialog [data-close]').click()
    close_files(page)
    # An absolute path is offered, and a click on one that is not there says so.
    click_link(page, 'see /tmp/nowhere/gone.pdf', 8)
    expect(page.locator('#notice')).to_have_text('No file at /tmp/nowhere/gone.pdf')
    expect(page.locator('#preview-dialog')).to_be_hidden()


def test_right_click_menu_on_a_wrapped_path(page, real_terminal, owner_files):
    t = real_terminal
    open_terminal(page, t)
    replay(page, 'codex-markdown-link.bin')
    expect(page.locator('#agent-terminal .xterm-rows')).to_contain_text('Jeremiah_Mackey_Cerebras.pdf)')

    def menu_on(text, offset):
        point = page.evaluate(CHAR_POINT, [text, offset])
        page.mouse.click(point['x'], point['y'], button='right')
        menu = page.locator('#link-menu')
        expect(menu).to_be_visible()
        return menu

    menu = menu_on('application-testing-20260927/', 4)
    expect(menu.get_by_role('menuitem')).to_have_text(['Open', 'Download', 'Copy path', 'Send path to the agent'])
    expect(menu).to_contain_text(str(OWNER_PDF))
    with page.expect_download() as download:
        menu.get_by_role('menuitem', name='Download').click()
    assert Path(download.value.path()).read_bytes() == owner_files
    assert download.value.suggested_filename == 'Jeremiah_Mackey_Cerebras.pdf'
    expect(page.locator('#link-menu')).to_have_count(0)

    menu = menu_on('Jeremiah_Mackey_Cerebras.pdf)', 2)
    page.context.grant_permissions(['clipboard-read', 'clipboard-write'])
    menu.get_by_role('menuitem', name='Copy path').click()
    expect(page.locator('#notice')).to_have_text(f'Copied {OWNER_PDF}')
    assert page.evaluate('navigator.clipboard.readText()') == str(OWNER_PDF)

    menu = menu_on('(/home/admin/.formwork/', 6)
    menu.get_by_role('menuitem', name='Send path to the agent').click()
    deadline = time.monotonic() + 10
    while f"'{OWNER_PDF}'" not in capture(t) and time.monotonic() < deadline:
        time.sleep(0.1)
    assert f"'{OWNER_PDF}'" in capture(t)
    page.keyboard.press('Control+c')

    menu = menu_on('application-testing-20260927/', 4)
    menu.get_by_role('menuitem', name='Open').click()
    expect_owner_pdf(page)
    # Escape closes it, and a right-click off any link keeps the browser's own menu.
    menu = menu_on('Jeremiah_Mackey_Cerebras.pdf)', 2)
    page.keyboard.press('Escape')
    expect(page.locator('#link-menu')).to_have_count(0)
    point = page.evaluate(CHAR_POINT, ['Both are ready', 1])
    page.mouse.click(point['x'], point['y'], button='right')
    expect(page.locator('#link-menu')).to_have_count(0)


def test_phone_taps_a_wrapped_path(browser, real_terminal, owner_files):
    t = real_terminal
    context = browser.new_context(**PHONE)
    phone = context.new_page()
    phone.goto(f"{t['url']}/terminal/session/{t['id']}")
    expect(phone.locator('#connection')).to_have_text('Connected', timeout=20000)
    phone.locator('#agent-terminal').tap()
    # The owner's sample as plain rows: on a phone the first one is also
    # wrapped by the terminal, so hard and soft wraps meet in one path.
    type_command(phone, "clear; printf '%s\\n' '  • Cerebras résumé (PDF) (/home/admin/.formwork/' '    application-testing-20260927/' '    Jeremiah_Mackey_Cerebras.pdf) — emphasizes GPU'")
    expect(phone.locator('#agent-terminal .xterm-rows')).to_contain_text('Jeremiah_Mackey_Cerebras.pdf)')
    point = phone.evaluate(CHAR_POINT, ['Jeremiah_Mackey_Cerebras.pdf)', 6])
    phone.touchscreen.tap(point['x'], point['y'])
    expect(phone.locator('#pdf-page')).to_have_text('Page 1 of 1', timeout=20000)
    expect(phone.locator('.wb-doc-state')).to_have_text('Outside workspace · read-only')
    assert phone.evaluate('document.documentElement.scrollWidth <= innerWidth')
    context.close()


@pytest.mark.parametrize("theme", ["light", "dark"])
def test_link_menu_and_outside_viewer_contrast_and_open_beside(page, real_terminal, owner_files, theme):
    """The link menu and the read-only outside viewer pass the contrast audit
    in both themes; inside the workspace, Open beside opens a file pane."""
    from test_light_mode_sweep import Sweep, light
    t = real_terminal
    notes = Path(os.environ['HOME']) / 'notes' / 'todo.md'
    light(page, theme)
    sweep = Sweep(page, 'desk-terminal-links', theme)
    open_terminal(page, t)
    type_command(page, f"clear; echo 'see {notes}'")
    expect(page.locator('#agent-terminal .xterm-rows')).to_contain_text(f'see {notes}')
    point = page.evaluate(CHAR_POINT, [f'see {notes}', 8])
    page.mouse.click(point['x'], point['y'], button='right')
    expect(page.locator('#link-menu')).to_be_visible()
    sweep.check('link-menu')
    page.locator('#link-menu-open').click()
    expect(page.locator('#preview-body')).to_contain_text('ship it', timeout=20000)
    expect(page.locator('.wb-doc-state')).to_have_text('Outside workspace · read-only')
    sweep.check('outside-viewer')
    # In the workspace, the terminal is a pane; Open beside puts the file next to it.
    page.goto(f"{t['url']}/#terminals/session/{t['id']}")
    frame = page.frame_locator('#terminal-workspace iframe').first
    expect(frame.locator('#agent-terminal .xterm-screen')).to_contain_text('$', timeout=20000)
    frame.locator('#agent-terminal').click()
    type_command(page, f"clear; echo 'see {notes}'")
    expect(frame.locator('#agent-terminal .xterm-rows')).to_contain_text(f'see {notes}')
    iframe = page.locator('#terminal-workspace iframe').first.bounding_box()
    inner = page.locator('#terminal-workspace iframe').first.element_handle().content_frame()
    point = inner.evaluate(CHAR_POINT, [f'see {notes}', 8])
    page.mouse.click(iframe['x'] + point['x'], iframe['y'] + point['y'], button='right')
    expect(frame.locator('#link-menu-beside')).to_be_visible()
    frame.locator('#link-menu-beside').click()
    pane = page.locator('.terminal-tabpanel[data-pane-kind="file"]')
    expect(pane).to_contain_text('Outside workspace · read-only', timeout=20000)
    expect(pane).to_contain_text('ship it')
    sweep.done()


def test_links_win_over_a_program_that_tracks_the_mouse(page, real_terminal):
    """A full-screen agent (or tmux with mouse on) asks for mouse reports;
    a click on a detected link still opens it, and every other click still
    reaches the program."""
    import subprocess
    t = real_terminal
    log = t['root'] / 'agent-input'
    script = t['root'] / 'tracking.py'
    script.write_text(
        "import os,sys,tty\n"
        "tty.setraw(0)\n"
        "sys.stdout.write('\\x1b[?1000h\\x1b[?1003h\\x1b[?1006h\\x1b[2J\\x1b[H')\n"
        "sys.stdout.write('wrote hello.txt for you\\r\\nsee https://example.com/docs/page\\r\\nTRACKING\\r\\n')\n"
        "sys.stdout.flush()\n"
        f"f=open({str(log)!r},'ab',buffering=0)\n"
        "while True:\n b=os.read(0,1024)\n if not b: break\n f.write(b)\n")
    subprocess.run(['tmux', 'set-option', '-g', 'mouse', 'on'], env=t['env'], check=True)
    open_terminal(page, t)
    type_command(page, f'clear; python3 {script}')
    expect(page.locator('#agent-terminal .xterm-rows')).to_contain_text('TRACKING')
    page.wait_for_function("document.querySelector('#agent-terminal .xterm')?.classList.contains('enable-mouse-events')")
    page.evaluate('window.__opened = []; window.open = (url) => { window.__opened.push(url); return null; }; 0')

    def presses():
        # Button presses only (SGR ...M). A click's release (...m) arrives
        # separately and, under load, after the press was counted.
        return len(re.findall(rb'\x1b\[<0;\d+;\d+M', log.read_bytes())) if log.exists() else 0

    # A plain click is the program's.
    point = page.evaluate(CHAR_POINT, ['TRACKING', 2])
    page.mouse.click(point['x'], point['y'])
    deadline = time.monotonic() + 5
    while presses() == 0 and time.monotonic() < deadline:
        time.sleep(.1)
    assert presses() >= 1, 'the program never got its click'
    before = presses()
    # A click on a link is Lectern's: the file opens, the program sees no press.
    point = page.evaluate(CHAR_POINT, ['wrote hello.txt', 8])
    page.mouse.click(point['x'], point['y'])
    expect(page.locator('.wb-doc-path')).to_have_text('hello.txt', timeout=20000)
    page.locator('#preview-dialog [data-close]').click()
    page.get_by_role('button', name='Close files').click()
    point = page.evaluate(CHAR_POINT, ['see https://example.com/docs/page', 10])
    page.mouse.click(point['x'], point['y'])
    page.wait_for_function('window.__opened.length === 1')
    assert page.evaluate('window.__opened') == ['https://example.com/docs/page']
    time.sleep(.5)
    assert presses() == before, 'a click on a link also reached the program'
