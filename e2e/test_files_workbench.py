"""Workspace files beside a real terminal: explorer, editor, viewers, Quick Open,
search, terminal path links and deep links, on desktop and phone widths.

Every byte is read and written on a real local target through the file API; the
assertions check the files on disk, not only what the page shows."""
import base64
import io
import json
import re
import subprocess
import time
import urllib.request
import zipfile
from pathlib import Path

import pytest
from playwright.sync_api import expect

from test_terminal_workspace import real_terminal, open_terminal, capture, type_command  # noqa: F401

PHONE = dict(viewport={'width': 390, 'height': 844}, is_mobile=True, has_touch=True, device_scale_factor=2)


def git(root, *args):
    subprocess.run(['git', '-C', str(root), '-c', 'user.name=t', '-c', 'user.email=t@example.com', *args],
                   check=True, capture_output=True)


def wait_for(predicate, timeout=10.0, message='condition not met'):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        if predicate():
            return
        time.sleep(0.1)
    raise AssertionError(message)


def seed(root: Path):
    (root / 'src').mkdir()
    (root / 'src' / 'app.py').write_text('import os\n\n\ndef main():\n    return "needle"\n')
    (root / 'file2.txt').write_text('two\n')
    (root / 'file10.txt').write_text('ten\n')
    (root / '.gitignore').write_text('build/\n')
    (root / 'build').mkdir()
    (root / 'build' / 'out.js').write_text('generated needle\n')
    git(root, 'add', '.')
    git(root, 'commit', '-qm', 'seed')
    (root / 'src' / 'app.py').write_text('import os\n\n\ndef main():\n    return "needle"  # changed\n')
    (root / 'notes.md').write_text('untracked\n')


def monaco_ready(page):
    expect(page.locator('#preview-body .monaco-editor')).to_be_visible(timeout=20000)
    page.wait_for_selector('#preview-body .wb-monaco[data-ready]', timeout=20000)


def row(page, name):
    return page.locator(f'.wb-row[data-path="{name}"]')


