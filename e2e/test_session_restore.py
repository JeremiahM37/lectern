"""Recovery uses real tmux identity and retains the original durable record."""
import concurrent.futures
import json
import sqlite3
import socket
import threading
import time
import subprocess
import urllib.error
import urllib.request

import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_terminal_dashboard import Dashboard
from test_session_groups import patch


def request(t, method, suffix, body=None):
    req = urllib.request.Request(t['url']+'/api'+suffix, method=method,
        data=json.dumps(body).encode() if body is not None else None,
        headers={'Content-Type':'application/json'})
    try:
        with urllib.request.urlopen(req,timeout=20) as response:
            return response.status,json.load(response)
    except urllib.error.HTTPError as error:
        return error.code,json.load(error)


def test_restore_checks_identity_conflicts_and_preserves_record(real_terminal):
    t=real_terminal;path=f"/sessions/{t['id']}"
    patch(t,t['id'],{'name':'Original name','group_path':'Work/Backend'})
    original=t['api'](path)
    pid=subprocess.check_output(['tmux','display-message','-p','-t','=terminal-test:','#{pane_pid}'],env=t['env'])
    assert request(t,'DELETE',path)[0]==200
    assert t['api'](path)['can_restore']
    with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
        results=list(pool.map(lambda _:request(t,'POST',path+'/restore',{}),range(2)))
    assert sorted(status for status,_ in results)==[200,409]
    restored=t['api'](path)
    for field in ['id','name','group_path','workdir','created_at','project_id','target_id']:
        assert restored[field]==original[field]
    assert restored['ended_at'] is None
    assert subprocess.check_output(['tmux','display-message','-p','-t','=terminal-test:','#{pane_pid}'],env=t['env'])==pid
    assert 'tracking_identity' not in restored
    request(t,'DELETE',path)
    other=t['api']('/sessions/adopt',{'target_id':t['target_id'],'tmux_session':'terminal-test','workdir':str(t['root']),'name':'Explicit new record','agent':'claude'})
    assert request(t,'POST',path+'/restore',{})[0]==409
    request(t,'DELETE',f"/sessions/{other['id']}")
    # Re-adoption must preserve the existing marker, not strand the older record.
    assert request(t,'POST',path+'/restore',{})[0]==200
    request(t,'DELETE',path)
    subprocess.run(['tmux','kill-session','-t','=terminal-test'],env=t['env'],check=True)
    subprocess.run(['tmux','new-session','-d','-s','terminal-test','-c',str(t['root']),'bash --norc'],env=t['env'],check=True)
    status,error=request(t,'POST',path+'/restore',{})
    assert status==409 and 'gone or has changed' in error['detail']
    assert t['api'](path)['ended_at'] is not None
    assert request(t,'DELETE',path+'?kill=true')[0]==409
    assert request(t,'POST',path+'/terminal',{})[0]==409
    assert request(t,'POST',path+'/send',{'text':'do not send this'})[0]==409
    subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)


@pytest.mark.parametrize('width',[390,1440])
def test_web_can_track_again_from_untracked_records(page,real_terminal,width):
    t=real_terminal;patch(t,t['id'],{'group_path':'Work/Backend'})
    page.set_viewport_size({'width':width,'height':900});page.goto(t['url']+'/#sessions')
    page.locator('summary[aria-label="More actions for Real terminal"]').click()
    page.get_by_role('button',name='Stop tracking',exact=True).click()
    expect(page.locator('.scard')).to_have_count(0)
    pending=[]
    page.route('**/api/sessions?all=true',lambda route:pending.append(route))
    page.locator('#sess-scope').select_option('all')
    page.locator('#sess-search').fill('Real terminal')
    assert pending
    pending[0].continue_()
    page.unroute('**/api/sessions?all=true')
    expect(page.locator('#sess-search')).to_be_focused()
    expect(page.locator('#sess-search')).to_have_value('Real terminal')
    card=page.locator('.scard',has_text='Real terminal')
    expect(card).to_contain_text('untracked');expect(card).to_contain_text('Work/Backend')
    expect(card.get_by_role('button',name='⌨ Attach',exact=True)).to_have_count(0)
    card.get_by_role('button',name='Track again',exact=True).click()
    expect(card.get_by_role('button',name='⌨ Attach',exact=True)).to_be_visible()
    assert t['api'](f"/sessions/{t['id']}")['ended_at'] is None
    assert len(t['api']('/sessions?all=true'))==1


def test_console_restores_original_session_without_starting_a_process(real_terminal):
    t=real_terminal;request(t,'DELETE',f"/sessions/{t['id']}")
    d=Dashboard(t)
    try:
        d.wait('No matching items');d.send('z');d.wait('Real terminal')
        d.send('\r');d.wait('Track again');d.send('\r');d.wait('Track again completed')
        assert t['api'](f"/sessions/{t['id']}")['ended_at'] is None
        d.quit()
        subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)
    finally:d.close()


