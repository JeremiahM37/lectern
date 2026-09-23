"""Real ttyd + tmux + filesystem; no simulated terminal or target executor."""
import base64
import hashlib
import json
import os
from pathlib import Path
import socket
import shlex
import shutil
import subprocess
import tempfile
import time
import urllib.request

import pytest
from playwright.sync_api import expect
from conftest import _binary, _unused_port, OUTSIDE_WORLD

@pytest.fixture()
def real_terminal(tmp_path, request):
    real_tmux = shutil.which('tmux')
    assert real_tmux, 'tmux is required for the real terminal fixture'
    tmux_dir = tempfile.TemporaryDirectory(prefix='adkt-', dir='/tmp')
    fixture_tmux_dir = os.environ.get('ADK_TEST_TMUX_ROOT', tmux_dir.name)
    Path(fixture_tmux_dir).mkdir(parents=True, exist_ok=True)
    env = {**os.environ, **OUTSIDE_WORLD, 'TMUX_TMPDIR': fixture_tmux_dir, 'TMUX': '', 'LECTERN_MOCK': '0',
           'LECTERN_DB': str(tmp_path/'test.db'), 'LECTERN_HOST': '127.0.0.1',
           'LECTERN_GRIMOIRE_URL': '', 'LECTERN_AUTH_TOKEN': '', 'LECTERN_SESSION_POLL': '3600',
           'LECTERN_HANDOFF_POLL': '0.1', 'LECTERN_TICK': '0.25',
           'XDG_STATE_HOME': str(tmp_path/'state')}
    port = _unused_port(); env['LECTERN_PORT'] = str(port)
    url = f'http://127.0.0.1:{port}'
    # Native attachment is a client command. Keep it pointed at this fixture's
    # server after the CLI's no-API default became the private local runtime.
    env['LECTERN_API'] = url
    root = tmp_path/'workspace'; root.mkdir()
    (root/'hello.txt').write_text('A useful artifact\n<script>window.bad=true</script>\n')
    subprocess.run(['git','init','-q',str(root)], check=True)
    subprocess.run(['tmux','-f','/dev/null','new-session','-d','-s','terminal-test','-c',str(root),'bash','--norc'],env=env,check=True, capture_output=True, text=True)
    options = getattr(request, 'param', {})
    if options.get('live'):
        env['LECTERN_LIVE'] = '1'
    if options.get('isolated_scratch'):
        # The scratch sweep inspects and removes directories under the target's
        # home. Give it a home of its own, so nothing it does can reach the real
        # one on the machine running the suite.
        for key, name in (('LECTERN_SCRATCH_ROOT','scratch'),('CLAUDE_CONFIG_DIR','claude-home'),('CODEX_HOME','codex-home')):
            (tmp_path/name).mkdir(); env[key] = str(tmp_path/name)
    if options.get('no_alternate_screen'):
        subprocess.run(['tmux','set-option','-g','terminal-overrides',',*:smcup@:rmcup@'],env=env,check=True)
    if options.get('agent_script'):
        agent = tmp_path/'test-agent'; agent.write_text(options['agent_script']); agent.chmod(0o755)
        env['LECTERN_CLAUDE_BIN'] = str(agent); env['LECTERN_TICK'] = '0.1'
    if options.get('controllable_stop'):
        tools = tmp_path/'tools'; tools.mkdir()
        wrapper = tools/'tmux'
        wrapper.write_text('#!/bin/sh\nif [ "$1" = if-shell ] && [ -e '+shlex.quote(str(root/'refuse-stop'))+' ]; then exit 0; fi\nexec '+shlex.quote(shutil.which('tmux'))+' "$@"\n')
        wrapper.chmod(0o755); env['PATH'] = str(tools)+os.pathsep+env['PATH']
    if options.get('hold_setup_launch'):
        tools=tmp_path/'launch-tools';tools.mkdir()
        wrapper=tools/'tmux'
        wrapper.write_text('#!/bin/sh\nlec_hold_setup=0\nfor arg do case "$arg" in LECTERN_SETUP_TOKEN=*) lec_hold_setup=1;; esac; done\n'
                           +shlex.quote(real_tmux)+' "$@"\nlec_launch_rc=$?\n'
                           +'if [ "$1" = new-session ] && [ "$lec_hold_setup" = 1 ] && [ "$lec_launch_rc" = 0 ]; then\n'
                           +'touch '+shlex.quote(str(root/'launch-held'))+'\nwhile [ ! -f '+shlex.quote(str(root/'release-launch'))+' ]; do sleep .05; done\nfi\nexit "$lec_launch_rc"\n')
        wrapper.chmod(0o755);env['PATH']=str(tools)+os.pathsep+env['PATH']
    log = (tmp_path/'server.log').open('w')
    proc = subprocess.Popen([_binary()],cwd=root,env=env,stdout=log,stderr=log)
    def api(path, data=None):
        req = urllib.request.Request(url+'/api'+path, data=json.dumps(data).encode() if data is not None else None,
                                     headers={'Content-Type':'application/json'})
        return json.load(urllib.request.urlopen(req,timeout=20))
    try:
        for _ in range(100):
            try: api('/health'); break
            except Exception: time.sleep(.1)
        else: raise RuntimeError('isolated terminal server did not start')
        target = api('/targets',{'name':'terminal-local','kind':'local'})
        sess = api('/sessions/adopt',{'target_id':target['id'],'tmux_session':'terminal-test','workdir':str(root),'name':'Real terminal','agent':'claude'})
        yield dict(url=url,root=root,env=env,id=sess['id'],api=api,proc=proc,port=port,target_id=target['id'])
    finally:
        proc.terminate();proc.wait(timeout=15);log.close()
        _cleanup_tmux(real_tmux, env, fixture_tmux_dir)
        tmux_dir.cleanup()