def test_explorer_edit_save_conflict_and_file_operations(page, real_terminal):
    t = real_terminal
    root = t['root']
    seed(root)
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    page.on('dialog', lambda d: d.accept())
    open_terminal(page, t)
    page.locator('#files').click()
    expect(page.locator('#files-dialog')).to_be_visible()
    # Natural order, folders first, and git colours.
    expect(row(page, 'src')).to_be_visible()
    names = page.locator('#file-list > .wb-row').evaluate_all('rows => rows.map(r => r.dataset.path)')
    assert names.index('src') < names.index('file2.txt') < names.index('file10.txt'), names
    expect(row(page, 'notes.md')).to_have_class(re.compile('git-untracked'))
    expect(row(page, 'src')).to_have_class(re.compile('git-modified'))
    expect(row(page, 'build')).to_have_class(re.compile('git-ignored'))
    # The explorer follows the disk without a manual refresh.
    (root / 'appeared.txt').write_text('new\n')
    expect(row(page, 'appeared.txt')).to_be_visible(timeout=8000)
    (root / 'appeared.txt').unlink()
    expect(row(page, 'appeared.txt')).to_have_count(0, timeout=8000)

    # Open, edit in the full editor and save: the bytes land on disk.
    row(page, 'src').get_by_role('button', name='src', exact=True).click()
    page.get_by_role('button', name='app.py', exact=True).click()
    monaco_ready(page)
    expect(page.locator('#preview-body')).to_contain_text('needle')
    page.locator('#preview-body .view-lines').click()
    page.keyboard.press('Control+End')
    page.keyboard.type('\n# saved from the browser')
    expect(page.locator('.wb-doc-state')).to_have_text('Unsaved')
    page.keyboard.press('Control+s')
    expect(page.locator('.wb-banner')).to_have_count(0)
    expect(page.locator('.wb-doc-state')).to_have_text('')
    wait_for(lambda: (root / 'src' / 'app.py').read_text().rstrip().endswith('# saved from the browser'),
             message='save not on disk: ' + repr((root / 'src' / 'app.py').read_text()))

    # Someone else writes the file while it has unsaved edits: nothing is
    # overwritten until the person chooses.
    page.keyboard.type('\n# mine')
    (root / 'src' / 'app.py').write_text('agent rewrote this\n')
    page.locator('#file-save').click()
    banner = page.locator('.wb-banner')
    expect(banner).to_be_visible(timeout=10000)
    assert (root / 'src' / 'app.py').read_text() == 'agent rewrote this\n'
    banner.get_by_role('button', name='Keep mine').click()
    wait_for(lambda: (root / 'src' / 'app.py').read_text().endswith('# mine'), message='keep mine not saved')
    expect(banner).to_have_count(0)
    # An unedited file follows the disk by itself.
    (root / 'src' / 'app.py').write_text('changed by the agent\n')
    expect(page.locator('#preview-body .view-lines')).to_contain_text('changed by the agent', timeout=10000)

    # Autosave writes after a pause in typing.
    page.locator('.wb-doc-menu > summary').click()
    page.locator('.wb-doc-menu').get_by_text('Autosave').click()
    page.keyboard.press('Escape')
    page.locator('#preview-body .view-lines').click()
    page.keyboard.press('Control+End')
    page.keyboard.type(' autosaved')
    wait_for(lambda: 'autosaved' in (root / 'src' / 'app.py').read_text(), timeout=10, message='autosave did not write')
    page.locator('.wb-doc-menu > summary').click()
    page.locator('.wb-doc-menu').get_by_text('Autosave').click()
    page.keyboard.press('Escape')

    # Create, rename, move by dragging, and delete — each checked on disk.
    # New entries go in the selected file's folder: select one at the root.
    row(page, 'file2.txt').get_by_role('button', name='file2.txt', exact=True).click()
    page.get_by_role('button', name='New folder').click()
    page.get_by_label('New folder name').fill('docs')
    page.keyboard.press('Enter')
    wait_for(lambda: (root / 'docs').is_dir(), message='folder not created')
    page.get_by_role('button', name='New file').click()
    page.get_by_label('New file name').fill('draft.md')
    page.keyboard.press('Enter')
    wait_for(lambda: (root / 'draft.md').is_file(), message='file not created')
    expect(page.locator('.wb-tab.active')).to_contain_text('draft.md')
    row(page, 'draft.md').get_by_role('button', name='draft.md', exact=True).press('F2')
    page.get_by_label('New name for draft.md').fill('plan.md')
    page.keyboard.press('Enter')
    wait_for(lambda: (root / 'plan.md').is_file() and not (root / 'draft.md').exists(), message='rename failed')
    expect(page.locator('.wb-tab.active')).to_contain_text('plan.md')
    row(page, 'plan.md').drag_to(row(page, 'docs'))
    wait_for(lambda: (root / 'docs' / 'plan.md').is_file(), message='drag move failed')
    row(page, 'file10.txt').get_by_role('button', name='Actions for file10.txt').click()
    page.get_by_role('menuitem', name='Delete').click()
    wait_for(lambda: not (root / 'file10.txt').exists(), message='delete failed')

    # Folder download is a zip of the folder's files.
    row(page, 'src').get_by_role('button', name='Actions for src').click()
    with page.expect_download() as download:
        page.get_by_role('menuitem', name='Download folder (.zip)').click()
    archive = zipfile.ZipFile(io.BytesIO(Path(download.value.path()).read_bytes()))
    assert archive.namelist() == ['app.py']

    # Dragging a file onto the terminal pastes its path without pressing Enter.
    before = capture(t)
    page.evaluate('''([path]) => {
      const data = new DataTransfer(); data.setData('application/x-lectern-path', path);
      const target = document.querySelector('#agent-terminal .xterm-screen');
      for (const type of ['dragover', 'drop']) target.dispatchEvent(new DragEvent(type, {bubbles: true, cancelable: true, dataTransfer: data}));
    }''', ['src/app.py'])
    wait_for(lambda: str(root / 'src' / 'app.py') in capture(t), message='path not inserted')
    assert capture(t).count('\n') <= before.count('\n') + 1
    assert not errors, errors


QUICK_OPEN_TIMING = '''async (query) => {
  const frame = () => new Promise((resolve) => requestAnimationFrame(resolve));
  const started = performance.now();
  window.dispatchEvent(new KeyboardEvent('keydown', {key: 'p', code: 'KeyP', ctrlKey: true, bubbles: true, cancelable: true}));
  let input;
  while (!(input = document.querySelector('#quick-open-input'))) await frame();
  Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set.call(input, query);
  input.dispatchEvent(new Event('input', {bubbles: true}));
  while (!(document.querySelector('#quick-open-0')?.textContent || '').includes(query)) await frame();
  return performance.now() - started;
}'''