@pytest.mark.parametrize('offline',[False,True],ids=['legacy-live','unreachable-target'])
def test_legacy_live_release_captures_identity_without_blocking_offline(real_terminal,offline):
    t=real_terminal;path=f"/sessions/{t['id']}"
    # Simulate a record created by the pre-identity version, on real tmux.
    with sqlite3.connect(t['env']['LECTERN_DB']) as db:
        db.execute("UPDATE sessions SET tracking_identity='' WHERE id=?",(t['id'],))
    subprocess.run(['tmux','set-option','-u','-t','=terminal-test:','@lectern-tracking-identity'],env=t['env'],check=True)
    if offline:
        subprocess.run(['tmux','new-session','-d','-s','recovery-unrelated','-c',str(t['root']),'bash --norc'],env=t['env'],check=True)
        other=t['api']('/sessions/adopt',{'target_id':t['target_id'],'tmux_session':'recovery-unrelated','workdir':str(t['root']),'name':'Unrelated healthy session','agent':'claude'})
        key=t['root']/'recovery-key'
        subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-f',str(key)],check=True)
        listener=socket.socket();listener.bind(('127.0.0.1',0));listener.listen();listener.settimeout(6)
        finished=threading.Event();accepted=threading.Event()
        def stall_handshake():
            try:
                connection,_=listener.accept();accepted.set()
                with connection:finished.wait(6)
            except OSError:pass
        thread=threading.Thread(target=stall_handshake,daemon=True);thread.start()
        target=t['api']('/targets',{'name':'unreachable-recovery','kind':'ssh','host':'127.0.0.1','port':listener.getsockname()[1],'user':'nobody','key_path':str(key)})
        with sqlite3.connect(t['env']['LECTERN_DB']) as db:
            db.execute('UPDATE sessions SET target_id=? WHERE id=?',(target['id'],t['id']))
    start=time.monotonic()
    try:
        if offline:
            with concurrent.futures.ThreadPoolExecutor(max_workers=1) as pool:
                stalled=pool.submit(request,t,'DELETE',path)
                assert accepted.wait(2)
                healthy_start=time.monotonic()
                assert request(t,'DELETE',f"/sessions/{other['id']}")[0]==200
                assert time.monotonic()-healthy_start<1.5
                assert stalled.result(timeout=5)[0]==200
        else:
            assert request(t,'DELETE',path)[0]==200
    finally:
        if offline:finished.set();listener.close();thread.join(timeout=7)
    assert time.monotonic()-start<6
    released=t['api'](path);assert released['ended_at'] is not None
    assert released['can_restore']==(not offline)
    if not offline:
        assert request(t,'POST',path+'/restore',{})[0]==200
        assert t['api'](path)['id']==t['id']
    else:
        # Returning later must not manufacture identity on an already released row.
        with sqlite3.connect(t['env']['LECTERN_DB']) as db:
            db.execute('UPDATE sessions SET target_id=? WHERE id=?',(t['target_id'],t['id']))
        assert request(t,'DELETE',path)[0]==200
        assert request(t,'POST',path+'/restore',{})[0]==409
    subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)


def test_adopt_and_restore_do_not_poll_unrelated_targets(real_terminal):
    t=real_terminal;key=t['root']/'unrelated-key'
    subprocess.run(['ssh-keygen','-q','-t','ed25519','-N','','-f',str(key)],check=True)
    listener=socket.socket();listener.bind(('127.0.0.1',0));listener.listen();listener.settimeout(.1)
    try:
        target=t['api']('/targets',{'name':'unrelated-stalled','kind':'ssh','host':'127.0.0.1','port':listener.getsockname()[1],'user':'nobody','key_path':str(key)})
        with sqlite3.connect(t['env']['LECTERN_DB']) as db:
            db.execute("INSERT INTO sessions(target_id,name,tmux_session,workdir,agent,status,origin,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)",(target['id'],'Unrelated session','unrelated',str(t['root']),'claude','idle','discovered',time.time(),time.time()))
        subprocess.run(['tmux','new-session','-d','-s','targeted-adoption','-c',str(t['root']),'bash --norc'],env=t['env'],check=True)
        start=time.monotonic()
        adopted=t['api']('/sessions/adopt',{'target_id':t['target_id'],'tmux_session':'targeted-adoption','workdir':str(t['root']),'name':'Targeted adoption','agent':'claude'})
        assert time.monotonic()-start<2
        path=f"/sessions/{adopted['id']}"
        assert request(t,'DELETE',path)[0]==200
        start=time.monotonic()
        assert request(t,'POST',path+'/restore',{})[0]==200
        assert time.monotonic()-start<2
        with pytest.raises(socket.timeout):listener.accept()
        assert t['api'](path)['pane_tail']
    finally:listener.close()
