import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal


@pytest.mark.parametrize('width', [390, 1440])
@pytest.mark.parametrize('real_terminal', [{'agent_script': '#!/bin/sh\necho SHOULD_NOT_RUN\n'}], indirect=True)
def test_target_command_lookup_retry_and_keyboard(page, real_terminal, width):
    t = real_terminal
    errors = []
    page.on('pageerror', lambda e: errors.append(str(e)))
    page.set_viewport_size({'width': width, 'height': 844})
    page.goto(t['url'] + '/#targets')
    button = page.locator('.rowcard').filter(has=page.get_by_role('heading', name='terminal-local')).get_by_role('button', name='Agent commands')
    button.click()
    dialog = page.get_by_role('dialog', name='Agent commands', exact=True)
    expect(dialog.locator('.ac-status')).to_contain_text('lookup complete')
    claude = dialog.locator('li').filter(has=page.get_by_text('claude', exact=True))
    expect(claude).to_contain_text('Found')
    expect(claude).to_contain_text('test-agent')
    assert dialog.evaluate('(e) => e.scrollWidth <= e.clientWidth')
    assert len(t['api']('/sessions')) == 1
    page.route('**/api/targets/*/agents', lambda r: r.fulfill(status=503, content_type='application/json', body='{"detail":"Target is unreachable"}'))
    dialog.get_by_role('button', name='Check again').click()
    expect(dialog.locator('.ac-status')).to_have_text('Target is unreachable')
    expect(dialog.locator('li')).to_have_count(0)
    page.unroute('**/api/targets/*/agents')
    dialog.get_by_role('button', name='Check again').click()
    expect(dialog.locator('.ac-status')).to_contain_text('lookup complete')
    page.screenshot(path=f'/tmp/lectern-command-check-{width}.png')
    page.keyboard.press('Escape')
    expect(dialog).to_have_count(0)
    expect(button).to_be_focused()
    assert errors == []


@pytest.mark.parametrize('real_terminal', [{'agent_script': '#!/bin/sh\necho SHOULD_NOT_RUN\n'}], indirect=True)
def test_terminal_target_commands(real_terminal):
    from test_terminal_dashboard import Dashboard
    t = real_terminal
    d = Dashboard(t)
    try:
        d.wait('Real terminal')
        d.send('5'); d.wait('terminal-local')
        d.send('/terminal-local\r')
        d.send('m'); d.wait('Check agent commands')
        d.send('j\r'); d.wait('test-agent')
        d.wait('Found')
        assert len(t['api']('/sessions')) == 1
        d.quit()
    finally:
        d.close()