def test_quick_open_ranks_five_thousand_files_and_search_opens_at_the_line(page, real_terminal):
    t = real_terminal
    root = t['root']
    seed(root)
    for i in range(5000):
        folder = root / 'pkg' / f'mod{i % 50:02d}'
        folder.mkdir(parents=True, exist_ok=True)
        (folder / f'file{i:04d}.go').write_text('package mod\n')
    git(root, 'add', '.')
    open_terminal(page, t)
    page.locator('#files').click()
    expect(row(page, 'src')).to_be_visible()
    # In the terminal Ctrl+P stays the shell's; Ctrl+Alt+P opens Go to file.
    page.locator('#agent-terminal').click()
    page.keyboard.press('Control+p')
    page.wait_for_timeout(300)
    expect(page.locator('#quick-open')).to_have_count(0)
    page.keyboard.press('Control+Alt+P')
    expect(page.locator('#quick-open-input')).to_be_focused()
    expect(page.locator('.wb-quick-foot')).to_contain_text('5,0', timeout=20000)
    page.keyboard.press('Escape')
    expect(page.locator('#quick-open')).to_have_count(0)
    # A warm index answers in well under 200 ms, keypress to rendered result.
    row(page, 'src').get_by_role('button', name='src', exact=True).focus()
    timings = []
    for n in ('4999', '0042', '2500'):
        timings.append(page.evaluate(QUICK_OPEN_TIMING, f'file{n}'))
        page.keyboard.press('Escape')
    print('Quick Open keypress-to-result (ms):', [round(v, 1) for v in timings])
    assert max(timings) < 200, timings
    # Ranking: a name beats scattered letters; ignored files form their own section.
    page.keyboard.press('Control+p')
    page.locator('#quick-open-input').fill('app')
    expect(page.locator('#quick-open-0 .wb-quick-name')).to_have_text('app.py')
    page.locator('#quick-open-input').fill('out.js')
    expect(page.locator('#quick-open .wb-section')).to_have_text('Ignored by .gitignore')
    expect(page.locator('.wb-quick-item.ignored').first).to_contain_text('out.js')
    # name:line opens at that line.
    page.locator('#quick-open-input').fill('app.py:4')
    page.keyboard.press('Enter')
    monaco_ready(page)
    expect(page.locator('#preview-body .wb-target-line')).to_have_count(1)
    assert '#L4' in page.url and 'open=src%2Fapp.py' in page.url

    # Project search runs on the target; ignored files only when asked.
    page.locator('#files-search-tab').click()
    page.locator('#files-search-input').fill('needle')
    expect(page.locator('.wb-search-summary')).to_have_text(re.compile(r'^1 match in 1 file'), timeout=15000)
    page.get_by_label('Ignored', exact=True).check()
    expect(page.locator('.wb-search-summary')).to_have_text(re.compile(r'^2 matches in 2 files'), timeout=15000)
    page.get_by_label('Ignored', exact=True).uncheck()
    page.get_by_label('Regular expression').click()
    page.locator('#files-search-input').fill('ne+dle"')
    expect(page.locator('.wb-search-summary')).to_have_text(re.compile(r'^1 match'), timeout=15000)
    page.get_by_label('Regular expression').click()
    page.get_by_label('Whole word').click()
    page.locator('#files-search-input').fill('needl')
    expect(page.locator('.wb-search-summary')).to_have_text(re.compile(r'^0 matches'), timeout=15000)
    page.get_by_label('Whole word').click()
    page.locator('#files-search-input').fill('return')
    page.locator('.wb-search-hit').first.click()
    expect(page.locator('#preview-body .wb-target-line')).to_have_count(1)
    assert '#L5' in page.url