def open_terminal(page, t):
    page.goto(f"{t['url']}/terminal/session/{t['id']}")
    expect(page.locator('#connection')).to_have_text('Connected',timeout=20000)
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('$',timeout=10000)
    page.locator('#agent-terminal').click()

def _cleanup_tmux(real_tmux, env, tmux_dir):
    """Kill exact sessions on each socket created by this fixture."""
    for socket_path in Path(tmux_dir).glob('tmux-*/default'):
        try:
            names = subprocess.check_output(
                [real_tmux, '-S', str(socket_path), 'list-sessions', '-F', '#{session_name}'],
                env=env, text=True, stderr=subprocess.DEVNULL,
            ).splitlines()
        except subprocess.CalledProcessError:
            continue
        for name in names:
            if name and '\n' not in name and '\r' not in name:
                subprocess.run([real_tmux, '-S', str(socket_path), 'kill-session', '-t', '='+name],
                               env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, check=False)

def terminal_tool(page, selector):
    if not page.locator(selector).is_visible():
        page.locator('#terminal-tools > summary').click()
    page.locator(selector).click()

def capture(t,session='terminal-test'):
    return subprocess.check_output(['tmux','capture-pane','-p','-J','-S','-100000','-t','='+session+':'],env=t['env']).decode()

def type_command(page,text):
    page.keyboard.type(text,delay=1);page.keyboard.press('Enter')

