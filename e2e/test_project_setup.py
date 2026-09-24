from pathlib import Path
import json
import urllib.request
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_interactive_worktree import setup
from test_terminal_dashboard import Dashboard
from session_sheet import open_advanced


@pytest.fixture
def routed_page(browser):
    """A page whose request-fault tests are not bypassed by the app SW.

    Playwright page routes do not observe requests handled by a service
    worker. This test intentionally installs a PATCH route after the first
    navigation, so block the worker for this page only; the normal ``page``
    fixture continues to cover the installed PWA path.
    """
    context = browser.new_context(service_workers="block")
    page = context.new_page()
    yield page
    context.close()


@pytest.mark.parametrize('width',[390,1440])
def test_web_project_setup_save_retry_and_launch(routed_page,real_terminal,width):
    page=routed_page
    t=real_terminal;project,git=setup(t);errors=[]
    page.on('pageerror',lambda e:errors.append(str(e)))
    page.set_viewport_size({'width':width,'height':900})
    page.goto(t['url']+'/#targets')
    page.get_by_role('tab',name='Projects',exact=True).click()
    page.locator('.pjname',has_text='Isolated project').click()
    field=page.get_by_label('New worktree setup command',exact=True)
    command='printf ready > prepared; echo SETUP_FINISHED'
    field.fill(command)
    endpoint=f'**/api/projects/{project["id"]}'
    intercepted=[]
    def fail_save(route):
        intercepted.append(route.request.method)
        route.fulfill(status=503,content_type='application/json',body='{"detail":"Temporary save failure"}')
    page.route(endpoint,fail_save)
    page.get_by_role('button',name='Save setup command').click()
    expect(page.locator('.project-setup-status')).to_have_text('Temporary save failure')
    expect(field).to_have_value(command)
    assert intercepted==['PATCH'],intercepted
    page.unroute(endpoint)
    page.get_by_role('button',name='Save setup command').click()
    expect(page.locator('.project-setup-status')).to_contain_text('Saved')
    page.keyboard.press('Escape')
    page.goto(t['url']+'/#sessions');page.locator('#sess-new').click()
    page.get_by_label('Project',exact=True).select_option(str(project['id']))
    open_advanced(page)
    page.locator('#ns-name').fill('Prepared workspace');page.locator('#ns-worktree').check()
    expect(page.locator('#ns-proj-hint')).to_contain_text('setup command')
    page.locator('#ns-go').click()
    card=page.locator('.scard',has_text='Prepared workspace')
    expect(card.get_by_role('button',name='⌨ Attach',exact=True)).to_be_visible(timeout=20000)
    row=next(s for s in t['api']('/sessions') if s['name']=='Prepared workspace')
    assert (Path(row['workspace']['path'])/'prepared').read_text()=='ready'
    assert not (t['root']/'prepared').exists()
    assert row['workspace']['setup_state']=='complete'
    card.locator('.session-worktree summary').click()
    expect(card.locator('.workspace-setup-output')).to_contain_text('SETUP_FINISHED')
    assert card.locator('.session-worktree').evaluate('(e)=>e.scrollWidth<=e.clientWidth')
    card.scroll_into_view_if_needed()
    page.screenshot(path=f'/tmp/lectern-project-setup-{width}.png')
    assert errors==[]


def test_terminal_edits_project_setup_and_creates_workspace(real_terminal):
    t=real_terminal;project,git=setup(t);d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('4');d.wait('Isolated project');d.send('/Isolated project\r')
        d.send('m');d.wait('Edit project')
        # Project actions: shell, review, brief, notes, handoffs, capability, rename, edit.
        d.send('j'*7+'\r');d.wait('New worktree setup command')
        d.send('\t'*3+'printf terminal-ready > prepared; echo TERMINAL_SETUP\x13');d.wait('Edit project completed')
        saved=t['api']('/projects')[0];assert 'terminal-ready' in saved['setup_cmd']
        row=t['api']('/sessions',{'name':'Terminal prepared','project_id':project['id'],'worktree':{}})
        d.send('1');d.wait('Terminal prepared');d.send('/Terminal prepared\r');d.wait('TERMINAL_SETUP')
        assert (Path(row['workspace']['path'])/'prepared').read_text()=='terminal-ready'
        d.quit()
    finally:d.close()


def test_terminal_grouped_setup_results(real_terminal):
    import subprocess
    t=real_terminal;primary,git=setup(t)
    def patch(project,command):
        req=urllib.request.Request(t['url']+f'/api/projects/{project["id"]}',method='PATCH',headers={'Content-Type':'application/json'},data=json.dumps({'setup_cmd':command}).encode())
        urllib.request.urlopen(req).close()
    patch(primary,'echo API_PREPARED')
    extra_path=t['root'].parent/'setup-extra'
    subprocess.run(['git','clone','-q',str(t['root']),str(extra_path)],check=True)
    extra=t['api']('/projects',{'name':'Web setup','target_id':t['target_id'],'repo_path':str(extra_path),'setup_cmd':'echo WEB_PREPARED'})
    row=t['api']('/sessions',{'name':'Grouped setup results','project_id':primary['id'],'worktree':{'extra_repositories':[{'project_id':extra['id']}]}})
    d=Dashboard(t)
    try:
        d.wait('Grouped setup results');d.send('/Grouped setup results\r')
        d.wait('API_PREPARED');d.wait('WEB_PREPARED')
        d.wait('Project setup: complete')
        assert all(repo['worktree']['setup_state']=='complete' for repo in row['workspace']['repositories'])
        d.quit()
    finally:d.close()
