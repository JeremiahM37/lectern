"""Repository additions through real browser/API/Git/tmux, with no model calls."""
import json
import subprocess
import time
import urllib.request
from pathlib import Path
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_interactive_worktree import setup
from test_terminal_dashboard import Dashboard
from conftest import _binary
from test_ui import _tab


def group(t, hook='echo ADDED_REPO_READY'):
    primary, _ = setup(t)
    def project(name, command=''):
        path = t['root'].parent / name
        subprocess.run(['git', 'clone', '-q', str(t['root']), str(path)], check=True)
        return t['api']('/projects', {'name':name, 'target_id':t['target_id'], 'repo_path':str(path), 'setup_cmd':command})
    second = project('Existing web')
    third = project('Additional tools', hook)
    row = t['api']('/sessions', {'name':'Extend this workspace', 'project_id':primary['id'], 'worktree':{'extra_repositories':[{'project_id':second['id']}]}})
    assert row['workspace']['state'] == 'ready'
    return row, third


def same_terminal(t, before):
    current = t['api'](f"/sessions/{before['id']}")
    for key in ['tmux_session', 'workdir', 'setup_state']:
        assert current.get(key) == before.get(key), key
    subprocess.run(['tmux', 'has-session', '-t', '='+before['tmux_session']], env=t['env'], check=True)
    return current


def open_extension(page, row):
    card = page.locator('.scard').filter(has=page.get_by_text(row['name'], exact=True)).first
    card.locator('summary[aria-label="More actions for '+row['name']+'"]').click()
    card.get_by_role('button', name='Workspace repositories', exact=True).click()
    dialog = page.get_by_role('dialog', name='Workspace repositories', exact=True)
    expect(dialog.locator('.we-status')).not_to_have_text('Loading workspace…')
    return dialog


@pytest.mark.parametrize('width', [390,1440])
def test_browser_extension_reopens_keeps_terminal_and_retries(page, real_terminal, width, request):
    t = real_terminal
    release = t['root'].parent/'release-extension'
    row, extra = group(t, f'while [ ! -f "{release}" ]; do sleep .05; done; echo ADDED_REPO_READY')
    request.addfinalizer(lambda: release.touch())
    dirty = Path(row['workspace']['repositories'][0]['worktree']['path'])/'keep.txt'
    dirty.write_text('Existing uncommitted work')
    errors = []; page.on('pageerror', lambda e:errors.append(str(e)))
    page.set_viewport_size({'width':width, 'height':900})
    page.goto(t['url']+'/#sessions')
    from test_terminal_tabs import attach, frame
    attach(page,row['name'])
    terminal=frame(page,row['id'])
    expect(terminal.locator('#connection')).to_have_text('Connected',timeout=15000)
    terminal.locator('body').evaluate('(el)=>window.extensionIdentity="retained"')
    _tab(page, 'sessions')
    dialog = open_extension(page,row)
    expect(dialog.get_by_label('Project',exact=True)).to_have_value(str(extra['id']))
    expect(dialog.locator('option')).to_have_count(1)
    dialog.get_by_label('Base (optional)').fill('HEAD')
    endpoint = '**/api/sessions/*/worktree/repositories'
    page.route(endpoint, lambda route: route.fulfill(status=503, content_type='application/json', body='{"detail":"Temporary target failure"}'))
    dialog.get_by_role('button',name='Add repository',exact=True).click()
    expect(dialog.locator('.we-status')).to_have_text('Temporary target failure')
    expect(dialog.get_by_label('Base (optional)')).to_have_value('HEAD')
    page.unroute(endpoint)
    dialog.get_by_role('button',name='Add repository',exact=True).click()
    expect(dialog.get_by_role('button',name='Cancel addition')).to_be_visible()
    same_terminal(t,row)
    dialog.get_by_role('button',name='Close',exact=True).click()
    dialog = open_extension(page,row)
    expect(dialog.get_by_role('button',name='Cancel addition')).to_be_visible()
    release.touch()
    expect(dialog.locator('.we-status')).to_contain_text('complete',timeout=20000)
    expect(dialog.locator('.we-progress')).to_contain_text('ADDED_REPO_READY')
    assert dialog.evaluate('(e)=>e.scrollWidth<=e.clientWidth')
    assert dirty.read_text() == 'Existing uncommitted work'
    assert len(same_terminal(t,row)['workspace']['repositories']) == 3
    page.screenshot(path=f'/tmp/lectern-extension-{width}.png')
    page.keyboard.press('Escape'); expect(dialog).to_have_count(0)
    _tab(page, 'terminals')
    expect(terminal.locator('#connection')).to_have_text('Connected',timeout=15000)
    assert terminal.locator('body').evaluate('(el)=>window.extensionIdentity')=='retained'
    assert errors == []