def test_real_terminal_drop_paste_files_and_shell(page,real_terminal):
    t=real_terminal;errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
    open_terminal(page,t)
    page.keyboard.type('sha256sum ')
    content=b'original bytes\x00\xff\n';name="Résumé's $(touch owned).pdf"
    with page.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/attachments')) as response:
        page.locator('#agent-terminal').evaluate('''(el, p) => {const d=new DataTransfer();d.items.add(new File([new Uint8Array(p.bytes)],p.name));el.dispatchEvent(new DragEvent('drop',{bubbles:true,cancelable:true,dataTransfer:d}));}''',{'bytes':list(content),'name':name})
    a=response.value.json();expect(page.locator('#notice')).to_contain_text('Path inserted')
    assert Path(a['path']).read_bytes()==content
    assert hashlib.sha256(content).hexdigest() not in capture(t)
    assert not (t['root']/'owned').exists()
    page.keyboard.press('Enter')
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text(hashlib.sha256(content).hexdigest(),timeout=10000)
    # A real clipboard image follows the upload path, never textual escape input.
    png=base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aF9sAAAAASUVORK5CYII=')
    page.keyboard.type('sha256sum ')
    with page.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/attachments')) as response:
        page.locator('#agent-terminal textarea').evaluate('''(el, bytes)=>{const d=new DataTransfer();d.items.add(new File([new Uint8Array(bytes)],'screenshot.png',{type:'image/png'}));el.dispatchEvent(new ClipboardEvent('paste',{clipboardData:d,bubbles:true,cancelable:true}));}''',list(png))
    a=response.value.json();expect(page.locator('#notice')).to_contain_text('screenshot.png uploaded')
    assert Path(a['path']).read_bytes()==png
    assert hashlib.sha256(png).hexdigest() not in capture(t)
    page.keyboard.press('Enter')
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text(hashlib.sha256(png).hexdigest(),timeout=10000)
    # File drawer renders HTML-looking output as text and downloads exact bytes.
    page.locator('#files').click();page.get_by_role('button',name='hello.txt',exact=True).click()
    expect(page.locator('#preview-body')).to_contain_text('<script>window.bad=true</script>')
    assert page.evaluate('window.bad') is None
    with page.expect_download() as download:page.locator('#preview-download').click()
    assert Path(download.value.path()).read_bytes()==(t['root']/'hello.txt').read_bytes()
    page.locator('#preview-dialog [data-close]').click();page.locator('#files-dialog [data-close]').click()
    terminal_tool(page,'#shell')
    expect(page.locator('#workspace .pane').nth(1).locator('.pane-status')).to_have_text('Connected',timeout=20000)
    page.locator('#workspace .pane').nth(1).locator('.terminal-host').click()
    type_command(page,"printf companion-proof > companion.txt")
    for _ in range(50):
        if (t['root']/'companion.txt').exists():break
        time.sleep(.1)
    assert (t['root']/'companion.txt').read_text()=='companion-proof'
    assert 'companion-proof' not in capture(t)
    terminal_tool(page,'#shell')
    subprocess.run(['tmux','has-session','-t',f"=lec-companion-session-{t['id']}"],env=t['env'],check=True)
    terminal_tool(page,'#shell')
    expect(page.locator('#workspace .pane').nth(1).locator('.pane-status')).to_have_text('Connected',timeout=20000)
    page.screenshot(path='/tmp/lectern-terminal-workspace-desktop.png')
    assert errors==[]

def test_real_terminal_history_preferences_pause_and_two_clients(page,browser,real_terminal):
    t=real_terminal;open_terminal(page,t)
    type_command(page,"for i in $(seq 1 180); do echo HISTORY-PROOF-$i; done")
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('HISTORY-PROOF-180')
    terminal_tool(page,'#history');page.locator('#history-query').fill('HISTORY-PROOF-12')
    expect(page.locator('#history-count')).to_contain_text(' / 11',timeout=10000)
    expect(page.locator('#history-text mark.current')).to_have_text('HISTORY-PROOF-12')
    page.locator('#history-next').click();expect(page.locator('#history-count')).to_have_text('2 / 11')
    page.locator('#history-dialog [data-close]').click()
    terminal_tool(page,'#preferences');page.locator('#font-size').fill('19');page.locator('#line-height').select_option('1.3');page.locator('#theme').select_option('black');page.locator('#settings-dialog [data-close]').click()
    page.reload();expect(page.locator('#connection')).to_have_text('Connected',timeout=20000)
    terminal_tool(page,'#preferences');expect(page.locator('#font-size')).to_have_value('19');expect(page.locator('#theme')).to_have_value('black');page.locator('#settings-dialog [data-close]').click()
    terminal_tool(page,'#pause');frozen=page.locator('#agent-pane .frozen');expect(frozen).to_be_visible();before=frozen.text_content()
    second=page.context.new_page();open_terminal(second,t);type_command(second,'echo AFTER-PAUSE-PROOF')
    expect(second.locator('#agent-terminal .xterm-screen')).to_contain_text('AFTER-PAUSE-PROOF')
    assert frozen.text_content()==before
    terminal_tool(page,'#bottom');expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('AFTER-PAUSE-PROOF')
    second.close()
    page.set_viewport_size({'width':390,'height':480})
    expect(page.locator('#agent-terminal')).to_be_visible()
    assert page.locator('#agent-terminal').bounding_box()['height']>150
    assert page.evaluate('document.documentElement.scrollWidth<=innerWidth')
    page.screenshot(path='/tmp/lectern-terminal-workspace-mobile.png')
    # The desktop launcher CLI joins the same session over a real PTY.
    import pty,select
    master,slave=pty.openpty()
    child=subprocess.Popen([_binary(),'attach','session',str(t['id'])],stdin=slave,stdout=slave,stderr=slave,env={**t['env'],'TERM':'xterm-256color'})
    os.close(slave)
    try:
        output=b'';deadline=time.time()+15
        while time.time()<deadline and b'AFTER-PAUSE-PROOF' not in output:
            if select.select([master],[],[],.2)[0]:output+=os.read(master,65536)
        assert b'AFTER-PAUSE-PROOF' in output
        os.write(master,b'echo DESKTOP-PTY-PROOF\r')
        expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('DESKTOP-PTY-PROOF',timeout=10000)
        os.write(master,b'\x02d');child.wait(timeout=10)
        assert child.returncode==0
    finally:
        if child.poll() is None:child.terminate();child.wait(timeout=10)
        os.close(master)
    subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)


