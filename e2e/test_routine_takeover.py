"""Take over an already-running routine through the actual browser and tmux."""
import subprocess
import time
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal

AGENT = '''#!/bin/bash
if [[ " $* " == *" -p "* ]]; then
 echo '{"type":"system","subtype":"init","session_id":"routine-thread-456"}'
 echo "prior edit" > existing-work.txt
 exec sleep 120
fi
printf 'argv:%s\\n' "$*" > takeover-log.txt
echo 'Interactive agent ready'
while IFS= read -r line; do printf 'typed:%s\\n' "$line" >> takeover-log.txt; done
'''

@pytest.mark.parametrize('real_terminal', [{'agent_script':AGENT}], indirect=True)
def test_take_over_started_routine_in_browser(page, real_terminal):
    t = real_terminal
    errors=[];page.on('pageerror',lambda e:errors.append(str(e)))
    for args in [['config','user.name','Lectern test'],['config','user.email','test@example.com'],['add','.'],['commit','-qm','initial']]:
        subprocess.run(['git','-C',str(t['root']),*args],check=True)
    branch=subprocess.check_output(['git','-C',str(t['root']),'branch','--show-current'],text=True).strip()
    p=t['api']('/projects',{'name':'Routine project','target_id':t['target_id'],'repo_path':str(t['root']),'default_base_branch':branch})
    r=t['api']('/routines',{'name':'Existing routine','prompt':'Keep the earlier edits','project_ids':[p['id']],'dispatch':True,'agent':'claude'})
    task_id=t['api'](f"/routines/{r['id']}/run",{})['tasks'][0]
    deadline=time.time()+10
    while time.time()<deadline:
        task=t['api'](f'/tasks/{task_id}')
        if task['status']=='running':break
        time.sleep(.1)
    assert task['status']=='running'
    page.goto(t['url'])
    page.locator('#qb-routines').click()
    page.get_by_role('button',name='Existing routine · Routine project · running',exact=True).click()
    page.get_by_role('button',name='Take over as session',exact=True).click()
    expect(page.get_by_role('button',name='Open session',exact=True)).to_be_visible(timeout=15000)
    page.get_by_role('button',name='Open session',exact=True).click()
    expect(page.locator('#conversation')).to_be_visible()
    page.locator('#conversation-input').fill('Continue with my changes')
    page.locator('#conversation-send').click()
    expect(page.locator('#conversation-receipt')).to_contain_text('Sent')
    from pathlib import Path
    wd=Path(task['attempt']['worktree_path'])
    deadline=time.time()+5
    while time.time()<deadline:
        log=(wd/'takeover-log.txt').read_text()
        if 'typed:Continue with my changes' in log:break
        time.sleep(.1)
    assert '--resume routine-thread-456' in log
    assert 'typed:Continue with my changes' in log
    assert (wd/'existing-work.txt').read_text()=='prior edit\n'
    page.screenshot(path='/tmp/lectern-routine-takeover.png')
    assert not errors
