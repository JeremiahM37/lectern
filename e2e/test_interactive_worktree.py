import json,subprocess,urllib.request,time
from pathlib import Path
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_terminal_dashboard import Dashboard


def setup(t):
    root=t['root']
    def git(*args):return subprocess.check_output(['git','-C',str(root),*args],text=True).strip()
    git('add','.');git('-c','user.name=Test','-c','user.email=test@example.com','commit','-qm','base')
    project=t['api']('/projects',{'name':'Isolated project','target_id':t['target_id'],'repo_path':str(root)})
    request=urllib.request.Request(t['url']+'/api/agents',method='PUT',headers={'Content-Type':'application/json'},data=json.dumps([{'name':'claude','command':'sleep 600'}]).encode())
    urllib.request.urlopen(request).close()
    return project,git

@pytest.mark.parametrize('width',[390,1440])
def test_web_launches_and_safely_removes_interactive_worktree(page,real_terminal,width):
    t=real_terminal;project,git=setup(t)
    page.set_viewport_size({'width':width,'height':900})
    page.goto(t['url']+'/#sessions');page.locator('#sess-new').click()
    page.get_by_label('Project',exact=True).select_option(str(project['id']))
    page.locator('label[for=ns-name]').click()
    expect(page.get_by_label('Name',exact=True)).to_be_focused()
    page.get_by_label('Name',exact=True).fill('Isolated UI proof')
    page.get_by_label('Isolate in a new Git worktree',exact=True).check()
    expect(page.locator('#ns-worktree-options')).to_be_visible()
    page.locator('#ns-worktree-branch').fill('feature/ui-proof')
    page.locator('#ns-go').click()
    expect(page.locator('#sesslist')).to_contain_text('feature/ui-proof',timeout=15000)
    # The branch is visible as soon as the allocation is planned. Attach only
    # appears when setup actually completes.
    expect(page.locator('.scard',has_text='Isolated UI proof').get_by_role('button',name='⌨ Attach',exact=True)).to_be_visible(timeout=15000)
    row=next(s for s in t['api']('/sessions') if s['name']=='Isolated UI proof')
    dest=Path(row['workspace']['path']);assert dest!=t['root'] and (dest/'hello.txt').exists()
    assert git('status','--porcelain')==''
    page.screenshot(path=f'/tmp/lectern-worktree-{width}.png')
    def delete(path):
        return urllib.request.urlopen(urllib.request.Request(t['url']+'/api'+path,method='DELETE')).read()
    delete('/sessions/'+str(row['id']))
    page.locator('#sess-scope').select_option('all')
    expect(page.locator('#sesslist')).to_contain_text('Isolated UI proof')
    # Only one allocated worktree exists; the original adopted terminal has no removal action.
    page.locator('summary[aria-label="More actions for Isolated UI proof"]').click()
    page.on('dialog',lambda d:d.accept())
    (dest/'keep.txt').write_text('do not remove')
    page.get_by_role('button',name='Remove worktree',exact=True).click()
    expect(page.locator('#toasts')).to_contain_text('untracked or ignored')
    assert (dest/'keep.txt').read_text()=='do not remove'
    (dest/'keep.txt').unlink()
    page.locator('summary[aria-label="More actions for Isolated UI proof"]').click()
    page.get_by_role('button',name='Remove worktree',exact=True).click()
    expect(page.locator('#toasts')).to_contain_text('branch kept')
    assert not dest.exists();git('rev-parse','feature/ui-proof')


def test_terminal_form_creates_an_isolated_session(real_terminal):
    t=real_terminal;project,git=setup(t);d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('n');d.wait('New session');d.send('TUI worktree')
        # Name -> profile (defaults) -> project (choose project) -> agent ->
        # model -> prompt -> isolation. Target and directory are derived from
        # the selected project and skipped by the form.
        d.send('\t\t\x1b[C'+'\t'*4+'\x1b[C')
        d.send('\x13');d.wait('Workspace setup started')
        row=next(s for s in t['api']('/sessions') if s['name']=='TUI worktree')
        deadline=time.monotonic()+15
        while row.get('setup_state')=='creating' and time.monotonic()<deadline:
            time.sleep(.05);row=t['api'](f"/sessions/{row['id']}")
        assert row['setup_state']=='ready',row
        assert row['workspace']['state']=='ready'  and row['workdir']!=str(t['root'])
        assert git('status','--porcelain')==''
        d.quit()
    finally:d.close()