def test_real_terminal_previews_failure_recovery_and_reconnect(page,real_terminal):
    t=real_terminal;open_terminal(page,t)
    png=base64.b64decode('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+aF9sAAAAASUVORK5CYII=')
    (t['root']/'image.png').write_bytes(png)
    # A small, valid PDF, including xref offsets, rendered by the real PDF.js worker.
    objects=[b'<< /Type /Catalog /Pages 2 0 R >>',b'<< /Type /Pages /Kids [3 0 R] /Count 1 >>',b'<< /Type /Page /Parent 2 0 R /MediaBox [0 0 300 200] /Contents 4 0 R /Resources << /Font << /F1 5 0 R >> >> >>']
    stream=b'BT /F1 24 Tf 30 100 Td (PDF preview proof) Tj ET'
    objects += [b'<< /Length '+str(len(stream)).encode()+b' >>\nstream\n'+stream+b'\nendstream', b'<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>']
    pdf=b'%PDF-1.4\n'; offsets=[0]
    for i,obj in enumerate(objects,1):
        offsets.append(len(pdf));pdf+=f'{i} 0 obj\n'.encode()+obj+b'\nendobj\n'
    xref=len(pdf);pdf+=b'xref\n0 6\n0000000000 65535 f \n'+b''.join(f'{v:010d} 00000 n \n'.encode() for v in offsets[1:])+f'trailer\n<< /Root 1 0 R /Size 6 >>\nstartxref\n{xref}\n%%EOF\n'.encode()
    (t['root']/'proof.pdf').write_bytes(pdf)
    page.locator('#files').click();page.get_by_role('button',name='image.png',exact=True).click()
    expect(page.locator('#preview-body img')).to_be_visible()
    page.wait_for_function("document.querySelector('#preview-body img').naturalWidth===1")
    page.locator('#preview-dialog [data-close]').click()
    page.get_by_role('button',name='proof.pdf',exact=True).click()
    expect(page.locator('#pdf-page')).to_have_text('Page 1 of 1',timeout=20000)
    assert page.locator('#preview-body canvas').evaluate('(c)=>c.width>250 && c.getContext("2d").getImageData(0,0,c.width,c.height).data.some((v,i)=>i%4!==3 && v<100)')
    page.screenshot(path='/tmp/lectern-terminal-pdf.png')
    page.locator('#preview-dialog [data-close]').click();page.locator('#files-dialog [data-close]').click()
    # A rejected upload is visible, does not type, and permits a successful retry.
    page.route('**/attachments',lambda route:route.fulfill(status=502,content_type='application/json',body='{"detail":"Target unavailable"}'))
    before=capture(t)
    page.locator('#file-input').set_input_files({'name':'retry.txt','mimeType':'text/plain','buffer':b'retry'})
    expect(page.locator('#notice')).to_have_text('Target unavailable')
    assert capture(t).rstrip()==before.rstrip()
    page.unroute('**/attachments')
    page.locator('#file-input').set_input_files({'name':'retry.txt','mimeType':'text/plain','buffer':b'retry'})
    expect(page.locator('#notice')).to_contain_text('Path inserted')
    page.keyboard.press('Control+C')
    # Stop only this isolated server's ttyd. The named URL must respawn it.
    children=subprocess.check_output(['ps','--ppid',str(t['proc'].pid),'-o','pid=,comm=']).decode().splitlines()
    ttyds=[int(line.split()[0]) for line in children if line.split()[1]=='ttyd']
    assert ttyds
    for pid in ttyds:os.kill(pid,15)
    expect(page.locator('#connection')).to_have_text('Reconnecting…',timeout=10000)
    expect(page.locator('#connection')).to_have_text('Connected',timeout=20000)
    page.locator('#agent-terminal').click();type_command(page,'echo RECONNECTED-PROOF')
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('RECONNECTED-PROOF')