def test_viewers_render_markdown_html_csv_notebook_images_and_pdf(page, real_terminal):
    t = real_terminal
    root = t['root']
    png = base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aF9sAAAAASUVORK5CYII=')
    (root / 'pixel.png').write_bytes(png)
    (root / 'README.md').write_text('---\ntitle: Files guide\n---\n# Overview\n\nSee [the app](src/app.py) and [[notes]].\n\n'
                                    '| Name | Size |\n| --- | --- |\n| a | 1 |\n\n- [x] done\n\n![pixel](pixel.png)\n\n'
                                    '```mermaid\nflowchart LR\n  Start --> Finish\n```\n\n## Details\n\nText.\n')
    (root / 'notes.md').write_text('# Notes\n')
    (root / 'src').mkdir()
    (root / 'src' / 'app.py').write_text('print(1)\n')
    (root / 'page.html').write_text('<h1>Static</h1><div id="out">not run</div><script>document.getElementById("out").textContent="script ran"</script>')
    (root / 'data.csv').write_text('name,count\n"Smith, J",10\nAda,2\n')
    (root / 'flow.mmd').write_text('sequenceDiagram\n  Alice->>Bob: Hello\n')
    (root / 'analysis.ipynb').write_text(json.dumps({'nbformat': 4, 'metadata': {}, 'cells': [
        {'cell_type': 'markdown', 'source': ['# Result']},
        {'cell_type': 'code', 'execution_count': 1, 'source': 'print(2)', 'outputs': [
            {'output_type': 'stream', 'name': 'stdout', 'text': ['2\n']},
            {'output_type': 'display_data', 'data': {'image/png': base64.b64encode(png).decode()}}]}]}))
    objects = [b'<< /Type /Catalog /Pages 2 0 R >>', b'<< /Type /Pages /Kids [3 0 R 4 0 R] /Count 2 >>',
               b'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] >>', b'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] >>']
    pdf = b'%PDF-1.4\n'
    offsets = []
    for i, obj in enumerate(objects, 1):
        offsets.append(len(pdf))
        pdf += f'{i} 0 obj\n'.encode() + obj + b'\nendobj\n'
    xref = len(pdf)
    pdf += b'xref\n0 5\n0000000000 65535 f \n' + b''.join(f'{v:010d} 00000 n \n'.encode() for v in offsets)
    pdf += f'trailer\n<< /Root 1 0 R /Size 5 >>\nstartxref\n{xref}\n%%EOF\n'.encode()
    (root / 'two.pdf').write_bytes(pdf)
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    open_terminal(page, t)
    page.locator('#files').click()

    def open_file(name):
        page.get_by_role('button', name=name, exact=True).click()

    # Markdown: front matter, table, task list, local image, diagram, contents.
    open_file('README.md')
    body = page.locator('#preview-body')
    expect(body.locator('.wb-front-matter')).to_contain_text('Files guide')
    expect(body.locator('.wb-md-block table')).to_contain_text('Name')
    expect(body.locator('input[type=checkbox]')).to_be_checked()
    expect(body.locator('.wb-mermaid svg')).to_be_visible(timeout=20000)
    page.wait_for_function("document.querySelector('#preview-body img[alt=pixel]')?.naturalWidth === 1")
    expect(body.locator('.wb-toc')).to_contain_text('Details')
    # Split editing: the toolbar edits the source and the preview follows.
    page.get_by_role('button', name='Split').click()
    monaco_ready(page)
    page.locator('#preview-body .view-lines').click()
    page.keyboard.press('Control+End')
    page.get_by_role('button', name='Bold').click()
    expect(body.locator('.wb-split-preview strong')).to_have_text('bold text')
    page.locator('#file-save').click()
    wait_for(lambda: '**bold text**' in (root / 'README.md').read_text(), message='markdown save')
    # Links inside the document open workspace files; wiki links resolve by name.
    page.get_by_role('button', name='Preview').click()
    body.get_by_role('link', name='notes').click()
    expect(page.locator('.wb-tab.active')).to_contain_text('notes.md')

    # HTML is sandboxed: scripts off by default, and on only when asked.
    open_file('page.html')
    frame = page.frame_locator('#preview-body iframe.wb-html')
    expect(frame.locator('#out')).to_have_text('not run')
    assert page.locator('#preview-body iframe.wb-html').get_attribute('sandbox') == ''
    page.locator('.wb-doc-menu > summary').click()
    page.get_by_role('menuitem', name='Allow scripts (no network)').click()
    expect(frame.locator('#out')).to_have_text('script ran')
    assert page.locator('#preview-body iframe.wb-html').get_attribute('sandbox') == 'allow-scripts'
    assert page.evaluate('window.bad') is None

    open_file('data.csv')
    expect(body.locator('tbody tr')).to_have_count(2)
    expect(body.locator('tbody tr').first).to_contain_text('Smith, J')
    body.get_by_role('button', name='count').click()
    expect(body.locator('tbody tr').first).to_contain_text('Ada')

    open_file('flow.mmd')
    expect(body.locator('.wb-mermaid-file svg')).to_be_visible(timeout=20000)

    open_file('analysis.ipynb')
    expect(body.locator('.wb-nb-cell.markdown')).to_contain_text('Result')
    expect(body.locator('.wb-nb-output')).to_contain_text('2')
    page.wait_for_function("document.querySelector('#preview-body .wb-nb-image')?.naturalWidth === 1")

    open_file('pixel.png')
    expect(body.locator('.wb-image-size')).to_contain_text('1 × 1')
    body.get_by_role('button', name='100%').click()
    body.get_by_role('button', name='Zoom in').click()
    expect(body.locator('.wb-image-size')).to_contain_text('125%')

    # A PDF reopens at the page it was left on.
    open_file('two.pdf')
    expect(page.locator('#pdf-page')).to_have_text('Page 1 of 2', timeout=20000)
    page.get_by_role('button', name='Next page').click()
    expect(page.locator('#pdf-page')).to_have_text('Page 2 of 2')
    page.locator('#preview-dialog [data-close]').click()
    open_file('two.pdf')
    expect(page.locator('#pdf-page')).to_have_text('Page 2 of 2', timeout=20000)
    assert not errors, errors


