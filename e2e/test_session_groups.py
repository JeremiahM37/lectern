import json,subprocess,urllib.request
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal,open_terminal
from test_terminal_dashboard import Dashboard


def patch(t,sid,body):
    request=urllib.request.Request(t['url']+f'/api/sessions/{sid}',method='PATCH',headers={'Content-Type':'application/json'},data=json.dumps(body).encode())
    return json.load(urllib.request.urlopen(request))

@pytest.mark.parametrize('width',[390,1440])
def test_group_tree_editor_search_and_failure_preserve_attachment(page,real_terminal,width):
    t=real_terminal;patch(t,t['id'],{'group_path':'Work/Client'})
    subprocess.run(['tmux','new-session','-d','-s','group-other','-c',str(t['root']),'sleep 600'],env=t['env'],check=True)
    other=t['api']('/sessions/adopt',{'target_id':t['target_id'],'tmux_session':'group-other','workdir':str(t['root']),'name':'Other project agent','agent':'codex'})
    patch(t,other['id'],{'group_path':'Work/Other'})
    page.set_viewport_size({'width':width,'height':900});page.goto(t['url']+'/#sessions')
    page.locator('#sess-grouping').select_option('group')
    top=page.locator('details.session-group[data-group-path="Work"]')
    expect(top).to_be_visible();expect(top).to_contain_text('Real terminal');expect(top).to_contain_text('Other project agent')
    top.locator(':scope > summary').click();expect(page.locator('#sesslist').get_by_text('Real terminal',exact=True)).not_to_be_visible()
    page.reload();expect(page.locator('#sess-grouping')).to_have_value('group');expect(page.locator('#sesslist').get_by_text('Real terminal',exact=True)).not_to_be_visible()
    page.locator('#sess-search').fill('Work/Client')
    expect(page.locator('#sesslist').get_by_text('Real terminal',exact=True)).to_be_visible();expect(page.locator('#sesslist').get_by_text('Other project agent',exact=True)).not_to_be_visible()
    page.locator('summary[aria-label="More actions for Real terminal"]').click();page.get_by_role('button',name='Move to group',exact=True).click()
    page.locator('#sg-path').fill('Work//Bad');page.locator('#sg-save').click()
    expect(page.locator('#sg-error')).to_contain_text('nonempty');expect(page.locator('#sg-path')).to_have_value('Work//Bad')
    page.locator('#sg-path').fill('Personal/研究');page.locator('#sg-save').click();expect(page.locator('#sheet')).not_to_be_visible()
    page.locator('#sess-search').fill('Personal/研究');expect(page.locator('#sesslist').get_by_text('Real terminal',exact=True)).to_be_visible()
    page.screenshot(path=f'/tmp/lectern-session-groups-{width}.png')
    assert t['api'](f"/sessions/{t['id']}")['tmux_session']=='terminal-test'
    open_terminal(page,t);expect(page.locator('#connection')).to_have_text('Connected')


def test_tui_move_and_clear_group_keeps_session(real_terminal):
    t=real_terminal;d=Dashboard(t)
    try:
        d.wait('Real terminal');d.send('G');d.wait('Move to group');d.send('Work/Client\x13');d.wait('Move to group completed')
        assert t['api'](f"/sessions/{t['id']}")['group_path']=='Work/Client'
        d.send('ggg');d.wait('named group');d.wait('Work/Client')
        d.send('G');d.wait('Move to group');d.send('\x01\x0b\x13');d.wait('Move to group completed')
        assert t['api'](f"/sessions/{t['id']}")['group_path']==''
        d.quit()
        subprocess.run(['tmux','has-session','-t','=terminal-test'],env=t['env'],check=True)
    finally:d.close()