def test_terminal_extension_form_and_progress(real_terminal):
    t = real_terminal; row, extra = group(t)
    d = Dashboard(t)
    try:
        d.wait(row['name']); d.send('/'+row['name']+'\r'); d.send('m'); d.wait('Add repository')
        # Thirteen standard session actions precede grouped workspace actions.
        d.send('j'*13+'\r'); d.wait('Add repository (runs project setup)')
        d.wait('Additional tools'); d.send('\x13')
        d.wait('Repository addition started')
        d.send('r'); d.wait('ADDED_REPO_READY')
        same_terminal(t,row); d.quit()
    finally: d.close()


def test_extension_server_restart_retains_original_session(real_terminal):
    t = real_terminal
    entered = t['root'].parent/'extension-entered'
    row, extra = group(t, f'touch "{entered}"; sleep 600')
    op = t['api'](f"/sessions/{row['id']}/worktree/repositories", {'project_id':extra['id']})
    deadline=time.monotonic()+15
    while not entered.exists() and time.monotonic()<deadline: time.sleep(.05)
    assert entered.exists()
    # Kill only this private server process; persistent tmux and the target
    # worker survive, just as on a lost server connection.
    t['proc'].kill(); t['proc'].wait(timeout=10)
    replacement=subprocess.Popen([_binary()],cwd=t['root'],env=t['env'],stdout=subprocess.DEVNULL,stderr=subprocess.DEVNULL)
    try:
        deadline=time.monotonic()+15
        while time.monotonic()<deadline:
            try: t['api']('/health'); break
            except Exception: time.sleep(.1)
        path=f"/sessions/{row['id']}/worktree/operations/{op['id']}"
        deadline=time.monotonic()+20
        while time.monotonic()<deadline:
            current=t['api'](path+'/recover',{})
            if current['state'] not in ['running','recovering']: break
            time.sleep(.1)
        assert current['state']=='failed',current
        result=same_terminal(t,row)
        assert len(result['workspace']['repositories'])==3
        assert Path(result['workspace']['repositories'][-1]['worktree']['path']).exists()
    finally:
        replacement.terminate(); replacement.wait(timeout=15)


def test_browser_cancels_addition_without_ending_session(page, real_terminal):
    t=real_terminal
    entered=t['root'].parent/'cancel-entered'
    late=t['root'].parent/'must-not-run'
    row, extra=group(t,f'touch "{entered}"; sleep 600; touch "{late}"')
    page.goto(t['url']+'/#sessions')
    dialog=open_extension(page,row)
    dialog.get_by_role('button',name='Add repository',exact=True).click()
    deadline=time.monotonic()+15
    while not entered.exists() and time.monotonic()<deadline: time.sleep(.05)
    assert entered.exists()
    dialog.get_by_role('button',name='Cancel addition',exact=True).click()
    expect(dialog.locator('.we-status')).to_contain_text('cancelled',timeout=20000)
    expect(dialog.get_by_role('button',name='Add repository',exact=True)).not_to_be_visible()
    result=same_terminal(t,row)
    assert Path(result['workspace']['repositories'][-1]['worktree']['path']).exists()
    assert not late.exists()