CHAR_POINT = '''([text, offset]) => {
  // The screen position of one character of terminal output, from xterm's DOM rows.
  const row = [...document.querySelectorAll('#agent-terminal .xterm-rows > div')].reverse().find(r => r.textContent.includes(text));
  if (!row) return null;
  const walker = document.createTreeWalker(row, NodeFilter.SHOW_TEXT);
  let node, at = row.textContent.indexOf(text) + offset, seen = 0;
  while ((node = walker.nextNode())) {
    if (seen + node.length > at) {
      const range = document.createRange();
      range.setStart(node, at - seen); range.setEnd(node, at - seen + 1);
      const r = range.getBoundingClientRect();
      return {x: r.left + r.width / 2, y: r.top + r.height / 2};
    }
    seen += node.length;
  }
  return null;
}'''


def click_link(page, text, offset):
    # xterm finds a link when the pointer rests on it, then shows a pointer
    # cursor; clicking before that does nothing.
    point = page.evaluate(CHAR_POINT, [text, offset])
    for nudge in (0, 1, 2, 3):
        page.mouse.move(point['x'] - 3 + nudge, point['y'])
        page.mouse.move(point['x'] + nudge, point['y'])
        try:
            page.wait_for_selector('#agent-terminal .xterm-cursor-pointer', timeout=3000)
            break
        except Exception:
            continue
    page.mouse.click(point['x'], point['y'])


def test_terminal_path_links_and_line_deep_links(page, real_terminal):
    t = real_terminal
    root = t['root']
    (root / 'src').mkdir()
    (root / 'src' / 'app.py').write_text('one\ntwo\nthree\nfour\nfive\n')
    open_terminal(page, t)
    type_command(page, "printf 'error at %s\\n' src/app.py:4:2")
    expect(page.locator('#agent-terminal .xterm-rows')).to_contain_text('error at src/app.py:4:2')
    click_link(page, 'error at src/app.py', 12)
    expect(page.locator('#preview-dialog')).to_be_visible()
    monaco_ready(page)
    expect(page.locator('#preview-body .wb-target-line')).to_have_count(1)
    assert '#L4' in page.url
    # The address is a deep link. Changing only its fragment moves the line.
    page.goto(f"{t['url']}/terminal/session/{t['id']}?open=src%2Fapp.py#L2")
    monaco_ready(page)
    expect(page.locator('#preview-body .wb-target-line')).to_have_count(1)
    # The same page only changed its fragment; the highlight moves to line 2.
    page.wait_for_function('''() => {
      const target = document.querySelector('#preview-body .wb-target-line');
      const line = [...document.querySelectorAll('#preview-body .view-line')].find(l => l.textContent === 'two');
      return target && line && Math.abs(target.getBoundingClientRect().top - line.getBoundingClientRect().top) < 4;
    }''')
    # A fresh load of a deep link does the same.
    page.goto(f"{t['url']}/terminal/session/{t['id']}?open=src%2Fapp.py&fresh=1#L5")
    monaco_ready(page)
    page.wait_for_function('''() => {
      const target = document.querySelector('#preview-body .wb-target-line');
      const line = [...document.querySelectorAll('#preview-body .view-line')].find(l => l.textContent === 'five');
      return target && line && Math.abs(target.getBoundingClientRect().top - line.getBoundingClientRect().top) < 4;
    }''')


