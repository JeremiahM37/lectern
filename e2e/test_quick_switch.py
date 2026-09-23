"""Quick provider switching through real tmux agents, HTTP and SSE."""
import json
import re
import subprocess
import urllib.request

import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal


def prepare(t, tmp_path):
    runner = tmp_path / 'switch-agent.py'
    runner.write_text(r'''#!/usr/bin/env python3
import json,os,re,sys
from pathlib import Path
record=Path.cwd()/('agent-%s.json'%os.getpid())
record.with_suffix('.partial').write_text(json.dumps({'argv':sys.argv[1:],'provider':os.environ.get('SWITCH_PROVIDER'),'cwd':os.getcwd()}))
record.with_suffix('.partial').replace(record)
print('SWITCH AGENT READY',flush=True)
for line in sys.stdin:
    match=re.search(r'/tmp/lectern-handoff-[0-9]+-[a-z0-9]+\.md',line)
    if match:
        path=Path(match.group())
        body='## WHERE WE ARE\nKeep the amber configuration and the current worktree.\n## NEXT\nContinue the operator task.\n'
        part=Path(str(path)+'.partial')
        part.write_text(body+'<!-- lectern:complete '+str(path)+' -->\n')
        part.replace(path)
        print('WRAPPED',flush=True)
''')
    runner.chmod(0o755)
    specs=[{'name':name,'command':str(runner),'model_flag':flag,'prompt_arg':True,
            'models_command':"printf '%s' '[\"%s\"]'"%('%s', model)}
           for name,flag,model in [('claude','--model','fable'),('codex','-m','astra-test')]]
    req=urllib.request.Request(t['url']+'/api/agents',method='PUT',data=json.dumps(specs).encode(),headers={'Content-Type':'application/json'})
    with urllib.request.urlopen(req) as response: json.load(response)
    source=t['api']('/sessions',{'target_id':t['target_id'],'workdir':str(t['root']),'agent':'claude','model':'fable','name':'Switch proof'})
    return source


@pytest.mark.parametrize('width',[320,390,1440])
def test_quick_switch_carries_context_and_opens_successor(page,real_terminal,tmp_path,width):
    t=real_terminal;source=prepare(t,tmp_path)
    page.set_viewport_size({'width':width,'height':844})
    page.goto(f"{t['url']}/#terminals/session/{source['id']}")
    trigger=page.get_by_role('button',name='Switch agent or model')
    expect(trigger).to_contain_text('fable')
    box=trigger.bounding_box(); assert box['x']>=0 and box['x']+box['width']<=width
    assert page.evaluate('document.documentElement.scrollWidth<=innerWidth')
    trigger.click()
    sheet=page.get_by_role('dialog',name='Switch agent',exact=True)
    expect(sheet.locator('section[aria-label="Claude"]').get_by_role('button',name='fable Current')).to_be_disabled()
    sheet.locator('section[aria-label="Codex"]').get_by_role('button',name='astra-test',exact=True).click()
    expect(sheet).not_to_be_visible()
    expect(page).not_to_have_url(re.compile(f'/session/{source["id"]}$'),timeout=20000)
    next_id=int(page.url.rsplit('/',1)[-1])
    successor=t['api']('/sessions/'+str(next_id))
    assert successor['agent']=='codex' and successor['model']=='astra-test'
    assert successor['workdir']==source['workdir']
    frame=page.frame_locator(f'iframe[src="/terminal/session/{next_id}?embed=1"]')
    expect(frame.locator('.xterm-screen')).to_contain_text('SWITCH AGENT READY',timeout=15000)
    assert t['api']('/sessions/'+str(source['id']))['ended_at'] is None
    subprocess.run(['tmux','has-session','-t','='+source['tmux_session']],env=t['env'],check=True)
    records=[json.loads(p.read_text()) for p in t['root'].glob('agent-*.json')]
    assert any('-m' in r['argv'] and 'astra-test' in r['argv'] and any('amber configuration' in arg for arg in r['argv']) for r in records),records
    assert page.evaluate("JSON.parse(sessionStorage.getItem('lec-pending-switches'))")=={}
    page.screenshot(path=f'/tmp/lectern-quick-switch-{width}.png')


def test_saved_provider_survives_reload_and_switch_errors_are_visible(page,real_terminal,tmp_path):
    t=real_terminal;source=prepare(t,tmp_path)
    profile=t['api']('/launch-profiles',{'name':'DeepSeek','agent':'codex','model':'deepseek-test','env_json':'{"SWITCH_PROVIDER":"deepseek-fixture"}'})
    page.goto(f"{t['url']}/#terminals/session/{source['id']}")
    trigger=page.get_by_role('button',name='Switch agent or model');trigger.click()
    sheet=page.get_by_role('dialog',name='Switch agent',exact=True)
    expect(sheet.get_by_role('button',name=re.compile('^DeepSeek'))).to_be_visible()
    # A profile removed in another tab must fail visibly without touching the
    # original agent. Exercise the real API, including the service worker.
    req=urllib.request.Request(t['url']+'/api/launch-profiles/'+str(profile['id']),method='DELETE')
    with urllib.request.urlopen(req) as response: response.read()
    sheet.get_by_role('button',name=re.compile('^DeepSeek')).click()
    expect(sheet.get_by_role('alert')).to_contain_text('launch profile is unavailable')
    assert page.url.endswith('/session/'+str(source['id']))
    t['api']('/launch-profiles',{'name':'DeepSeek','agent':'codex','model':'deepseek-test','env_json':'{"SWITCH_PROVIDER":"deepseek-fixture"}'})
    sheet.get_by_role('button',name='Close switcher').click()
    trigger.click()
    sheet.get_by_role('button',name=re.compile('^DeepSeek')).click()
    expect(sheet).not_to_be_visible()
    page.reload()  # reconnect while the real agent writes its handoff
    expect(page).not_to_have_url(re.compile(f'/session/{source["id"]}$'),timeout=25000)
    successor=t['api']('/sessions/'+page.url.rsplit('/',1)[-1])
    assert successor['launch_profile']=='DeepSeek'
    assert successor['model']=='deepseek-test'
    records=[json.loads(p.read_text()) for p in t['root'].glob('agent-*.json')]
    assert any(r['provider']=='deepseek-fixture' for r in records),records
    assert t['api']('/sessions/'+str(source['id']))['ended_at'] is None
    page.goto(t['url']+'/#sessions')
    card=page.locator('.scard').filter(has_text='deepseek-test')
    card.get_by_role('button',name='⇄ Switch',exact=True).click()
    expect(sheet).to_be_visible()
    sheet.get_by_role('button',name='Close switcher').click()
    card.get_by_role('button',name='Chat',exact=True).click()
    page.locator('#conversation-input').fill('Keep this unsent draft')
    page.get_by_role('button',name='⇄ Switch',exact=True).last.click()
    expect(sheet).to_be_visible()
    sheet.get_by_role('button',name='Close switcher').click()
    card.get_by_role('button',name='Chat',exact=True).click()
    expect(page.locator('#conversation-input')).to_have_value('Keep this unsent draft')