def test_terminal_resize_and_reconnect_during_fullscreen_output(page, real_terminal):
    t = real_terminal
    subprocess.run(['tmux','set-window-option','-g','window-size','latest'],env=t['env'],check=True)
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    page.add_init_script('''window.terminalSockets=[];
      const Original=window.WebSocket;
      window.WebSocket=class extends Original {
        constructor(...args){super(...args);window.terminalSockets.push(this);}
      };''')
    # A real full-screen process redraws on SIGWINCH, as coding TUIs do.
    (t['root']/'grid.py').write_text('''import os,signal,time
redraw=True
def resized(*args):
 global redraw
 redraw=True
signal.signal(signal.SIGWINCH,resized)
while True:
 if redraw:
  redraw=False
  cols,rows=os.get_terminal_size()
  text='\\x1b[2J\\x1b[H'+''.join(f'\\x1b[{r+1};1HR{r:03d}='+chr(65+r%26)*(cols-5) for r in range(rows))
  os.write(1,text.encode())
 time.sleep(.03)
''')
    open_terminal(page, t)
    type_command(page, 'python3 -u grid.py')
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('R000=', timeout=10000)
    for width,height in [(1280,800),(390,750),(750,390),(430,850),(1100,760)]:
        page.set_viewport_size({'width':width,'height':height})
        # Let browser geometry and tmux's PTY resize reach the application.
        deadline = time.time()+5
        expected = []
        while time.time()<deadline:
            page.wait_for_timeout(100)
            expected = subprocess.check_output(['tmux','capture-pane','-p','-t','=terminal-test:'], env=t['env']).decode().splitlines()[:2]
            if len(expected)==2 and expected[0].startswith('R000=') and expected[1].startswith('R001='): break
        assert len(expected)==2 and expected[0].startswith('R000='), capture(t)
        try:
            page.wait_for_function('''lines => {
              const rows=[...document.querySelectorAll('#agent-terminal .xterm-rows>div')].map(r=>r.textContent.trimEnd());
              return lines.every(line=>rows.includes(line));
            }''', arg=expected, timeout=5000)
        except Exception:
            page.screenshot(path='/tmp/lectern-grid-failure.png')
            print('Viewport',width,height,'expected',expected)
            print('DOM',page.locator('#agent-terminal .xterm-screen').inner_html()[:3000])
            raise
    # A native terminal joins the very same tmux session, without detaching the
    # web client. Exercise larger and smaller native grids while the browser's
    # own viewport stays unchanged (there is no browser ResizeObserver event).
    import fcntl, pty, struct, termios, threading
    master, slave = pty.openpty()
    fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 50, 240, 0, 0))
    def native_tty():
        os.setsid()
        fcntl.ioctl(slave, termios.TIOCSCTTY, 0)
    native = subprocess.Popen([_binary(), 'attach', 'session', str(t['id'])],
        env={**t['env'], 'TERM':'xterm-256color'}, stdin=slave, stdout=slave, stderr=slave,
        preexec_fn=native_tty)
    os.close(slave)
    def drain():
        try:
            while os.read(master, 65536): pass
        except OSError: pass
    threading.Thread(target=drain, daemon=True).start()
    try:
        for cols, rows in [(240,50),(72,22),(180,44),(90,30)]:
            fcntl.ioctl(master, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))
            # Native attach wraps the session in a private tmux whose status
            # line shows the controls shortcut, so the client of the agent's
            # own tmux is one row shorter than the terminal around it.
            native_size = f'{cols}x{rows-1}'
            # Attachment includes an HTTP resolution step. Wait for the
            # actual client/resize instead of assuming it finished in 400ms.
            deadline=time.monotonic()+10
            while time.monotonic()<deadline:
                page.wait_for_timeout(100)
                assert native.poll() is None, 'native attachment exited'
                sizes = subprocess.check_output(['tmux','list-clients','-F','#{client_width}x#{client_height}'],env=t['env']).decode().splitlines()
                if native_size in sizes:break
            assert native_size in sizes, sizes
            page.evaluate("window.dispatchEvent(new Event('focus'))")
            expect(page.locator('#connection')).to_have_text('Connected')
            # Focus claims the shared window for the browser, whatever size
            # the native client is. Under `window-size smallest` a smaller
            # native client pinned the browser to its size, padded with dots.
            deadline=time.monotonic()+5
            while time.monotonic()<deadline:
                clients = subprocess.check_output(['tmux','list-clients','-F','#{client_width}x#{client_height}'],env=t['env']).decode().splitlines()
                browser_size = next((c for c in clients if c != native_size), clients[0])
                window = subprocess.check_output(['tmux','display-message','-p','-t','=terminal-test:','#{window_width}'],env=t['env']).decode().strip()
                if window == browser_size.split('x')[0]: break
                page.wait_for_timeout(100)
            assert window == browser_size.split('x')[0], (window, clients)
            expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('R000=', timeout=5000)
            # Rows have one label and one repeated glyph; overlapping redraws
            # produce stale labels or a second row's glyph on the same line.
            # Focus resizes the pane (and the claim steps it one row and back),
            # so the application redraws more than once: judge the screen
            # once it has settled, and fail only if it never comes out clean.
            def grid_problem():
                visible = page.locator('#agent-terminal .xterm-rows>div').all_text_contents()
                grid_rows = [line.rstrip() for line in visible if line.startswith('R')]
                if len(grid_rows) < 2:
                    return visible
                grid_cols = int(subprocess.check_output(['tmux','display-message','-p','-t','=terminal-test:', '#{pane_width}'],env=t['env']))
                for line in grid_rows:
                    number = int(line[1:4]); payload = line[5:grid_cols].rstrip()
                    if not payload or set(payload) != {chr(65+number%26)}:
                        return repr(line)
                return None
            deadline = time.monotonic()+5
            problem = grid_problem()
            while problem is not None and time.monotonic()<deadline:
                page.wait_for_timeout(100)
                problem = grid_problem()
            assert problem is None, problem
            clients = subprocess.check_output(['tmux','list-clients','-F','#{client_name}'],env=t['env']).decode().splitlines()
            assert len(clients)==2, clients
    finally:
        native.terminate();native.wait(timeout=5);os.close(master)
    page.wait_for_timeout(400)
    page.evaluate('window.staleSocket=terminalSockets.at(-1)')
    for _ in range(5): terminal_tool(page,'#reconnect')
    expect(page.locator('#connection')).to_have_text('Connected',timeout=20000)
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('R000=')
    # Delayed callbacks from the discarded stream must not clear or contaminate
    # the new screen, or schedule another connection behind the user's back.
    page.evaluate('''() => {
      staleSocket.onmessage({data:new TextEncoder().encode('0\\x1b[2J\\x1b[HSTALE-STREAM-CORRUPTION').buffer});
      staleSocket.onclose();
    }''')
    page.wait_for_timeout(700)
    expect(page.locator('#connection')).to_have_text('Connected')
    expect(page.locator('#agent-terminal .xterm-screen')).not_to_contain_text('STALE-STREAM-CORRUPTION')
    expected = subprocess.check_output(['tmux','capture-pane','-p','-t','=terminal-test:'],env=t['env']).decode().splitlines()[0].rstrip()
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text(expected)
    page.screenshot(path='/tmp/lectern-terminal-redraw.png')
    # Native and web input both reach the existing process; no replacement
    # session or browser reopening is involved.
    page.locator('#agent-terminal').click()
    page.keyboard.press('Control+c')
    type_command(page,'echo BROWSER-AFTER-NATIVE')
    expect(page.locator('#agent-terminal .xterm-screen')).to_contain_text('BROWSER-AFTER-NATIVE')
    assert t['proc'].poll() is None
    assert not errors
