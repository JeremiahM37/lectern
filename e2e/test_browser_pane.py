"""The Browser pane, Design Mode and the agent browser tools, end to end.

A real Lectern with a real local target and a real tmux session standing in
for the agent (it writes whatever it is sent to received.txt), a fixture dev
server bound to loopback, and the real headless Chromium Lectern starts on the
target. Nothing here talks to the live service.
"""
import glob
import json
import re
import os
import secrets
import shutil
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path

import pytest
from playwright.sync_api import expect
from conftest import _binary, _port_open, _unused_port
from test_terminal_workspace import real_terminal

LIVE = [{'live': True}]
CHROMIUM = any(shutil.which(b) for b in ('chromium', 'chromium-browser', 'google-chrome', 'google-chrome-stable'))
pytestmark = pytest.mark.skipif(not CHROMIUM, reason='needs a Chromium for the session browser')

SHOP = '''<!doctype html><html><head><title>Fixture shop</title><style>
body{margin:0;font-family:sans-serif}
h1{margin:16px 30px;font-size:28px}
#card{position:absolute;left:30px;top:90px;width:180px;height:100px;background:rgb(0,0,255);color:#fff;border-radius:8px;padding:0}
form{position:absolute;top:230px;left:30px}
</style><script src="/assets/app.js"></script></head><body>
<h1 id="title">Fixture shop</h1>
<div id="card" class="card"><span>Blue card</span></div>
<form><label>Name <input id="name"></label>
<button type="button" id="save">Save</button></form>
<p id="msg" style="position:absolute;top:290px;left:30px">unsaved</p>
</body></html>'''
APP_JS = '''addEventListener('DOMContentLoaded',()=>{document.body.dataset.assets='loaded';
document.querySelector('#save').onclick=()=>{document.querySelector('#msg').textContent='saved '+document.querySelector('#name').value;console.log('saved')}})'''


