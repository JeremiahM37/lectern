"""Grouped review against real owned worktrees and a live tmux session."""
from pathlib import Path
import subprocess
import time
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal, terminal_tool
from test_interactive_worktree import setup
from test_terminal_dashboard import Dashboard
from session_sheet import open_advanced


def grouped(t):
    primary,_=setup(t)
    other=t['root'].parent/'other';other.mkdir()
    subprocess.run(['git','init','-q',str(other)],check=True)
    (other/'second.txt').write_text('original\n')
    subprocess.run(['git','-C',str(other),'add','.'],check=True)
    subprocess.run(['git','-C',str(other),'-c','user.name=Test','-c','user.email=test@example.invalid','commit','-qm','base'],check=True)
    extra=t['api']('/projects',{'name':'Second repository','target_id':t['target_id'],'repo_path':str(other)})
    row=t['api']('/sessions',{'name':'Grouped review','project_id':primary['id'],'worktree':{'extra_repositories':[{'project_id':extra['id']}]}})
    repos=row['workspace']['repositories']
    (Path(repos[0]['worktree']['path'])/'first.txt').write_text('PRIMARY DIFF SENTINEL\n')
    (Path(repos[1]['worktree']['path'])/'second.txt').write_text('SECOND DIFF SENTINEL\n')
    return row


@pytest.mark.parametrize('width',[390,1440])
def test_grouped_browser_review_switches_repository(page,real_terminal,width):
    t=real_terminal;row=grouped(t);errors=[]
    page.on('pageerror',lambda e:errors.append(str(e)))
    page.set_viewport_size({'width':width,'height':900})
    page.goto(t['url']+'/#sessions')
    card=page.locator('.scard',has_text='Grouped review')
    details=card.locator('.session-worktree');details.locator('summary').click()
    details.get_by_role('button',name='Refresh setup progress').click()
    expect(details.locator('pre')).to_contain_text('Second repository: ready')
    assert details.evaluate('(el)=>el.scrollWidth<=el.clientWidth')
    progress=t['api'](f"/sessions/{row['id']}/worktree")
    assert 'token' not in progress and all('token' not in repo['worktree'] for repo in progress['repositories'])
    page.goto(f"{t['url']}/terminal/session/{row['id']}")
    expect(page.locator('#connection')).to_have_text('Connected',timeout=20000)
    terminal_tool(page,'#review')
    dialog=page.get_by_role('dialog',name='Review changes')
    expect(dialog.locator('.review-patch')).to_contain_text('PRIMARY DIFF SENTINEL')
    dialog.get_by_label('Repository',exact=True).select_option('1')
    expect(dialog.locator('.review-patch')).to_contain_text('SECOND DIFF SENTINEL')
    expect(dialog.locator('.review-patch')).not_to_contain_text('PRIMARY DIFF SENTINEL')
    dialog.get_by_label('Repository',exact=True).select_option('0')
    expect(dialog.locator('.review-patch')).to_contain_text('PRIMARY DIFF SENTINEL')
    assert dialog.evaluate('(e)=>e.scrollWidth<=e.clientWidth+1')
    dialog.get_by_role('button',name='Close review').click()
    expect(page.locator('#connection')).to_have_text('Connected')
    assert not errors


def test_grouped_terminal_review_switches_repository(real_terminal):
    t=real_terminal;grouped(t);d=Dashboard(t)
    try:
        d.wait('Grouped review');d.send('/Grouped review\r');d.send('v')
        d.wait('PRIMARY DIFF SENTINEL');d.wait('Tab repo')
        d.send('\t');d.wait('Second repository');d.wait('SECOND DIFF SENTINEL')
        d.send('\t');d.wait('PRIMARY DIFF SENTINEL')
        d.send('\x1b');d.wait('Grouped review');d.quit()
    finally:d.close()


