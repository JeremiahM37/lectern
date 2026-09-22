"""Agents posting evidence: the real CLI, a real recording, a real browser."""
import json
import shlex
import shutil
import subprocess
import time

import pytest
from playwright.sync_api import expect
from conftest import _binary
from test_terminal_workspace import real_terminal


def post(t, *args, **kw):
    env={**t['env'],'LECTERN_API':t['url'],'TMUX':''}
    out=subprocess.run([_binary(),'post',*args],env=env,capture_output=True,text=True,timeout=60,**kw)
    assert out.returncode==0,out.stderr
    return json.loads(out.stdout)


def recording(path):
    # A real, seekable H.264 file: a fake one would prove nothing about playback.
    subprocess.run(['ffmpeg','-loglevel','error','-y','-f','lavfi','-i','testsrc=duration=4:size=320x240:rate=15',
                    '-pix_fmt','yuv420p','-movflags','+faststart',str(path)],check=True,timeout=120)
    return path


@pytest.mark.skipif(not shutil.which('ffmpeg'),reason='needs ffmpeg to make a real recording')
def test_posted_recording_plays_and_seeks_in_the_media_feed(page,real_terminal,tmp_path):
    t=real_terminal;errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
    page.goto(t['url']+'/#media')
    expect(page.locator('#media')).to_contain_text('Nothing posted yet')
    video=recording(tmp_path/'demo.mp4')
    row=post(t,str(video),'--title','Checkout flow passing','--note','Watch the total update','--session',str(t['id']))
    assert row['mime']=='video/mp4' and row['session_id']==t['id'],row
    video.unlink()  # the feed must not depend on the agent's copy surviving
    # No reload: the post has to arrive over the live stream.
    card=page.locator(f'.media-card[data-media-id="{row["id"]}"]')
    expect(card).to_contain_text('Checkout flow passing',timeout=10000)
    expect(card).to_contain_text('Real terminal');expect(card).to_contain_text('Watch the total update')
    player=card.locator('video')
    expect(player).to_be_visible()
    player.evaluate('(v)=>new Promise((ok,fail)=>{if(v.readyState>=1)return ok();v.onloadedmetadata=()=>ok();v.onerror=()=>fail(new Error("video failed to load"));})')
    assert player.evaluate('(v)=>v.duration')>3
    # Seeking past what has buffered only works if the server answers ranges.
    seeked=player.evaluate('(v)=>new Promise((ok)=>{v.onseeked=()=>ok(v.currentTime);v.currentTime=3;})')
    assert seeked>=2.9,seeked
    expect(page.locator('#media-badge')).to_have_text('1')
    # The session card points at what its agent posted.
    page.locator('.tab[data-tab="sessions"]').click()
    page.locator('.scard',has_text='Real terminal').locator('.media-chip').click()
    expect(page.locator('.media-card')).to_have_count(1)
    assert page.evaluate('location.hash')==f'#media/{t["id"]}'
    page.once('dialog',lambda d:d.accept())
    card.get_by_role('button',name='Delete Checkout flow passing').click()
    expect(page.locator('.media-card')).to_have_count(0)
    assert t['api']('/media')==[]
    assert not errors,errors


def test_a_post_from_inside_a_session_knows_which_session_it_is(page,real_terminal,tmp_path):
    t=real_terminal
    report=tmp_path/'report.html';report.write_text('<h1 id="proof">All 42 checks passed</h1><script>document.title=String(window.parent===window)</script>')
    # Typed into the agent's own pane, with no --session: the poster has to work
    # out where it is from tmux, exactly as an agent's tool call does.
    command=' '.join(['LECTERN_API='+shlex.quote(t['url']),shlex.quote(_binary()),'post',shlex.quote(str(report)),'--title',shlex.quote('Test report')])
    subprocess.run(['tmux','send-keys','-t','=terminal-test:',command,'Enter'],env=t['env'],check=True)
    for _ in range(100):
        rows=t['api']('/media')
        if rows: break
        time.sleep(.1)
    assert rows and rows[0]['session_id']==t['id'] and rows[0]['source']=='cli',rows
    link=post(t,'http://127.0.0.1:5173/cart','--title','Dev server')
    assert link['kind']=='link' and link['session_id'] is None,link
    page.goto(t['url']+'/#media')
    shown=page.locator(f'.media-card[data-media-id="{rows[0]["id"]}"]')
    frame=shown.frame_locator('iframe')
    expect(frame.locator('#proof')).to_have_text('All 42 checks passed',timeout=10000)
    # An agent's HTML renders, but never as the Lectern origin it is served from.
    assert shown.locator('iframe').get_attribute('sandbox')=='allow-scripts'
    origin=page.evaluate('''async (id)=>{const r=await fetch(`/api/media/${id}/content`);return r.headers.get('content-security-policy');}''',rows[0]['id'])
    assert origin.startswith('sandbox'),origin
    dev=page.locator(f'.media-card[data-media-id="{link["id"]}"]')
    expect(dev.get_by_role('link')).to_have_attribute('href','http://127.0.0.1:5173/cart')