@pytest.fixture()
def shop(tmp_path, real_terminal):
    # A dev server the way an agent starts one: from the session's workspace,
    # bound to loopback only.
    site = tmp_path / 'shop'
    (site / 'assets').mkdir(parents=True)
    (site / 'index.html').write_text(SHOP)
    (site / 'more.html').write_text('<title>More</title><p>Needle one, needle two, needle three.</p>'
                                    '<a id="pop" href="/index.html" target="_blank">Open shop in a new tab</a> '
                                    '<a id="dl" href="/report.csv" download>Download report</a>')
    (site / 'report.csv').write_text('a,b\n1,2\n')
    (site / 'assets' / 'app.js').write_text(APP_JS)
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        port = s.getsockname()[1]
    proc = subprocess.Popen([sys.executable, '-m', 'http.server', str(port), '--bind', '127.0.0.1', '--directory', str(site)],
                            cwd=real_terminal['root'], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for _ in range(100):
        try:
            urllib.request.urlopen(f'http://127.0.0.1:{port}/', timeout=1).read()
            break
        except Exception:
            time.sleep(.05)
    yield port
    proc.terminate()
    proc.wait(timeout=10)


def stand_in_agent(t):
    """The session's 'agent': everything typed into it lands in received.txt."""
    subprocess.run(['tmux', 'send-keys', '-t', 'terminal-test', 'cat > received.txt', 'Enter'], env=t['env'], check=True)
    time.sleep(.3)


def received(t):
    path = Path(t['root']) / 'received.txt'
    return path.read_text(errors='replace') if path.exists() else ''


def wait_for(what, fn, timeout=20):
    deadline = time.time() + timeout
    while time.time() < deadline:
        value = fn()
        if value:
            return value
        time.sleep(.2)
    raise AssertionError(f'timed out waiting for {what}')


def open_pane(page, t):
    page.goto(t['url'] + '/#sessions')
    page.locator('.scard', has_text='Real terminal').get_by_role('button', name='Chat', exact=True).click()
    expect(page.locator('#conversation')).to_be_visible()
    page.locator('#conversation-browser').click()
    pane = page.locator('.browser-pane')
    expect(pane).to_be_visible()
    return pane


def evidence(page, name):
    """Keep a screenshot for docs/media/browser when LECTERN_BROWSER_EVIDENCE names a directory."""
    out = os.environ.get('LECTERN_BROWSER_EVIDENCE')
    if out:
        Path(out).mkdir(parents=True, exist_ok=True)
        page.screenshot(path=str(Path(out) / name))


def mcp(t, *calls):
    """Speak MCP to `lectern mcp` the way an agent in this session would."""
    lines = [json.dumps({'jsonrpc': '2.0', 'id': i + 1, 'method': 'tools/call',
                         'params': {'name': name, 'arguments': args}}) for i, (name, args) in enumerate(calls)]
    env = {**t['env'], 'LECTERN_API': t['url'], 'LECTERN_SESSION_ID': str(t['id']), 'TMUX': ''}
    out = subprocess.run([_binary(), 'mcp'], input='\n'.join(lines) + '\n', env=env, capture_output=True, text=True, timeout=120)
    assert out.returncode == 0, out.stderr
    results = []
    for line in out.stdout.strip().splitlines():
        frame = json.loads(line)
        result = frame['result']
        text = result['content'][0]['text']
        results.append({'text': text, 'error': result.get('isError', False), 'content': result['content']})
    return results


def staged(t):
    files = {}
    for path in glob.glob(str(Path(t['root']) / '.lectern' / 'context' / '*' / '*')):
        files.setdefault(os.path.basename(path), []).append(path)
    return files


def png_size(path):
    data = Path(path).read_bytes()
    assert data[:8] == b'\x89PNG\r\n\x1a\n', path
    return int.from_bytes(data[16:20], 'big'), int.from_bytes(data[20:24], 'big')


@pytest.mark.parametrize('real_terminal', LIVE, indirect=True)
def test_design_mode_on_a_live_page_sends_elements_to_the_agent(page, real_terminal, shop):
    t = real_terminal
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    stand_in_agent(t)
    page.set_viewport_size({'width': 1440, 'height': 900})
    pane = open_pane(page, t)
    # Its ports are found on the target, this workspace's first.
    pane.get_by_role('button', name='Ports').click()
    expect(pane.locator('.browser-ports .port.in-workspace', has_text=f':{shop}')).to_be_visible(timeout=20000)
    pane.get_by_label('Address').fill(f'localhost:{shop}')
    pane.get_by_role('button', name='Go').click()
    frame = pane.frame_locator('iframe')
    # The dev server runs untouched through the view: its absolute asset path loads.
    expect(frame.locator('#title')).to_have_text('Fixture shop', timeout=20000)
    expect(frame.locator('body')).to_have_attribute('data-assets', 'loaded')
    # No picker until Design Mode is on.
    assert frame.locator('lectern-design-overlay').count() == 0
    assert pane.locator('iframe').evaluate('f=>f.src').count('__lectern_ticket') == 1
    pane.get_by_role('button', name='Design').click()
    # The page reloads with the picker: only a document loaded in Design Mode carries it.
    expect(frame.locator('script[src="/__lectern/design.js"]')).to_have_count(1, timeout=20000)
    expect(frame.locator('#title')).to_have_text('Fixture shop')
    frame.locator('#card').hover()
    expect(pane.locator('.browser-hover')).to_contain_text('body › div#card', timeout=10000)
    frame.locator('#card').click()
    expect(pane.locator('.browser-picked li')).to_have_count(1)
    # Shift adds a second element; the page never saw either click.
    frame.locator('#title').click(modifiers=['Shift'])
    expect(pane.locator('.browser-picked li')).to_have_count(2)
    pane.get_by_label('Note for the agent').fill('Make the card green and the title bigger')
    evidence(page, 'design-pane.png')
    with page.expect_response(lambda r: r.url.endswith(f'/api/sessions/{t["id"]}/design')) as sent:
        pane.get_by_role('button', name='Send 2 to agent').click()
    body = sent.value.json()
    assert sent.value.status == 200, body
    sources = [s['source'] for s in body['screenshots']]
    assert all('headless Chromium on terminal-local' in s for s in sources), sources
    files = staged(t)
    for name in ('design.md', 'element-1.html', 'element-2.html', 'element-1.png', 'element-2.png'):
        assert name in files, files
    assert png_size(files['element-1.png'][0]) == (180, 100)
    md = Path(files['design.md'][0]).read_text()
    if os.environ.get('LECTERN_BROWSER_EVIDENCE'):
        shutil.copy(files['element-1.png'][0], Path(os.environ['LECTERN_BROWSER_EVIDENCE']) / 'element-1.png')
        shutil.copy(files['design.md'][0], Path(os.environ['LECTERN_BROWSER_EVIDENCE']) / 'design.md')
    assert 'Selector: `#card`' in md and 'background-color: rgb(0, 0, 255);' in md, md
    assert f'http://localhost:{shop}/' in md, md
    # One message reached the agent, naming the files.
    wait_for('the message', lambda: files['element-2.png'][0] in received(t) and 'Make the card green' in received(t))
    assert not errors, errors


@pytest.mark.parametrize('real_terminal', LIVE, indirect=True)
def test_the_agent_drives_the_shared_browser_while_the_operator_watches_and_takes_over(page, real_terminal, shop):
    t = real_terminal
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    stand_in_agent(t)
    page.set_viewport_size({'width': 1440, 'height': 900})
    # The agent opens its dev server through MCP; the browser starts on the target.
    nav, snap = mcp(t, ('browser_navigate', {'url': f'http://localhost:{shop}/'}), ('browser_snapshot', {}))
    assert 'Fixture shop' in nav['text'] and not nav['error'], nav
    tree = json.loads(snap['text'])['tree']
    assert 'button "Save"' in tree and snap['content'][1]['type'] == 'image', snap
    pane = open_pane(page, t)
    # The pane shows the same browser, live.
    expect(pane.locator('.browser-screen img')).to_be_visible(timeout=20000)
    expect(pane.get_by_role('radio', name='Shared browser')).to_be_checked()
    ref = lambda label: int(next(l for l in tree.splitlines() if label in l).split('[ref=')[1].split(']')[0])
    fill, click, value = mcp(t, ('browser_fill', {'ref': ref('textbox "Name"'), 'text': 'Ada'}),
                             ('browser_click', {'ref': ref('button "Save"')}),
                             ('browser_evaluate', {'expression': "document.querySelector('#msg').textContent"}))
    assert '"value": "saved Ada"' in value['text'], (fill, click, value)
    expect(pane.locator('.browser-control')).to_contain_text('The agent is driving', timeout=10000)
    evidence(page, 'agent-driving.png')
    # Taking over refuses the agent until the operator hands it back.
    pane.get_by_role('button', name='Take over').click()
    expect(pane.locator('.browser-control')).to_contain_text('You have control')
    refused, = mcp(t, ('browser_click', {'selector': '#save'}))
    assert refused['error'] and 'taken over' in refused['text'], refused
    pane.get_by_role('button', name='Hand back to agent').click()
    expect(pane.locator('.browser-control')).to_contain_text('may drive')
    ok, = mcp(t, ('browser_console', {}))
    assert not ok['error'] and 'saved' in ok['text'], ok
    # The operator types into the streamed page: a click on the field, keys.
    img = pane.locator('.browser-screen img')
    box = img.bounding_box()
    vp = page.request.get(f"{t['url']}/api/sessions/{t['id']}/browser").json()['state']['viewport']
    # Page coordinates come from the fixture's own CSS (evaluate cannot
    # measure layout: DevTools counts that as a side effect).
    scale = min(box['width'] / vp['width'], box['height'] / vp['height'])
    left = box['x'] + (box['width'] - vp['width'] * scale) / 2
    top = box['y'] + (box['height'] - vp['height'] * scale) / 2
    # A click on the "Name" label focuses its field.
    page.mouse.click(left + 40 * scale, top + 240 * scale)
    expect(pane.locator('.browser-control')).to_contain_text('You have control')
    page.keyboard.press('End')
    page.keyboard.type('!')
    pane.get_by_role('button', name='Hand back to agent').click()
    # The hand-back follows the keystrokes on the same stream, so they can
    # no longer take control back after it.
    expect(pane.locator('.browser-control')).to_contain_text('may drive')
    value, = mcp(t, ('browser_evaluate', {'expression': "document.querySelector('#name').value"}))
    assert '"value": "Ada!"' in value['text'], value
    # Design Mode in the shared browser: the element comes from the live page.
    pane.get_by_role('button', name='Design').click()
    expect(pane.locator('.browser-design')).to_be_visible()
    # The Design Mode panel takes room, so the picture has moved: measure again.
    box = img.bounding_box()
    scale = min(box['width'] / vp['width'], box['height'] / vp['height'])
    left = box['x'] + (box['width'] - vp['width'] * scale) / 2
    top = box['y'] + (box['height'] - vp['height'] * scale) / 2
    card = {'x': 30, 'y': 90}
    page.mouse.move(left + (card['x'] + 20) * scale, top + (card['y'] + 20) * scale)
    expect(pane.locator('.browser-hover')).to_contain_text('div#card', timeout=10000)
    page.mouse.click(left + (card['x'] + 20) * scale, top + (card['y'] + 20) * scale)
    expect(pane.locator('.browser-picked li')).to_have_count(1, timeout=10000)
    with page.expect_response(lambda r: r.url.endswith('/design')) as sent:
        pane.get_by_role('button', name='Send 1 to agent').click()
    body = sent.value.json()
    assert body['screenshots'][0]['source'].startswith("the session's shared browser"), body
    files = staged(t)
    assert png_size(files['element-1.png'][0]) == (180, 100)
    wait_for('the message', lambda: files['element-1.png'][0] in received(t))
    assert not errors, errors


@pytest.mark.parametrize('real_terminal', LIVE, indirect=True)
def test_the_browser_cli_is_the_same_door_for_agents_without_mcp(real_terminal, shop, tmp_path):
    t = real_terminal
    env = {**t['env'], 'LECTERN_API': t['url'], 'TMUX': ''}

    def cli(*args):
        out = subprocess.run([_binary(), 'browser', *args, '--session', str(t['id'])], env=env,
                             capture_output=True, text=True, timeout=120)
        assert out.returncode == 0, out.stderr
        return out.stdout

    assert 'Fixture shop' in cli('open', f'http://localhost:{shop}/')
    shot = tmp_path / 'page.png'
    tree = cli('snapshot', '--screenshot', str(shot))
    assert tree.startswith('Fixture shop — http://localhost') and 'button "Save"' in tree, tree
    assert shot.read_bytes()[:4] == b'\x89PNG'
    ref = next(l for l in tree.splitlines() if 'textbox "Name"' in l).split('[ref=')[1].split(']')[0]
    cli('fill', ref, 'Lin')
    cli('click', '--selector', '#save')
    assert '"value": "saved Lin"' in cli('eval', "document.querySelector('#msg').textContent")
    card = tmp_path / 'card.png'
    cli('screenshot', str(card), '--selector', '#card')
    assert png_size(str(card)) == (180, 100)
    assert '"closed": true' in cli('close')


DESKTOP = all(shutil.which(b) for b in ('Xvfb', 'x11vnc', 'websockify', 'xdotool')) and \
    any(shutil.which(b) for b in ('scrot', 'import', 'ffmpeg')) and os.path.exists('/usr/share/novnc/vnc.html')


@pytest.mark.skipif(not DESKTOP, reason='needs Xvfb, x11vnc, websockify, noVNC, xdotool and a screen grabber')
@pytest.mark.parametrize('real_terminal', LIVE, indirect=True)
def test_computer_use_is_off_until_allowed_and_stops_on_demand(page, real_terminal, shop):
    t = real_terminal
    env = {**t['env'], 'LECTERN_API': t['url'], 'TMUX': ''}
    # A desktop with the dev server open in a browser on it, as an agent would start one.
    view = json.loads(subprocess.run([_binary(), 'live', f'http://127.0.0.1:{shop}/', '--title', 'Agent desktop',
                                      '--session', str(t['id'])], env=env, capture_output=True, text=True, timeout=90).stdout)
    shot, = mcp(t, ('computer_screenshot', {}))
    assert shot['error'] and 'computer use is off' in shot['text'], shot
    page.set_viewport_size({'width': 1440, 'height': 900})
    pane = open_pane(page, t)
    pane.get_by_role('tab', name='Desktop').click()
    pane.get_by_role('button', name='Allow agent control').click()
    expect(pane.locator('.desk .browser-control')).to_contain_text('may control', timeout=10000)
    # The agent can see what is on screen: the desktop's browser window, by name.
    wait_for('the desktop browser window', lambda: 'Fixture shop' in mcp(t, ('computer_windows', {}))[0]['text'], timeout=30)
    # By element: the accessibility tree names the page's own button.
    last = {}

    def read_tree():
        last['r'] = mcp(t, ('computer_snapshot', {}))[0]
        return not last['r']['error'] and 'button "Save"' in json.loads(last['r']['text'])['tree'] and last['r']
    try:
        snap = wait_for('the page in the accessibility tree', read_tree, timeout=30)
    except AssertionError:
        raise AssertionError(f"no Save button in the accessibility tree: {last.get('r', {}).get('text', '')[:3000]}")
    tree = json.loads(snap['text'])['tree']
    ref = int(next(l for l in tree.splitlines() if 'button "Save"' in l).split('[ref=')[1].split(']')[0])
    pressed, = mcp(t, ('computer_click', {'ref': ref}))
    assert not pressed['error'] and '"via": "accessibility"' in pressed['text'], pressed
    wait_for('the click to land', lambda: 'static "saved' in json.loads(mcp(t, ('computer_snapshot', {}))[0]['text'])['tree'], timeout=20)
    shot, click = mcp(t, ('computer_screenshot', {}), ('computer_click', {'x': 50, 'y': 60}))
    assert not shot['error'] and shot['content'][1]['type'] == 'image', shot
    assert not click['error'], click
    expect(pane.locator('.desk')).to_have_class('desk agent-driving', timeout=10000)
    expect(pane.locator('.desk-shot')).to_be_visible(timeout=10000)
    page.wait_for_timeout(4000)  # let the desktop's browser draw before keeping a picture
    evidence(page, 'computer-use.png')
    pane.get_by_role('button', name='Stop agent').click()
    expect(pane.locator('.desk .browser-control')).to_contain_text('stopped')
    refused, = mcp(t, ('computer_key', {'key': 'Return'}))
    assert refused['error'] and 'stopped' in refused['text'], refused
    subprocess.run([_binary(), 'live', 'stop', str(view['id'])], env=env, capture_output=True, timeout=60)


@pytest.mark.parametrize('real_terminal', LIVE, indirect=True)
def test_the_pane_is_full_screen_on_a_phone(page, real_terminal, shop):
    t = real_terminal
    page.set_viewport_size({'width': 390, 'height': 844})
    pane = open_pane(page, t)
    box = pane.bounding_box()
    assert box['width'] >= 389 and box['height'] >= 800, box
    # One row of controls; everything else is in a menu.
    pane.get_by_role('button', name='More browser controls').click()
    expect(pane.get_by_label('Device size')).to_have_value('phone')
    # Choosing closes the menu.
    pane.get_by_role('radio', name='Shared browser').click()
    expect(pane.locator('.browser-menu')).to_have_count(0)
    pane.get_by_label('Address').fill(f'localhost:{shop}')
    pane.get_by_role('button', name='Go').click()
    expect(pane.locator('.browser-screen img')).to_be_visible(timeout=30000)
    # The page, not the controls, has the screen: at most a fifth of it is chrome.
    stage = pane.locator('.browser-stage').bounding_box()
    assert stage['y'] - box['y'] <= 844 * 0.2, stage
    vp = page.request.get(f"{t['url']}/api/sessions/{t['id']}/browser").json()['state']['viewport']
    assert vp['width'] == 390 and vp['mobile'], vp
    evidence(page, 'phone.png')
    pane.get_by_role('button', name='More browser controls').click()
    expect(pane.locator('.browser-menu')).to_be_visible()
    evidence(page, 'phone-menu.png')
    pane.get_by_role('button', name='More browser controls').click()
    expect(pane.locator('.browser-menu')).to_have_count(0)


RELAY_PORT = _unused_port()
RELAY_SECRET = secrets.token_hex(32)


@pytest.fixture(scope='module')
def browser_relay():
    log = Path(tempfile.mkdtemp(prefix='lec-relay-')) / 'relay.log'
    with log.open('wb') as out:
        relay = subprocess.Popen([_binary(), 'relay', '--listen', f'127.0.0.1:{RELAY_PORT}'],
                                 env={**os.environ, 'LECTERN_RELAY_HOST_SECRET': RELAY_SECRET},
                                 stdout=out, stderr=subprocess.STDOUT)
    for _ in range(100):
        if _port_open(RELAY_PORT):
            break
        time.sleep(.1)
    try:
        yield RELAY_PORT
    finally:
        relay.terminate()
        relay.wait(timeout=10)


@pytest.mark.parametrize('real_terminal', [{'live': True, 'env': {
    'LECTERN_RELAY_URL': f'ws://127.0.0.1:{RELAY_PORT}', 'LECTERN_RELAY_HOST_SECRET': RELAY_SECRET}}], indirect=True)
def test_the_shared_browser_and_design_mode_work_over_the_relay(browser, browser_relay, real_terminal, shop):
    """A phone paired over the encrypted relay has no direct route to Lectern:
    the pane falls back to the shared browser, whose frames, input and Design
    Mode all ride the tunnel."""
    t = real_terminal
    stand_in_agent(t)
    for _ in range(100):
        if t['api']('/relay').get('connected'):
            break
        time.sleep(.1)
    else:
        raise AssertionError('Lectern never reached the relay')
    minted = t['api']('/relay/pair', {})
    ctx = browser.new_context(viewport={'width': 390, 'height': 844})
    phone = ctx.new_page()
    sockets = []
    phone.on('websocket', lambda ws: sockets.append(ws.url))
    try:
        phone.goto(t['url'] + '/relay-pair#p=' + minted['fragment'])
        phone.click('#relay-pair-submit')
        expect(phone.locator('#conn-label')).to_have_text('LIVE', timeout=20000)
        pane = open_pane(phone, t)
        pane.get_by_role('button', name='More browser controls').click()
        expect(pane.get_by_role('radio', name='Live page')).to_be_disabled()
        expect(pane.get_by_role('radio', name='Shared browser')).to_be_checked()
        pane.get_by_role('button', name='More browser controls').click()
        pane.get_by_label('Address').fill(f'localhost:{shop}')
        pane.get_by_role('button', name='Go').click()
        img = pane.locator('.browser-screen img')
        expect(img).to_be_visible(timeout=30000)
        evidence(phone, 'relay-phone.png')
        # Design Mode through the tunnel: the pick, and the send.
        pane.get_by_role('button', name='More browser controls').click()
        pane.get_by_role('button', name='Design').click()
        expect(pane.locator('.browser-design')).to_be_visible()
        vp = t['api'](f"/sessions/{t['id']}/browser")['state']['viewport']
        box = img.bounding_box()
        scale = box['width'] / vp['width']
        # The fixture has no viewport meta, so a phone lays it out 980px wide.
        layout = 980 / vp['width']
        phone.mouse.click(box['x'] + (50 / layout) * scale, box['y'] + (110 / layout) * scale)
        expect(pane.locator('.browser-picked li')).to_have_count(1, timeout=15000)
        expect(pane.locator('.browser-picked li')).to_contain_text('div#card')
        pane.get_by_role('button', name='Send 1 to agent').click()
        files = wait_for('staged files', lambda: staged(t).get('element-1.png'))
        wait_for('the message', lambda: files[0] in received(t))
        # Every socket the phone opened went to the relay, none to Lectern.
        assert sockets and all(f':{browser_relay}/' in u for u in sockets), sockets
    finally:
        ctx.close()


@pytest.mark.parametrize('real_terminal', LIVE, indirect=True)
def test_tabs_find_cookies_downloads_and_profiles(page, real_terminal, shop, tmp_path):
    t = real_terminal
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    page.set_viewport_size({'width': 1440, 'height': 900})
    pane = open_pane(page, t)
    pane.get_by_role('radio', name='Shared browser').check()
    pane.get_by_label('Address').fill(f'localhost:{shop}/more.html')
    pane.get_by_role('button', name='Go').click()
    expect(pane.locator('.browser-tab.on')).to_contain_text('More', timeout=30000)
    # Find in page.
    pane.get_by_role('button', name='Find', exact=True).click()
    pane.get_by_label('Find in page').fill('needle')
    pane.get_by_label('Find in page').press('Enter')
    expect(pane.locator('.browser-find [role=status]')).to_have_text('1 of 3')
    pane.get_by_role('button', name='Next match').click()
    expect(pane.locator('.browser-find [role=status]')).to_have_text('2 of 3')
    # A link that opens a new window becomes a tab, and the agent can list it.
    mcp(t, ('browser_click', {'selector': '#pop'}))
    expect(pane.locator('.browser-tab')).to_have_count(2, timeout=15000)
    expect(pane.locator('.browser-tab.on')).to_contain_text('Fixture shop')
    tabs = json.loads(mcp(t, ('browser_tabs', {}))[0]['text'])['tabs']
    assert [x['title'] for x in tabs] == ['More', 'Fixture shop'] and tabs[1]['active'], tabs
    # The agent acts on a tab by id without switching the operator's view.
    first = tabs[0]['id']
    found, = mcp(t, ('browser_evaluate', {'expression': 'document.title', 'tab': first}))
    assert '"value": "More"' in found['text'], found
    expect(pane.locator('.browser-tab.on')).to_contain_text('Fixture shop')
    pane.locator('.browser-tab', has_text='Fixture shop').get_by_role('button', name=re.compile('Close tab')).click()
    expect(pane.locator('.browser-tab')).to_have_count(1)
    # A download lands in the workspace and shows on the shelf.
    mcp(t, ('browser_click', {'selector': '#dl'}))
    shelf = pane.locator('.browser-downloads li', has_text='report.csv')
    expect(shelf.get_by_role('button', name='Save')).to_be_visible(timeout=20000)
    saved = Path(t['root']) / '.lectern' / 'downloads' / 'report.csv'
    assert saved.read_text() == 'a,b\n1,2\n'
    with page.expect_download() as dl:
        shelf.get_by_role('button', name='Save').click()
    assert Path(dl.value.path()).read_text() == 'a,b\n1,2\n'
    # Cookies from a file, only for the sites asked for.
    cookies = tmp_path / 'cookies.txt'
    cookies.write_text('# Netscape HTTP Cookie File\n'
                       f'localhost\tFALSE\t/\tFALSE\t{int(time.time()) + 3600}\tsignedin\tyes\n'
                       f'other.example\tTRUE\t/\tFALSE\t0\tnot\tme\n')
    pane.get_by_role('button', name='Cookies').click()
    pane.get_by_label('Only these sites').fill('localhost')
    pane.get_by_label('Cookies file').set_input_files(str(cookies))
    expect(pane.locator('.browser-cookies [role=status]')).to_contain_text('Imported 1 cookies for 1 sites', timeout=20000)
    mcp(t, ('browser_navigate', {'url': f'http://localhost:{shop}/index.html'}))
    got, = mcp(t, ('browser_evaluate', {'expression': 'document.cookie'}))
    assert '"value": "signedin=yes"' in got['text'], got
    evidence(page, 'tabs-downloads.png')
    # Profiles: the cookie lives in this profile, not in a new one.
    profile = pane.get_by_label('Browser profile')
    page.once('dialog', lambda d: d.accept('work'))
    profile.select_option('__new')
    expect(profile).to_have_value('work', timeout=30000)
    mcp(t, ('browser_navigate', {'url': f'http://localhost:{shop}/index.html'}))
    got, = mcp(t, ('browser_evaluate', {'expression': 'document.cookie'}))
    assert '"value": ""' in got['text'], got
    assert not errors, errors
