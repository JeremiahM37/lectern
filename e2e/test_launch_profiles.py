import json,time,urllib.request,uuid
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_terminal_dashboard import Dashboard
from test_command_palette import search


def setup_profile_agent(t):
    root=t['root'];agent=root/'profile-agent';home=root/'profile-home';(home/'sessions').mkdir(parents=True)
    agent.write_text('#!/usr/bin/env python3\nimport os,sys,json,time\nfrom pathlib import Path\np=Path(os.environ["PROFILE_PROOF"])/("run-"+str(os.getpid())+".json")\np.write_text(json.dumps({"argv":sys.argv[1:],"cwd":os.getcwd(),"home":os.environ["CODEX_HOME"],"secret":os.environ["PROFILE_SECRET"]}))\nprint("PROFILE READY",flush=True)\nwhile True:time.sleep(1)\n');agent.chmod(0o755)
    env={'CODEX_HOME':str(home),'PROFILE_PROOF':str(root),'PROFILE_SECRET':'profile-private-sentinel'}
    cid=str(uuid.uuid4());source=home/'sessions'/(cid+'.jsonl')
    source.write_text(json.dumps({'type':'session_meta','payload':{'id':cid,'cwd':str(root)}})+'\n'+json.dumps({'type':'response_item','payload':{'type':'message','role':'assistant','channel':'final','content':[{'type':'output_text','text':'Saved profile conversation'}]}})+'\n')
    return agent,env,cid,source


def wait_record(t,n):
    for _ in range(100):
        files=list(t['root'].glob('run-*.json'))
        if len(files)>=n:return [json.loads(p.read_text()) for p in files]
        time.sleep(.1)
    raise AssertionError('profile command did not start')


