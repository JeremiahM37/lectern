"""Background setup stays observable across navigation and terminal refreshes."""
from pathlib import Path
import shlex
import subprocess
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_terminal_dashboard import Dashboard
from test_multi_workspace import grouped
from test_session_restore import request
from session_sheet import open_advanced


def hold_second_checkout(t):
    grouped(t)
    profile=str(t['root'].parent/'private-profile')
    assert request(t,'PUT','/agents',[{'name':agent,'command':'sleep 600','env':{'CODEX_HOME':profile,'CLAUDE_CONFIG_DIR':profile}} for agent in ('claude','codex')])[0]==200
    release=t['root'].parent/'release-checkout'
    hook=t['root'].parent/'other/.git/hooks/post-checkout'
    hook.write_text('#!/bin/sh\nwhile [ ! -f '+shlex.quote(str(release))+' ]; do sleep 0.05; done\n')
    hook.chmod(0o700)
    return release


@pytest.mark.parametrize('width',[390,1440])
def test_browser_background_setup_survives_reload(page,real_terminal,width):
    t=real_terminal;release=hold_second_checkout(t);errors=[]
    page.on('pageerror',lambda error:errors.append(str(error)))
    try:
        page.set_viewport_size({'width':width,'height':900});page.goto(t['url']+'/#sessions')
        page.locator('#sess-new').click();open_advanced(page);page.locator('#ns-name').fill('Slow browser setup')
        page.locator('#ns-worktree').check();page.locator('#ns-repositories summary').click()
        extra=next(p for p in t['api']('/projects') if p['name']=='Second repository')
        page.get_by_label('Additional repository',exact=True).select_option(str(extra['id']))
        page.get_by_role('button',name='Add repository',exact=True).click()
        with page.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/sessions')) as response:
            page.locator('#ns-go').click()
        assert response.value.status==202
        session=response.value.json()
        expect(page.locator('#ns-go')).not_to_be_visible()
        card=page.locator('.scard',has_text='Slow browser setup')
        expect(card.get_by_role('button',name='Setting up',exact=True)).to_be_disabled()
        expect(card.locator('.spane')).to_contain_text('Isolated project: ready',timeout=15000)
        expect(card.locator('.spane')).to_contain_text('Second repository: creating')
        assert card.get_by_role('button',name='⌨ Attach',exact=True).count()==0
        page.screenshot(path=f'/tmp/lectern-background-setup-{width}.png',full_page=True)
        page.reload()
        expect(card.locator('.spane')).to_contain_text('Isolated project: ready',timeout=15000)
        assert t['api'](f"/sessions/{session['id']}")['setup_state']=='creating'
        assert page.evaluate('document.documentElement.scrollWidth<=innerWidth')
        release.touch()
        expect(card.get_by_role('button',name='⌨ Attach',exact=True)).to_be_visible(timeout=15000)
        card.get_by_role('button',name='⌨ Attach',exact=True).click()
        frame=page.frame_locator('#terminal-workspace .terminal-tabpanel:not([hidden]) iframe')
        expect(frame.locator('#connection')).to_have_text('Connected',timeout=15000)
        subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)
        assert not errors
    finally:release.touch()


def test_terminal_background_setup_updates_selected_preview(real_terminal):
    t=real_terminal;release=hold_second_checkout(t);d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('/Real terminal\r');d.send('n');d.wait('New session')
        # A selected project supplies target and directory, so two hidden
        # fields are skipped before the isolation controls.
        d.send('Slow terminal setup unique');d.send('\t\t\x1b[C'+'\t'*4+'\x1b[C\t\x1b[C\x13')
        d.wait('Workspace repositories:');d.send('\x13');d.wait('Base (blank uses committed HEAD)')
        d.send('HEAD\x13');d.wait('Create session');d.send('\x13');d.wait('Workspace setup started')
        # The new session is created from the unassigned adopted terminal. Use
        # the exact name to select it: list ordering can change as setup
        # progress refreshes, so a relative `k` would be racy here.
        d.send('\x1b');d.send('/unique\r');d.wait('Setting up workspace')
        d.wait('Isolated project: ready',timeout=15);d.wait('Second repository: creating')
        d.send('\r');d.wait('Workspace is setting up')
        release.touch();d.wait('Second repository: ready',timeout=15)
        session=next(r for r in t['api']('/sessions') if r['name']=='Slow terminal setup unique')
        assert session['setup_state']=='ready'
        d.quit()
    finally:
        release.touch();d.close()