def test_phone_reads_edits_and_taps_terminal_paths(browser, real_terminal):
    t = real_terminal
    root = t['root']
    (root / 'src').mkdir()
    (root / 'src' / 'app.py').write_text('one\ntwo\nthree\nfour\nfive\n')
    (root / 'guide.md').write_text('# Guide\n\n## Install\n\nText\n\n## Use\n\nMore\n')
    context = browser.new_context(**PHONE)
    phone = context.new_page()
    errors = []
    phone.on('pageerror', lambda e: errors.append(str(e)))
    phone.goto(f"{t['url']}/terminal/session/{t['id']}")
    expect(phone.locator('#connection')).to_have_text('Connected', timeout=20000)
    expect(phone.locator('#agent-terminal .xterm-screen')).to_contain_text('$', timeout=10000)
    phone.locator('#agent-terminal').tap()
    type_command(phone, "printf 'see %s\\n' src/app.py:3")
    expect(phone.locator('#agent-terminal .xterm-rows')).to_contain_text('see src/app.py:3')
    point = phone.evaluate(CHAR_POINT, ['see src/app.py', 6])
    phone.touchscreen.tap(point['x'], point['y'])
    # The file opens full screen in the light reader, at the line, without
    # downloading the desktop editor.
    editor = phone.locator('#preview-dialog')
    expect(editor).to_be_visible()
    frame = editor.bounding_box()
    assert frame['width'] >= 389 and frame['x'] <= 1
    expect(phone.locator('.wb-plain-line.target')).to_have_attribute('data-line', '3')
    assert phone.locator('.monaco-editor').count() == 0
    assert not phone.evaluate("performance.getEntriesByType('resource').some(e => /editor\\.api|monaco-/.test(e.name))")
    assert phone.evaluate('document.documentElement.scrollWidth <= innerWidth')
    # Simple edit mode, saved to the target.
    phone.locator('#file-edit').tap()
    area = phone.locator('.wb-plain-edit')
    area.fill('one\ntwo\nthree (edited on the phone)\nfour\nfive\n')
    phone.locator('#file-save').tap()
    wait_for(lambda: 'edited on the phone' in (root / 'src' / 'app.py').read_text(), message='phone save')
    # Back to the explorer, then a Markdown file with its contents menu.
    phone.locator('#preview-dialog [data-close]').tap()
    expect(phone.locator('#files-dialog')).to_be_visible()
    phone.get_by_role('button', name='guide.md', exact=True).tap()
    toc = phone.locator('.wb-toc')
    toc.get_by_role('button', name='Contents').tap()
    toc.get_by_role('link', name='Use').tap()
    expect(phone.locator('#wb-h-use')).to_be_in_viewport()
    phone.locator('#preview-dialog [data-close]').tap()
    phone.locator('#files-dialog [data-close]').tap()
    expect(phone.locator('#files-dialog')).to_be_hidden()
    assert not errors, errors
    context.close()


def put_pref(t, key, value):
    request = urllib.request.Request(t['url'] + '/api/ui/prefs/' + key, data=json.dumps(value).encode(), method='PUT',
                                     headers={'Content-Type': 'application/json'})
    urllib.request.urlopen(request, timeout=10).read()


