from pathlib import Path
import urllib.error
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_interactive_worktree import setup
from test_terminal_dashboard import Dashboard
from session_sheet import open_advanced


@pytest.mark.parametrize('width',[390,1440])
def test_failed_checkout_hook_is_visible_and_safely_recoverable(page,real_terminal,width):
    t=real_terminal;project,git=setup(t);head=git('rev-parse','HEAD')
    hook=t['root']/'.git/hooks/post-checkout'
    hook.write_text('#!/bin/sh\nprintf "preserve setup output" > setup-artifact\necho "SETUP FAILURE SENTINEL" >&2\nexit 1\n');hook.chmod(0o755)
    page.set_viewport_size({'width':width,'height':900});page.goto(t['url']+'/#sessions')
    page.locator('#sess-new').click();open_advanced(page);page.locator('#ns-name').fill('Failed setup proof');page.locator('#ns-worktree').check()
    page.locator('#ns-worktree-branch').fill('recoverable-setup')
    with page.expect_response(lambda r:r.request.method=='POST' and r.url.endswith('/sessions')) as response:page.locator('#ns-go').click()
    assert response.value.status==202
    expect(page.locator('#ns-name')).not_to_be_visible()
    card=page.locator('.scard').filter(has=page.locator('.nm',has_text='Failed setup proof'))
    expect(card).to_be_visible();expect(card.locator('.spane')).to_contain_text('SETUP FAILURE SENTINEL',timeout=15000)
    assert page.locator('#sess-scope').input_value()=='active'
    card.locator('.session-worktree summary').click()
    expect(card.locator('.session-worktree')).to_contain_text('Setup error:')
    expect(card.locator('.session-worktree')).to_contain_text('SETUP FAILURE SENTINEL')
    rows=t['api']('/sessions?all=true');row=next(r for r in rows if r['name']=='Failed setup proof')
    dest=Path(row['workspace']['path']);assert row['workspace']['state']=='failed'
    assert row['workspace']['commit']==head and (dest/'setup-artifact').read_text()=='preserve setup output'
    page.on('dialog',lambda d:d.accept())
    card.locator('summary[aria-label="More actions for Failed setup proof"]').click();page.get_by_role('button',name='Remove worktree',exact=True).click()
    expect(page.locator('#toasts')).to_contain_text('untracked or ignored')
    assert (dest/'setup-artifact').exists()
    (dest/'setup-artifact').rename(t['root'].parent/'preserved-setup-artifact')
    card.locator('summary[aria-label="More actions for Failed setup proof"]').click();page.get_by_role('button',name='Remove worktree',exact=True).click()
    expect(page.locator('#toasts')).to_contain_text('branch kept')
    assert not dest.exists() and git('rev-parse','recoverable-setup')==head and git('status','--porcelain')==''


def test_terminal_can_remove_failed_worktree_from_ended_session(real_terminal):
    t=real_terminal;project,git=setup(t)
    hook=t['root']/'.git/hooks/post-checkout';hook.write_text('#!/bin/sh\necho "TUI SETUP FAILURE" >&2\nexit 1\n');hook.chmod(0o755)
    with pytest.raises(urllib.error.HTTPError) as error:
        t['api']('/sessions',{'name':'Failed terminal setup','project_id':project['id'],'worktree':{'branch':'tui-failed-setup'}})
    assert error.value.code==409
    row=next(r for r in t['api']('/sessions?all=true') if r['name']=='Failed terminal setup');dest=Path(row['workspace']['path'])
    d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('z');d.wait('Failed terminal setup');d.send('/Failed terminal setup\r')
        d.wait('TUI SETUP FAILURE');d.send('m');d.wait('Remove worktree (keep branch)')
        # Find the named action without depending on how many other actions exist.
        d.send('/remove worktree\r');d.wait('Changed, untracked or ignored');d.send('y');d.wait('Remove worktree (keep branch) completed')
        assert not dest.exists();git('rev-parse','tui-failed-setup');d.quit()
    finally:d.close()
