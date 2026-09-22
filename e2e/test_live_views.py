"""Reaching a target's localhost, and watching a desktop on it, from the browser."""
import json
import os
import re
import shutil
import socket
import subprocess
import sys
import time
import urllib.request

import pytest
from playwright.sync_api import expect
from conftest import _binary
from test_terminal_workspace import real_terminal

LIVE=[{'live':True}]

DESKTOP_TOOLS=all(shutil.which(b) for b in ('Xvfb','x11vnc','websockify'))


@pytest.fixture()
def loopback_app(tmp_path):
    # An application the way agents leave them: bound to 127.0.0.1 only, with an
    # absolute asset path. A path-prefix proxy breaks exactly this.
    site=tmp_path/'site';(site/'assets').mkdir(parents=True)
    (site/'index.html').write_text('<title>Loopback app</title><h1 id="app">served from loopback</h1><script src="/assets/app.js"></script>')
    (site/'assets'/'app.js').write_text('document.body.dataset.assets="loaded";')
    with socket.socket() as s:s.bind(('127.0.0.1',0));port=s.getsockname()[1]
    proc=subprocess.Popen([sys.executable,'-m','http.server',str(port),'--bind','127.0.0.1','--directory',str(site)],
                          stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    for _ in range(100):
        try:urllib.request.urlopen(f'http://127.0.0.1:{port}/',timeout=1).read();break
        except Exception:time.sleep(.05)
    yield port
    proc.terminate();proc.wait(timeout=10)


def cli(t,*args):
    out=subprocess.run([_binary(),*args],env={**t['env'],'LECTERN_API':t['url'],'TMUX':''},capture_output=True,text=True,timeout=90)
    assert out.returncode==0,out.stderr
    return json.loads(out.stdout) if out.stdout.strip() else None


@pytest.mark.parametrize('real_terminal',LIVE,indirect=True)
def test_a_posted_localhost_link_becomes_reachable_with_one_click(page,real_terminal,loopback_app):
    t=real_terminal;errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
    cli(t,'post',f'http://127.0.0.1:{loopback_app}/index.html','--title','Dev server','--session',str(t['id']))
    page.goto(t['url']+'/#media')
    card=page.locator('.media-card[data-kind="link"]')
    # Nothing is exposed because a link was posted: that takes a person.
    assert t['api']('/live')['views']==[]
    card.get_by_role('button',name=f'Expose localhost:{loopback_app} so this device can open it').click()
    live=page.locator('.live-card[data-kind="port"]')
    expect(live).to_contain_text('exposed',timeout=10000);expect(live).to_contain_text(f'localhost:{loopback_app}')
    view=t['api']('/live')['views'][0]
    assert view['port']==loopback_app and view['session_id']==t['id'] and view['listen_port']!=loopback_app,view
    # The posted link now points at the forwarded port, path intact.
    expect(card.get_by_role('link')).to_have_attribute('href',f'http://127.0.0.1:{view["listen_port"]}/index.html')
    # And the application works through it untouched: its absolute asset path
    # resolves against the forwarded port and its script really runs.
    with page.context.expect_page() as opened:card.get_by_role('link').click()
    app=opened.value
    expect(app.locator('#app')).to_have_text('served from loopback')
    expect(app.locator('body')).to_have_attribute('data-assets','loaded')
    app.close()
    live.get_by_role('button',name='Stop Dev server').click()
    expect(page.locator('.live-card')).to_have_count(0)
    with pytest.raises(OSError):socket.create_connection(('127.0.0.1',view['listen_port']),timeout=2)
    # The CLI is the same door, and knows its session the same way a post does.
    made=cli(t,'expose',str(loopback_app),'--title','From the CLI','--session',str(t['id']))
    assert made['kind']=='port' and made['session_id']==t['id']
    assert [v['id'] for v in cli(t,'live','list')['views']]==[made['id']]
    cli(t,'live','stop',str(made['id']))
    assert t['api']('/live')['views']==[]
    assert not errors,errors


@pytest.mark.parametrize('real_terminal',LIVE,indirect=True)
def test_forwards_are_confined_to_the_targets_loopback(real_terminal):
    t=real_terminal
    def post(path,body):
        req=urllib.request.Request(t['url']+'/api'+path,data=json.dumps(body).encode(),headers={'Content-Type':'application/json'})
        try:return urllib.request.urlopen(req).status
        except urllib.error.HTTPError as e:return e.code
    assert post('/live/ports',{'port':0})==422
    assert post('/live/ports',{'port':70000})==422
    assert post('/live/ports',{'port':80,'session_id':99999})==422
    assert t['api']('/live')['views']==[]


@pytest.mark.skipif(not DESKTOP_TOOLS,reason='needs Xvfb, x11vnc and websockify for a real desktop')
@pytest.mark.parametrize('real_terminal',LIVE,indirect=True)
def test_a_live_desktop_shows_what_runs_on_the_target(page,real_terminal,loopback_app):
    t=real_terminal;errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
    page.goto(t['url']+'/#media')
    page.locator('#live-desktop').click()
    card=page.locator('.live-card[data-kind="desktop"]')
    expect(card).to_be_visible(timeout=30000)
    view=t['api']('/live')['views'][0];display=view['detail']['display']
    try:
        expect(card).to_contain_text(f'DISPLAY={display}')
        # The embedded client really connects to the display, not merely loads.
        vnc=card.frame_locator('iframe')
        expect(vnc.locator('html.noVNC_connected')).to_have_count(1,timeout=30000)
        assert 'view_only=1' in card.locator('iframe').get_attribute('src'),'a watcher must not be able to type into the run'
        # Anything started with that DISPLAY draws there.
        assert subprocess.run(['xdpyinfo'],env={**t['env'],'DISPLAY':display},capture_output=True).returncode==0 if shutil.which('xdpyinfo') else True
        # A browser on the desktop sees the target's own localhost — the app that
        # no other machine can reach opens there with no forward at all.
        card.get_by_label("Open an address in this desktop's browser").fill(f'http://127.0.0.1:{loopback_app}/index.html')
        with page.expect_response(lambda r:r.url.endswith(f'/live/{view["id"]}/browser')) as opened:
            card.get_by_role('button',name='Open',exact=True).click()
        if opened.value.status==200:
            deadline=time.time()+20;seen=False
            while time.time()<deadline and not seen:
                # xdotool prints the ids of windows whose title matches; any id means
                # the page is really on that display.
                seen=not shutil.which('xdotool') or bool(subprocess.run(['xdotool','search','--name','Loopback app'],env={**t['env'],'DISPLAY':display},capture_output=True,text=True).stdout.strip())
                time.sleep(.5)
            assert seen,'the browser never showed the loopback app on the desktop'
        else:
            assert 'no browser is installed' in opened.value.text()
        card.get_by_role('button',name='Take control').click()
        assert 'view_only=0' in card.locator('iframe').get_attribute('src')
    finally:
        page.locator('.live-card').get_by_role('button',name='Stop Live desktop').click()
    expect(page.locator('.live-card')).to_have_count(0)
    # Stopping takes the display and everything in it down with it.
    number=display.lstrip(':')
    for _ in range(50):
        if not os.path.exists(f'/tmp/.X11-unix/X{number}'):break
        time.sleep(.1)
    assert not os.path.exists(f'/tmp/.X11-unix/X{number}')
    assert not errors,errors


def test_live_views_are_not_offered_until_the_operator_turns_them_on(page,real_terminal,loopback_app):
    # The fixture's server is a default one: nothing here asked for live views.
    t=real_terminal
    assert t['api']('/live')=={'enabled':False,'views':[]}
    cli(t,'post',f'http://127.0.0.1:{loopback_app}/','--title','Dev server','--session',str(t['id']))
    page.goto(t['url']+'/#media')
    card=page.locator('.media-card[data-kind="link"]')
    expect(card).to_contain_text('Dev server')
    # No bar to start one, and no button on the link that could not work.
    expect(page.locator('#live')).to_have_count(0)
    expect(card.get_by_role('button',name=re.compile('Expose'))).to_have_count(0)
    out=subprocess.run([_binary(),'expose',str(loopback_app)],env={**t['env'],'LECTERN_API':t['url'],'TMUX':''},capture_output=True,text=True,timeout=60)
    assert out.returncode!=0 and 'LECTERN_LIVE=1' in out.stderr,out.stderr