def test_workspace_file_panes_go_to_file_and_light_theme(page, real_terminal):
    t = real_terminal
    root = t['root']
    (root / 'wanted.txt').write_text('first line\nsecond line\n')
    (root / 'docs').mkdir()
    (root / 'docs' / 'guide.md').write_text('# Guide\n')
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    page.goto(f"{t['url']}/#terminals/session/{t['id']}")
    frame = page.frame_locator('#terminal-workspace iframe').first
    expect(frame.locator('#connection')).to_have_text('Connected', timeout=20000)
    # Ctrl+P over the workspace: Go to file for the session in front, which
    # opens the file as a pane beside the terminal.
    page.locator('.terminal-tablist').click()
    page.keyboard.press('Control+p')
    quick = page.locator('#workspace-quick-open')
    expect(quick.locator('#quick-open-input')).to_be_focused()
    quick.locator('#quick-open-input').fill('wanted:2')
    expect(quick.locator('#quick-open-0')).to_contain_text('wanted.txt', timeout=10000)
    quick.locator('#quick-open-input').press('Enter')
    file_pane = page.locator('.terminal-tabpanel[data-pane-kind="file"]')
    expect(file_pane).to_be_visible()
    expect(file_pane.locator('.wb-body .wb-monaco[data-ready]')).to_be_visible(timeout=20000)
    expect(file_pane.locator('.wb-target-line')).to_have_count(1)
    # Edit and save from the pane; the bytes land on the target.
    file_pane.locator('.view-lines').click()
    page.keyboard.press('Control+End')
    page.keyboard.type('from the pane')
    file_pane.locator('#file-save').click()
    wait_for(lambda: 'from the pane' in (root / 'wanted.txt').read_text(), message='pane save')
    # The explorer as its own pane, which opens files beside itself.
    page.locator('.terminal-actions > summary').first.click()
    page.get_by_role('menuitem', name='Open files beside').click()
    explorer = page.locator('.terminal-tabpanel[data-pane-kind="files"]')
    expect(explorer.locator('.wb-row[data-path="docs"]')).to_be_visible(timeout=10000)
    # It follows the disk through the target watch.
    (root / 'appeared-in-pane.txt').write_text('x')
    expect(explorer.locator('.wb-row[data-path="appeared-in-pane.txt"]')).to_be_visible(timeout=10000)
    explorer.get_by_role('button', name='docs', exact=True).click()
    explorer.get_by_role('button', name='guide.md', exact=True).click()
    expect(page.locator('.terminal-tabpanel[data-pane-kind="file"]', has_text='Guide')).to_be_visible(timeout=10000)
    # Light theme: the file panes follow the app's tokens.
    put_pref(t, 'appearance', {'theme': 'light', 'accent': '', 'zoom': 1, 'language': ''})
    page.reload()
    expect(page.locator('html')).to_have_attribute('data-theme', 'light')
    explorer = page.locator('.terminal-tabpanel[data-pane-kind="files"]')
    expect(explorer.locator('.wb-row').first).to_be_visible(timeout=10000)
    background = explorer.locator('.wb-pane').evaluate('e => getComputedStyle(e).backgroundColor')
    channels = [int(v) for v in re.findall(r'\d+', background)[:3]]
    assert min(channels) > 200, background
    put_pref(t, 'appearance', {'theme': 'dark', 'accent': '', 'zoom': 1, 'language': ''})
    assert not errors, errors


def test_rich_markdown_editing_and_live_watch(page, real_terminal):
    t = real_terminal
    root = t['root']
    (root / 'notes.md').write_text('---\ntitle: Kept\n---\n# Notes\n\nFirst paragraph.\n\n- [ ] todo\n')
    open_terminal(page, t)
    page.locator('#files').click()
    # The explorer is live through the target's file watch, not a timer.
    expect(page.locator('#files-dialog')).to_have_attribute('data-watch', re.compile('inotify|poll'), timeout=10000)
    started = time.monotonic()
    (root / 'watched.txt').write_text('x')
    expect(row(page, 'watched.txt')).to_be_visible(timeout=10000)
    print('explorer saw a new file after', round(time.monotonic() - started, 2), 's')
    page.get_by_role('button', name='notes.md', exact=True).click()
    page.get_by_role('button', name='Rich').click()
    rich = page.locator('#preview-body .wb-rich')
    expect(rich).to_be_visible(timeout=20000)
    expect(rich.locator('h1')).to_have_text('Notes')
    # Type into the rendered document; the toolbar formats the selection.
    rich.locator('p', has_text='First paragraph.').click()
    page.keyboard.press('End')
    page.keyboard.type(' Typed in place.')
    page.keyboard.press('Enter')
    page.get_by_role('button', name='Bold').click()
    page.keyboard.type('strong words')
    page.keyboard.press('Control+s')
    def saved():
        text = (root / 'notes.md').read_text()
        return 'Typed in place.' in text and '**strong words**' in text
    wait_for(saved, message='rich save: ' + (root / 'notes.md').read_text())
    text = (root / 'notes.md').read_text()
    assert text.startswith('---\ntitle: Kept\n---\n'), text
    assert '# Notes' in text and '[ ] todo' in text, text