@pytest.mark.parametrize('width',[390,1440])
def test_browser_creates_grouped_workspace(page,real_terminal,width):
    t=real_terminal;primary,_=setup(t)
    repo=t['root'].parent/'extra';repo.mkdir()
    subprocess.run(['git','init','-q',str(repo)],check=True)
    (repo/'extra.txt').write_text('extra repository\n')
    subprocess.run(['git','-C',str(repo),'add','.'],check=True)
    subprocess.run(['git','-C',str(repo),'-c','user.name=Test','-c','user.email=test@example.invalid','commit','-qm','base'],check=True)
    subprocess.run(['git','-C',str(repo),'tag','review-base'],check=True)
    extra=t['api']('/projects',{'name':'Extra project','target_id':t['target_id'],'repo_path':str(repo)})
    other_target=t['api']('/targets',{'name':'Other machine','kind':'local'})
    t['api']('/projects',{'name':'Wrong target','target_id':other_target['id'],'repo_path':str(repo)})
    page.set_viewport_size({'width':width,'height':900});page.goto(t['url']+'/#sessions')
    page.locator('#sess-new').click();page.locator('#ns-project').select_option(str(primary['id']))
    open_advanced(page)
    page.locator('#ns-name').fill('Browser grouped workspace');page.locator('#ns-worktree').check()
    page.locator('#ns-repositories summary').click()
    picker=page.get_by_label('Additional repository',exact=True)
    expect(picker).not_to_contain_text('Wrong target')
    picker.select_option(str(extra['id']));page.get_by_role('button',name='Add repository',exact=True).click()
    page.get_by_label('Base for Extra project',exact=True).fill('review-base')
    expect(page.locator('#ns-repositories summary')).to_contain_text('(1)')
    assert page.locator('#sheet').evaluate('(e)=>e.scrollWidth<=e.clientWidth+1')
    def unavailable(route):
        if route.request.method=='POST':route.fulfill(status=503,content_type='application/json',body='{"detail":"Temporary creation failure"}')
        else:route.continue_()
    page.route('**/api/sessions',unavailable)
    page.locator('#ns-go').click()
    expect(page.locator('#toasts')).to_contain_text('Temporary creation failure')
    expect(page.get_by_label('Base for Extra project',exact=True)).to_have_value('review-base')
    expect(page.locator('#ns-repositories summary')).to_contain_text('(1)')
    page.unroute('**/api/sessions',unavailable)
    with page.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/sessions')) as response:
        page.locator('#ns-go').click()
    assert response.value.status==202,response.value.text()
    row=response.value.json()
    deadline=time.monotonic()+15
    while row.get('setup_state')=='creating' and time.monotonic()<deadline:
        time.sleep(.05);row=t['api'](f"/sessions/{row['id']}")
    assert row['setup_state']=='ready',row
    repositories=row['workspace']['repositories']
    assert len(repositories)==2 and repositories[1]['worktree']['base']=='review-base'
    assert (Path(repositories[1]['worktree']['path'])/'extra.txt').read_text()=='extra repository\n'
    assert row['workdir']==row['workspace']['path']


def test_terminal_creates_grouped_workspace(real_terminal):
    t=real_terminal;grouped(t);d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('/Real terminal\r');d.send('n');d.wait('New session')
        d.send('Terminal grouped creation')
        # The adopted terminal is intentionally unassigned. Search for the
        # primary project by name instead of depending on API/list ordering;
        # target and directory are supplied by the selected project.
        d.send('\t\t');d.wait('Project')
        d.send('Isolated project');d.wait('1 matches');d.send('\r')
        d.wait('Agent (without a profile)')
        d.send('\t'*3+'\x1b[C\t\x1b[C\x13')
        # Additional repository workflow immediately follows isolation.
        # The action defaults to the first available addition. Ctrl-s submits
        # that action directly; tab/right would wrap to “Back” when this
        # one-field form is focused.
        d.send('\x13');d.wait('Repository: Second repository')
        d.send('HEAD\x13');d.wait('Second repository @ HEAD');d.wait('Create session')
        d.send('\x13');d.wait('Workspace setup started',timeout=20)
        row=next(r for r in t['api']('/sessions') if r['name']=='Terminal grouped creation')
        deadline=time.monotonic()+15
        while row.get('setup_state')=='creating' and time.monotonic()<deadline:
            time.sleep(.05);row=t['api'](f"/sessions/{row['id']}")
        assert row['setup_state']=='ready',row
        assert len(row['workspace']['repositories'])==2
        assert row['workspace']['repositories'][1]['worktree']['base']=='HEAD'
        assert row['workdir']==row['workspace']['path']
        d.quit()
    finally:d.close()
