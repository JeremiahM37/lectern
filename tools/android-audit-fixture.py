#!/usr/bin/env python3
"""Private native-device fixture. Invoked only by the reviewed isolated runner."""
import datetime
import json
import os
from pathlib import Path
import shutil
import shlex
import runpy
from types import SimpleNamespace
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request


def port():
    with socket.socket() as s:
        s.bind(('127.0.0.1', 0))
        return s.getsockname()[1]


def main():
    if os.environ.get('ADK_TEST_ISOLATED') != '1' or os.getuid() != 65534:
        raise SystemExit('Run through tools/run-isolated-tests.sh in android mode')
    assert not Path(os.environ['ADK_HOST_TMUX_SOCKET']).exists(), 'host tmux is visible'
    out = Path(os.environ['LEC_ANDROID_ARTIFACTS'])
    out.mkdir(parents=True, exist_ok=True)
    receipt = out / 'result.json'
    receipt.unlink(missing_ok=True)
    serial = os.environ['LEC_ANDROID_SERIAL']
    adb = os.environ['LEC_ANDROID_ADB']
    root = Path(tempfile.mkdtemp(prefix='lec-android-fixture-'))
    report = {'revision': os.environ.get('LEC_AUDIT_REVISION'), 'serial': serial,
              'started_at': datetime.datetime.now(datetime.timezone.utc).isoformat(),
              'ok': False, 'checks': []}
    server = None
    reverse = forward = None
    log = (out / 'fixture.log').open('w')
    def device(*args):
        return subprocess.check_output([adb, '-s', serial, *args], timeout=30)
    try:
        host_port, cdp_port = port(), port()
        base = f'http://127.0.0.1:{host_port}'
        env = dict(os.environ, LECTERN_HOST='127.0.0.1', LECTERN_PORT=str(host_port),
                   LECTERN_DB=str(root/'lectern.db'), LECTERN_MOCK='0',
                   LECTERN_GRIMOIRE_URL='', LECTERN_GRIMOIRE_TOKEN='',
                   LECTERN_CHECKPOINT='', LECTERN_AUTH_TOKEN='', LECTERN_SESSION_POLL='3600',
                   LECTERN_SCRATCH_ROOT=str(root/'scratch'),
                   CODEX_HOME=str(root/'codex'), CLAUDE_CONFIG_DIR=str(root/'claude'))
        def api(path, data=None):
            request = urllib.request.Request(base+'/api'+path,
                data=None if data is None else json.dumps(data).encode(),
                headers={'Content-Type':'application/json'})
            with urllib.request.urlopen(request, timeout=20) as response:
                return json.load(response)
        server = subprocess.Popen(['/tmp/lectern-audit'], cwd=root, env=env, stdout=log, stderr=log)
        for _ in range(150):
            if server.poll() is not None:
                raise RuntimeError('fixture exited; see fixture.log')
            try:
                api('/health'); break
            except OSError:
                time.sleep(.1)
        else:
            raise RuntimeError('fixture did not start')
        target = api('/targets', {'name':'android-audit', 'kind':'local'})
        sessions=[]
        for i in range(2):
            name=f'android-audit-{i}'
            subprocess.run(['tmux','-f','/dev/null','new-session','-d','-s',name,'-c',str(root),'bash','--norc'],env=env,check=True)
            sessions.append(api('/sessions/adopt', {'target_id':target['id'], 'tmux_session':name,
                'workdir':str(root), 'name':f'Android audit {i+1}', 'agent':'shell'}))
        reverse=f'tcp:{host_port}'
        device('reverse', reverse, reverse)
        forward=f'tcp:{cdp_port}'
        device('forward', forward, 'localabstract:chrome_devtools_remote')
        # A cold AVD snapshot may restore a stale Chrome ANR dialog. Restart
        # the browser on this explicit test emulator before opening our fixture.
        device('shell','am','force-stop','com.android.chrome')
        device('shell','am','start','-W','-a','android.intent.action.VIEW','-d',base,'com.android.chrome')
        for _ in range(100):
            try:
                with urllib.request.urlopen(f'http://127.0.0.1:{cdp_port}/json/list',timeout=2) as response:
                    pages = json.load(response)
                if any(tab.get('type') == 'page' and tab.get('url', '').startswith(base) for tab in pages):
                    break
            except OSError:
                pass
            time.sleep(.2)
        else:
            raise RuntimeError('Chrome CDP unavailable')
        # Full-screen prompt and gesture proof, using only this fixture's panes.
        from playwright.sync_api import sync_playwright, expect
        tui=root/'bottom-prompt.py'
        tui.write_text(r'''import os,signal,sys,termios,tty
old=termios.tcgetattr(0); tty.setraw(0); text=''
def draw(*_):
 rows,cols=os.get_terminal_size().lines,os.get_terminal_size().columns
 sys.stdout.write('\x1b[?1049h\x1b[?1000h\x1b[2J\x1b[HNative full-screen fixture\x1b[%d;1H> %s'%(rows,text));sys.stdout.flush()
signal.signal(signal.SIGWINCH,draw); draw()
try:
 while True:
  ch=os.read(0,1)
  if ch==b'\x03':break
  if ch.isalnum() or ch in (b' ',b'-'):text+=ch.decode()
  draw()
finally:
 sys.stdout.write('\x1b[?1000l\x1b[?1049l');sys.stdout.flush();termios.tcsetattr(0,termios.TCSADRAIN,old)
''')
        subprocess.run(['tmux','send-keys','-t','='+sessions[1]['tmux_session']+':0.0','-l','python3 '+str(tui)],env=env,check=True)
        subprocess.run(['tmux','send-keys','-t','='+sessions[1]['tmux_session']+':0.0','Enter'],env=env,check=True)
        print('Connecting full-screen Android audit',flush=True)
        with sync_playwright() as p:
            browser=p.chromium.connect_over_cdp(f'http://127.0.0.1:{cdp_port}',timeout=15000)
            print('Connected full-screen Android audit',flush=True)
            context=browser.contexts[0]
            page=next(page for page in context.pages if page.url.startswith(base))
            page.bring_to_front()
            # Keep one CDP connection through all stages. Reattaching another
            # Playwright driver to Android Chrome can stall on detached targets.
            runpy.run_path('/src/tools/android-terminal-audit.py', init_globals={
                '_audit_page': page,
                '_audit_args': SimpleNamespace(url=base,session=sessions[0]['id'],
                    serial=serial,adb=adb,cdp=f'http://127.0.0.1:{cdp_port}',
                    artifacts=out/'keyboard',allow_input=True),
            })
            report['checks'].extend(json.loads((out/'keyboard/result.json').read_text())['checks'])
            # Set resize policy before navigation. Updating the viewport meta
            # after load is ignored by some Chrome versions.
            cdp=context.new_cdp_session(page)
            cdp.send('Network.setBypassServiceWorker', {'bypass': True})
            def overlay_document(route):
                response=route.fetch()
                route.fulfill(response=response,body=response.text().replace('interactive-widget=resizes-content','interactive-widget=resizes-visual'))
            page.route('**/?keyboard=overlay',overlay_document)
            page.goto(f'{base}/?keyboard=overlay#terminals/session/{sessions[1]["id"]}')
            frame=page.frame_locator(f'iframe[src="/terminal/session/{sessions[1]["id"]}?embed=1"]')
            expect(frame.locator('.xterm-screen')).to_contain_text('Native full-screen fixture',timeout=20000)
            # Exercise browsers/PWAs where IME shrinks the visual viewport only.
            page.locator('meta[name=viewport]').evaluate("el=>el.content='width=device-width,initial-scale=1,viewport-fit=cover,interactive-widget=resizes-visual'")
            frame.locator('#agent-terminal').click()
            page.wait_for_function('visualViewport.height < 600')
            report['keyboard_viewport']=page.evaluate('({layout:innerHeight,visible:visualViewport.height})')
            assert report['keyboard_viewport']['layout']-report['keyboard_viewport']['visible']>150, report['keyboard_viewport']
            page.keyboard.type('VISIBLE-PROMPT')
            expect(frame.locator('.xterm-screen')).to_contain_text('VISIBLE-PROMPT')
            cursor=frame.locator('.xterm-cursor')
            expect(cursor).to_be_visible()
            # Native IME/compositor animation can lag DOM layout. Wait for it
            # before measuring or taking the device screenshot.
            page.wait_for_timeout(800)
            box=cursor.bounding_box()
            keybar=frame.locator('#terminal-keybar').bounding_box()
            viewport=page.evaluate('({top:visualViewport.offsetTop,height:visualViewport.height})')
            visible=viewport['top']+viewport['height']
            report['prompt_geometry']={'cursor':box,'keybar':keybar,'viewport':viewport}
            assert box and keybar and viewport['top']<=box['y'] and box['y']+box['height']<=keybar['y']+2 and keybar['y']+keybar['height']<=visible+2, (box,keybar,viewport)
            report['layout_geometry']=page.evaluate('''() => { const f=document.querySelector('iframe[src*="/session/2"]'), w=f.contentWindow, c=w.document.querySelector('.xterm-cursor'); const v=visualViewport; return { width:innerWidth,height:innerHeight,scrollY,viewport:{top:v.offsetTop,left:v.offsetLeft,height:v.height,width:v.width,scale:v.scale},frame:f.getBoundingClientRect().toJSON(),child:{width:w.innerWidth,height:w.innerHeight,scrollY:w.scrollY,cursor:c.getBoundingClientRect().toJSON()},body:document.body.getBoundingClientRect().toJSON()}; }''')
            (out/'full-screen-keyboard.png').write_bytes(device('exec-out','screencap','-p'))
            tools_box=frame.locator('#terminal-tools-summary').bounding_box()
            assert tools_box and viewport['top']<=tools_box['y'] and tools_box['y']+tools_box['height']<=visible+2, ('Tools hidden by keyboard',tools_box,viewport)
            frame.locator('#terminal-tools-summary').click()
            menu=frame.locator('#terminal-tools > .action-menu-panel')
            expect(menu).to_be_visible()
            page.wait_for_timeout(400)
            menu_box=menu.bounding_box()
            menu_viewport=page.evaluate('({top:visualViewport.offsetTop,height:visualViewport.height})')
            report['tools_geometry']={'menu':menu_box,'viewport':menu_viewport}
            assert menu_box and menu_viewport['top']-2<=menu_box['y'] and menu_box['y']+menu_box['height']<=menu_viewport['top']+menu_viewport['height']+2, ('Tools menu outside visible viewport',menu_box,menu_viewport)
            frame.locator('#terminal-tools-summary').click()
            report['checks'].append('Full-screen bottom prompt stays above real OS keyboard')
            if page.evaluate('visualViewport.height < 600'):
                frame.locator('#terminal-keyboard').click()
            page.wait_for_function('visualViewport.height > 650')
            page.locator('meta[name=viewport]').evaluate("el=>el.content='width=device-width,initial-scale=1,viewport-fit=cover,interactive-widget=resizes-content'")
            page.goto(f'{base}/#terminals/session/{sessions[1]["id"]}')
            expect(frame.locator('.xterm-screen')).to_contain_text('VISIBLE-PROMPT')
            bounds=frame.locator('.xterm-screen').bounding_box()
            # CDP exposes browser geometry; the actual gesture comes from ADB.
            metrics=page.evaluate('({dpr:devicePixelRatio,offset:screen.height-innerHeight})')
            y=int((bounds['y']+bounds['height']*.45+metrics['offset'])*metrics['dpr'])
            device('shell','input','swipe','250',str(y),'850',str(y),'250')
            expect(page).to_have_url(f'{base}/#terminals/session/{sessions[0]["id"]}',timeout=10000)
            report['checks'].append('Native horizontal body swipe switches a mouse-reporting full-screen terminal')
            # Host connectivity remains untouched; Chromium's context goes offline.
            context.set_offline(True)
            frame0=page.frame_locator(f'iframe[src="/terminal/session/{sessions[0]["id"]}?embed=1"]')
            try:
                expect(frame0.locator('#connection')).not_to_have_text('Connected',timeout=15000)
            finally:
                context.set_offline(False)
            expect(frame0.locator('#connection')).to_have_text('Connected',timeout=20000)
            device('shell','input','keyevent','3')
            device('shell','am','start','-a','android.intent.action.VIEW','-d',page.url,'com.android.chrome')
            expect(frame0.locator('#connection')).to_have_text('Connected',timeout=20000)
            report['checks'].append('Offline/online and background/foreground restore the same terminal')
            page.bring_to_front()
            # A WebView/browser shell may overlay the IME without resizing
            # either viewport. The OS keyboard geometry must still fit the PTY.
            page.goto(f'{base}/#terminals/session/{sessions[1]["id"]}')
            frame=page.frame_locator(f'iframe[src="/terminal/session/{sessions[1]["id"]}?embed=1"]')
            expect(frame.locator('#connection')).to_have_text('Connected',timeout=20000)
            page.reload()
            expect(frame.locator('#connection')).to_have_text('Connected',timeout=20000)
            page.evaluate('navigator.virtualKeyboard.overlaysContent=true')
            frame.locator('#agent-terminal').click()
            page.wait_for_function('navigator.virtualKeyboard.boundingRect.height > 200')
            page.keyboard.type('OVERLAY-VISIBLE')
            expect(frame.locator('.xterm-screen')).to_contain_text('OVERLAY-VISIBLE')
            page.wait_for_timeout(800)
            keyboard=page.evaluate('navigator.virtualKeyboard.boundingRect.toJSON()')
            cursor=frame.locator('.xterm-cursor').bounding_box()
            keys=frame.locator('#terminal-keybar').bounding_box()
            report['overlay_geometry']={'keyboard':keyboard,'cursor':cursor,'keybar':keys,
                'viewport':page.evaluate('({layout:innerHeight,visual:visualViewport.height})')}
            (out/'overlay-keyboard.png').write_bytes(device('exec-out','screencap','-p'))
            # Chrome 148's boundingRect.y is a window inset (upstream
            # crbug.com/493416495), not keyboard top. Its docked IME height
            # is correct; ADB screenshot includes the actual OS boundary.
            keyboard_top=page.evaluate('innerHeight')-keyboard['height']
            assert cursor and keys and cursor['y']+cursor['height']<=keys['y']+2 and keys['y']+keys['height']<=keyboard_top+2, report['overlay_geometry']
            report['checks'].append('Overlay keyboard geometry keeps full-screen input visible without viewport resizing')
            frame.locator('#terminal-keyboard').click()
            page.evaluate('navigator.virtualKeyboard.overlaysContent=false')
            if shutil.which('claude'):
                # Optional real renderer, mounted explicitly by the isolated
                # runner. Private config/dummy key; never submit a prompt.
                home=root/'claude-input-audit';home.mkdir()
                (home/'.claude.json').write_text(json.dumps({'hasCompletedOnboarding':True,'theme':'dark','numStartups':1}))
                claude_env=dict(env, ANTHROPIC_API_KEY='lectern-keyboard-audit',
                    ANTHROPIC_BASE_URL='http://127.0.0.1:1',CLAUDE_CONFIG_DIR=str(home),
                    DISABLE_AUTOUPDATER='1',CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC='1')
                command='env '+ ' '.join(shlex.quote(k+'='+claude_env[k]) for k in
                    ['ANTHROPIC_API_KEY','ANTHROPIC_BASE_URL','CLAUDE_CONFIG_DIR','DISABLE_AUTOUPDATER','CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC'])+' claude --setting-sources ""'
                subprocess.run(['tmux','new-session','-d','-s','android-audit-claude','-c',str(root),command],env=env,check=True)
                session=api('/sessions/adopt', {'target_id':target['id'],'tmux_session':'android-audit-claude',
                    'workdir':str(root),'name':'Claude input audit','agent':'claude'})
                page.goto(f'{base}/#terminals/session/{session["id"]}')
                claude=page.frame_locator(f'iframe[src="/terminal/session/{session["id"]}?embed=1"]')
                expect(claude.locator('.xterm-screen')).to_contain_text('trust',timeout=20000)
                claude.locator('#agent-terminal').click()
                page.keyboard.press('ArrowDown');page.keyboard.press('Enter')
                expect(claude.locator('.xterm-screen')).to_contain_text('custom API key',timeout=20000)
                page.keyboard.press('ArrowUp');page.keyboard.press('Enter')
                expect(claude.locator('.xterm-screen')).to_contain_text('? for shortcuts',timeout=20000)
                page.keyboard.type('Unsent keyboard visibility probe '*20+' CLAUDE-DRAFT')
                def check_claude(marker, filename):
                    row=claude.locator('.xterm-rows > div').filter(has_text=marker).last
                    expect(row).to_be_visible()
                    page.wait_for_timeout(800)
                    line=row.bounding_box();bar=claude.locator('#terminal-keybar').bounding_box()
                    limit=page.evaluate('navigator.virtualKeyboard.overlaysContent ? innerHeight-navigator.virtualKeyboard.boundingRect.height : visualViewport.offsetTop+visualViewport.height')
                    assert line and bar and line['y']+line['height']<=bar['y']+2 and bar['y']+bar['height']<=limit+2,(line,bar,limit)
                    (out/filename).write_bytes(device('exec-out','screencap','-p'))
                check_claude('CLAUDE-DRAFT','claude-keyboard.png')
                report['checks'].append('Real Claude long unsent draft stays above native Gboard')
                claude.locator('#terminal-keyboard').click()
                page.wait_for_function('innerHeight > 650')
                page.evaluate('navigator.virtualKeyboard.overlaysContent=true')
                claude.locator('#agent-terminal').click()
                page.wait_for_function('navigator.virtualKeyboard.boundingRect.height > 200')
                page.keyboard.type(' CLAUDE-OVERLAY')
                check_claude('CLAUDE-OVERLAY','claude-overlay-keyboard.png')
                report['checks'].append('Real Claude input stays above native overlay Gboard')
                claude.locator('#terminal-keyboard').click()
                page.evaluate('navigator.virtualKeyboard.overlaysContent=false')
            page.close()
        report['ok']=True
    except Exception as exc:
        report['error']=str(exc)
        try:(out/'failure.png').write_bytes(device('exec-out','screencap','-p'))
        except Exception:pass
        raise
    finally:
        # A nightly run must not accumulate Chrome tabs. Close only tabs for
        # this fixture's unique origin, including after an assertion failure.
        if forward:
            try:
                from playwright.sync_api import sync_playwright
                with sync_playwright() as p:
                    browser=p.chromium.connect_over_cdp(f'http://127.0.0.1:{cdp_port}', timeout=5000)
                    for context in browser.contexts:
                        for page in context.pages:
                            if page.url.startswith(base + '/'):
                                page.close()
            except Exception:
                pass
        if server:
            server.terminate()
            try:server.wait(timeout=10)
            except subprocess.TimeoutExpired:server.kill();server.wait()
        # Only this namespace's tmux socket exists here; parent death also reaps
        # every fixture process. Never issue a host kill-server or emulator kill.
        for name in ['android-audit-0','android-audit-1','android-audit-claude']:
            subprocess.run(['tmux','kill-session','-t','='+name],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
        for operation,spec in [('reverse',reverse),('forward',forward)]:
            if spec:
                try:device(operation,'--remove',spec)
                except Exception:pass
        log.close()
        report['finished_at']=datetime.datetime.now(datetime.timezone.utc).isoformat()
        receipt.with_suffix('.tmp').write_text(json.dumps(report,indent=2)+'\n')
        receipt.with_suffix('.tmp').replace(receipt)
        print(f'{"PASS" if report["ok"] else "FAIL"}: native Android audit ({len(report["checks"])} checks)',flush=True)

if __name__=='__main__':main()
