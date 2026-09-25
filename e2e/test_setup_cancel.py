"""Cancel real held checkouts through desktop, phone, and the terminal dashboard."""
import time
import subprocess
from pathlib import Path
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_terminal_dashboard import Dashboard
from test_background_setup_ui import hold_second_checkout
from test_session_restore import request


def choose_action(dashboard, label, timeout=12):
    """Select a named action from the rendered menu, independent of its index."""
    dashboard.send('m')
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        dashboard.pump()
        lines = dashboard.text.splitlines()
        try:
            start = next(i for i, line in enumerate(lines)
                         if line.strip().startswith('Actions ·'))
            end = next(i for i in range(start + 1, len(lines))
                       if lines[i].strip().startswith(('Enter attach ·', 'Click attach ·', 'Click/Enter attach ·')))
        except StopIteration:
            continue
        actions = [line.strip() for line in lines[start + 1:end] if line.strip()]
        if label in actions:
            dashboard.send('j' * actions.index(label) + '\r')
            return
    raise AssertionError(f'Menu action {label!r} was not selected:\n{dashboard.text}')


def prepare(t):
    release=hold_second_checkout(t)
    projects=t['api']('/projects')
    primary=next(p for p in projects if p['name']=='Isolated project')
    extra=next(p for p in projects if p['name']=='Second repository')
    row=t['api']('/sessions',{'name':'Cancel setup proof','agent':'claude','target_id':t['target_id'],
                             'project_id':primary['id'],'background':True,
                             'worktree':{'extra_repositories':[{'project_id':extra['id']}]}})
    deadline=time.monotonic()+10
    while time.monotonic()<deadline:
        current=t['api'](f"/sessions/{row['id']}")
        repos=current.get('workspace',{}).get('repositories',[])
        if len(repos)==2 and (Path(repos[1]['worktree']['path'])/'second.txt').exists():break
        time.sleep(.05)
    assert len(repos)==2 and (Path(repos[1]['worktree']['path'])/'second.txt').exists()
    return release,row


def outcome(t,row):
    deadline=time.monotonic()+10
    while time.monotonic()<deadline:
        current=t['api'](f"/sessions/{row['id']}")
        if current['setup_state']=='failed':break
        time.sleep(.05)
    assert current['setup_cancel_requested'] and 'cancelled' in current['setup_error'],current
    assert current['ended_at'] is not None
    first=Path(current['workspace']['repositories'][0]['worktree']['path'])
    assert (first/'hello.txt').exists(), 'cancellation removed the completed first checkout'
    assert subprocess.run(['tmux','has-session','-t','='+current['tmux_session']],env=t['env'],capture_output=True).returncode!=0, 'cancelled setup launched an agent'
    return current


def recovery_outcome(t,row,kept):
    current=t['api'](f"/sessions/{row['id']}")
    assert all(r['worktree']['state']=='ready' for r in current['workspace']['repositories']),current
    assert kept.read_text()=='user work'
    assert request(t,'DELETE',f"/sessions/{row['id']}/worktree")[0]==409
    kept.unlink()
    assert request(t,'DELETE',f"/sessions/{row['id']}/worktree")[0]==200
    for repo in current['workspace']['repositories']:
        assert not Path(repo['worktree']['path']).exists()
        subprocess.run(['git','-C',repo['worktree']['repo'],'rev-parse',repo['worktree']['branch']],check=True,capture_output=True)


@pytest.mark.parametrize('width',[390,1440])
def test_browser_cancels_setup_and_retains_completed_checkout(page,real_terminal,width):
    t=real_terminal;release,row=prepare(t);errors=[]
    page.on('pageerror',lambda error:errors.append(str(error)))
    try:
        page.set_viewport_size({'width':width,'height':900});page.goto(t['url']+'/#sessions')
        card=page.locator('.scard',has_text='Cancel setup proof')
        expect(card.locator('.spane')).to_contain_text('Isolated project: ready',timeout=15000)
        card.get_by_role('button',name='Cancel setup',exact=True).click()
        expect(card.locator('.spane')).to_contain_text('cancelled',timeout=15000)
        current=outcome(t,row)
        kept=Path(current['workspace']['repositories'][1]['worktree']['path'])/'user-work.txt';kept.write_text('user work')
        card.locator('summary[aria-label="More actions for Cancel setup proof"]').click()
        card.get_by_role('button',name='Recover allocation',exact=True).click()
        expect(page.locator('#toasts')).to_contain_text('Allocation validated',timeout=15000)
        recovery_outcome(t,row,kept)
        expect(card.get_by_role('button',name='⌨ Attach',exact=True)).to_have_count(0)
        assert page.evaluate('document.documentElement.scrollWidth<=innerWidth')
        assert not errors
    finally:release.touch()


def test_terminal_cancels_selected_setup(real_terminal):
    t=real_terminal;release,row=prepare(t);d=Dashboard(t)
    try:
        d.wait('Cancel setup proof');d.send('/Cancel setup proof\r')
        d.wait('Isolated project: ready',timeout=15)
        choose_action(d, 'Cancel setup')
        d.wait('Cancellation requested.')
        d.wait('cancelled',timeout=15)
        current=outcome(t,row)
        kept=Path(current['workspace']['repositories'][1]['worktree']['path'])/'user-work.txt';kept.write_text('user work')
        choose_action(d, 'Recover allocation (keep files)')
        d.wait('Recover allocation (keep files) completed')
        recovery_outcome(t,row,kept);d.quit()
    finally:release.touch();d.close()