@pytest.mark.parametrize('width',[390,1440])
def test_web_profiles_keep_drafts_launch_and_preserve_continuation(page,real_terminal,width):
    t=real_terminal;agent,env,cid,source=setup_profile_agent(t);original=source.read_bytes();errors=[]
    project=t['api']('/projects',{'name':'Profile workspace','target_id':t['target_id'],'repo_path':str(t['root'])})
    page.on('pageerror',lambda e:errors.append(str(e)));page.set_viewport_size({'width':width,'height':844});page.goto(t['url']+'/#sessions')
    page.locator('#sess-new').click();page.locator('#ns-name').fill('Profile web session')
    page.locator('#ns-manage-profiles').click();d=page.get_by_role('dialog',name='Launch profiles',exact=True)
    expect(d.locator('.lp-agent')).to_contain_text('codex')
    briefing = 'PROFILE-BRIEFING proof: keep the user task; literal $(echo unsafe) and single quote \' stay text.'
    d.get_by_label('Description',exact=True).fill('Focused profile proof')
    d.get_by_label('Workflow instructions',exact=True).fill(briefing)
    d.get_by_label('Name',exact=True).fill('Work account');d.get_by_label('Agent',exact=True).select_option('codex')
    d.get_by_label('Command override').fill(str(agent));d.get_by_label('Default model').fill('profile-model');d.get_by_label('Environment (JSON)').fill(json.dumps(env))
    def reject(route):
        if route.request.method=='POST':route.fulfill(status=503,content_type='application/json',body='{"detail":"Temporary save failure"}')
        else:route.continue_()
    page.route('**/api/launch-profiles',reject)
    d.get_by_role('button',name='Save profile',exact=True).click();expect(d.locator('.lp-status')).to_contain_text('Temporary save failure')
    expect(d.get_by_label('Name',exact=True)).to_have_value('Work account');expect(d.get_by_label('Environment (JSON)')).to_have_value(json.dumps(env))
    page.unroute('**/api/launch-profiles',reject)
    page.evaluate('''()=>{const f=window.fetch;window.fetch=(u,o)=>String(u).endsWith('/launch-profiles')&&o?.method==='POST'?new Promise(r=>setTimeout(()=>r(f(u,o)),400)):f(u,o)}''')
    with page.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/launch-profiles')) as response:
        d.get_by_role('button',name='Save profile',exact=True).click();expect(d.get_by_role('button',name='Close',exact=True)).to_be_disabled();page.keyboard.press('Escape');expect(d).to_be_visible()
    profile=response.value.json();expect(d.locator('.lp-status')).to_contain_text('Profile saved')
    assert d.evaluate('(e)=>e.scrollWidth<=e.clientWidth')
    page.screenshot(path=f'/tmp/lectern-launch-profiles-{width}.png')
    d.get_by_role('button',name='Close',exact=True).click()
    expect(page.locator('#ns-name')).to_have_value('Profile web session');expect(page.locator('#ns-profile')).to_have_value(str(profile['id']))
    expect(page.locator('#ns-agent')).to_have_value('codex');expect(page.locator('#ns-agent')).to_be_disabled()
    page.locator('#ns-yolo').uncheck();page.locator('#ns-model').fill('explicit-model')
    with page.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/sessions')) as response:page.locator('#ns-go').click()
    row=response.value.json();assert response.value.status==201,row
    observed=wait_record(t,1)[0];assert 'explicit-model' in observed['argv'] and observed['home']==env['CODEX_HOME'] and observed['cwd']==str(t['root'])
    assert sum(arg.count(briefing) for arg in observed['argv']) == 1
    assert row['launch_profile']=='Work account' and 'profile-private-sentinel' not in json.dumps(row)
    search(page,'manage launch profiles').get_by_role('option').click();d=page.get_by_role('dialog',name='Launch profiles',exact=True)
    expect(d.locator('.lp-select')).to_contain_text('Work account');d.locator('.lp-select').select_option(str(profile['id']))
    d.get_by_label('Workflow instructions',exact=True).fill('EDITED-BRIEFING must not reach a continuation')
    d.get_by_label('Command override').fill('false');d.get_by_role('button',name='Save profile',exact=True).click();expect(d.locator('.lp-status')).to_contain_text('Profile saved')
    page.once('dialog',lambda dialog:dialog.accept());d.get_by_role('button',name='Delete profile',exact=True).click();expect(d.locator('.lp-status')).to_contain_text('Profile deleted');d.get_by_role('button',name='Close',exact=True).click()
    child=t['api'](f"/sessions/{row['id']}/fork",{'conversation_id':cid,'name':'Profile continuation'})
    records=wait_record(t,2);assert any(cid in r['argv'] for r in records)
    fork_record=next(r for r in records if cid in r['argv'])
    assert sum(arg.count(briefing) for arg in fork_record['argv']) == 1
    assert 'EDITED-BRIEFING' not in json.dumps(fork_record)
    assert child['launch_profile']=='Work account' and source.read_bytes()==original and not errors


def test_terminal_manages_profile_and_launches_with_it(real_terminal):
    t=real_terminal;agent,env,cid,source=setup_profile_agent(t);d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('P');d.wait('Launch profiles — reusable');d.send('\x13');d.wait('Profile name')
        d.send('Terminal account\t\x1b[C\t'+str(agent)+'\tprofile-model\t\x01\x0b'+json.dumps(env)+'\x13')
        d.wait('Save launch profile completed');assert len(t['api']('/launch-profiles'))==1
        d.send('n');d.wait('Launch profile:');d.send('Profile terminal\t\x1b[C'+'\t'*5+str(t['root'])+'\x13');d.wait('Create session completed',timeout=20)
        row=next(r for r in t['api']('/sessions') if r['name']=='Profile terminal');assert row['agent']=='codex' and row['model']=='profile-model'
        assert wait_record(t,1)[0]['home']==env['CODEX_HOME']
        d.send('P');d.wait('Launch profiles — reusable');d.send('\x1b[C\x13');d.wait('Delete Terminal account?');d.send('y');d.wait('Delete launch profile completed')
        assert not t['api']('/launch-profiles')
        child=t['api'](f"/sessions/{row['id']}/fork",{'conversation_id':cid,'name':'Terminal profile continuation'})
        assert child['launch_profile']=='Terminal account';assert any(cid in r['argv'] for r in wait_record(t,2))
        d.quit()
    finally:d.close()
