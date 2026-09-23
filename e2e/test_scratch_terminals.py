"""Scratch classification and in-place promotion on real shells and files."""
from pathlib import Path
import subprocess
import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal


@pytest.mark.parametrize('width', [320, 1280])
@pytest.mark.parametrize('real_terminal', [{'isolated_scratch': True}], indirect=True)
def test_quick_terminal_stays_scratch_until_promoted(page, real_terminal, width):
    t = real_terminal
    page.set_viewport_size({'width': width, 'height': 844})
    errors = []
    page.on('pageerror', lambda error: errors.append(str(error)))
    before = {s['id'] for s in t['api']('/sessions')}
    page.goto(t['url'] + '/#terminals')
    page.locator('.terminal-empty').get_by_role('button', name='New terminal').click()
    expect(page.get_by_role('tab')).to_have_count(1, timeout=15000)
    shell, = [s for s in t['api']('/sessions') if s['id'] not in before]
    sid = shell['id']
    selector = f'.scard[data-session-id="{sid}"]'
    target = '=' + shell['tmux_session'] + ':'
    def pane_pid():
        return subprocess.check_output(['tmux', 'display-message', '-p', '-t', target, '#{pane_pid}'], env=t['env']).strip()
    pid = pane_pid()
    frame = page.frame_locator(f'iframe[src="/terminal/session/{sid}?embed=1"]')
    expect(frame.locator('#connection')).to_have_text('Connected', timeout=20000)
    frame.locator('#agent-terminal').click()
    page.keyboard.type('printf scratch-kept > proof.txt; echo SCRATCH-READY')
    page.keyboard.press('Enter')
    expect(frame.locator('.xterm-screen')).to_contain_text('SCRATCH-READY', timeout=10000)
    proof = Path(shell['workdir']) / 'proof.txt'
    assert proof.read_text() == 'scratch-kept'
    page.goto(t['url'] + '/?view=board#sessions')
    scratch = page.locator('#scratch-terminals')
    regular = page.locator('#regular-sessions')
    expect(scratch.locator(selector)).to_be_visible()
    expect(regular.locator(selector)).to_have_count(0)
    # Projectless AI conversations still belong among regular sessions.
    expect(regular.locator(f'.scard[data-session-id="{t["id"]}"]')).to_be_visible()
    expect(scratch.locator(selector).get_by_role('button', name='Make a project')).to_be_visible()
    page.locator('#sess-search').fill(shell['workdir'])
    expect(scratch.locator(selector)).to_be_visible()
    expect(regular.locator('.scard')).to_have_count(0)
    page.locator('#sess-search').fill('')
    page.locator('#sess-grouping').select_option('target')
    expect(scratch.locator(selector)).to_be_visible()
    expect(regular.locator(f'.scard[data-session-id="{t["id"]}"]')).to_be_visible()
    page.locator('#sess-grouping').select_option('none')
    page.reload()
    expect(scratch.locator(selector)).to_be_visible()
    scratch.locator(selector).get_by_role('button', name='⌨ Attach', exact=True).click()
    expect(frame.locator('#connection')).to_have_text('Connected', timeout=20000)
    assert pane_pid() == pid
    page.goto(t['url'] + '/?view=board#sessions')
    page.once('dialog', lambda dialog: dialog.accept('kept-scratch'))
    with page.expect_request(lambda r: r.url.endswith(f'/sessions/{sid}/promote')) as request:
        scratch.locator(selector).get_by_role('button', name='Make a project').click()
    assert request.value.post_data_json['wrap'] is False
    expect(scratch.locator(selector)).to_have_count(0, timeout=15000)
    expect(regular.locator(selector)).to_be_visible()
    expect(regular.locator(selector)).to_contain_text('kept-scratch')
    promoted = t['api'](f'/sessions/{sid}')
    assert promoted['project_id'] and promoted['workdir'] == shell['workdir']
    assert promoted['tmux_session'] == shell['tmux_session'] and pane_pid() == pid
    assert proof.read_text() == 'scratch-kept'
    assert len(t['api']('/sessions')) == len(before) + 1
    output = subprocess.check_output(['tmux', 'capture-pane', '-p', '-t', target], env=t['env']).decode()
    assert 'Before this session ends' not in output
    page.reload()
    expect(regular.locator(selector)).to_be_visible()
    expect(scratch.locator(selector)).to_have_count(0)
    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth + 1')
    assert not errors
