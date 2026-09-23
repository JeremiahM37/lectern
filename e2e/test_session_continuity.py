"""Continuous agent switching: progress, honest failure, lineage and favorites.

The real terminal fixture gives us real tmux and a real server; the agents are
scripted so a wrap is written on demand and the destination launch is genuinely
exercised.
"""
import json
import re
import subprocess
import urllib.request

import pytest
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal


def _write(t, path, data, method='PATCH'):
    req = urllib.request.Request(
        t['url'] + '/api' + path,
        data=json.dumps(data).encode(),
        headers={'Content-Type': 'application/json'},
        method=method,
    )
    return json.load(urllib.request.urlopen(req, timeout=20))


def install_agents(t, tmp_path):
    """Two scripted agents: they answer a handoff prompt with a real wrap."""
    runner = tmp_path / 'continuity-agent.py'
    runner.write_text(r'''#!/usr/bin/env python3
import os,re,sys,time
from pathlib import Path
record=Path.cwd()/('agent-%s.json'%os.getpid())
record.write_text(Path.cwd().as_posix()+' '+repr(sys.argv[1:]))
print('CONTINUITY AGENT READY',flush=True)
for line in sys.stdin:
    match=re.search(r'/tmp/lectern-handoff-[0-9]+-[a-z0-9]+\.md',line)
    if match:
        # Stay in flight long enough for the progress surface to be observed
        # (and for a reload to land while the wrap is still being written).
        time.sleep(4)
        path=Path(match.group())
        body='## WHERE WE ARE\nContinuity fixture.\n## NEXT\nKeep going.\n'
        part=Path(str(path)+'.partial')
        part.write_text(body+'<!-- lectern:complete '+str(path)+' -->\n')
        part.replace(path)
        print('WRAPPED',flush=True)
''')
    runner.chmod(0o755)
    specs = [
        {'name': name, 'command': str(runner), 'model_flag': flag, 'prompt_arg': True,
         'models_command': "printf '%s' '[\"%s\"]'" % ('%s', model)}
        for name, flag, model in [('claude', '--model', 'fable'), ('codex', '-m', 'astra-test')]
    ]
    req = urllib.request.Request(t['url'] + '/api/agents', method='PUT',
                                 data=json.dumps(specs).encode(),
                                 headers={'Content-Type': 'application/json'})
    with urllib.request.urlopen(req) as response:
        json.load(response)


def new_session(t, name, extra=None):
    body = {'target_id': t['target_id'], 'workdir': str(t['root']), 'agent': 'claude',
            'model': 'fable', 'name': name}
    body.update(extra or {})
    return t['api']('/sessions', body)


def open_switch_sheet(page, t, source):
    page.goto(f"{t['url']}/#terminals/session/{source['id']}")
    trigger = page.get_by_role('button', name='Switch agent or model')
    expect(trigger).to_be_visible()
    trigger.click()
    sheet = page.get_by_role('dialog', name='Switch agent', exact=True)
    expect(sheet).to_be_visible()
    return sheet


@pytest.mark.parametrize('width', [390, 1440])
def test_switch_progress_survives_reload_then_goes_back(page, real_terminal, tmp_path, width):
    t = real_terminal
    install_agents(t, tmp_path)
    source = new_session(t, 'Continuity source')
    page.set_viewport_size({'width': width, 'height': 844})
    sheet = open_switch_sheet(page, t, source)
    sheet.locator('section[aria-label="Codex"]').get_by_role('button', name='astra-test', exact=True).click()
    expect(sheet).not_to_be_visible()

    panel = page.locator('.switch-progress')
    expect(panel).to_be_visible()
    steps = panel.locator('.switch-steps li')
    # The backend is still writing the wrap; nothing claims Ready yet.
    expect(steps.nth(0)).to_contain_text('Saving context')
    expect(steps.nth(0)).to_have_attribute('data-state', 'active')
    expect(steps.nth(2)).to_contain_text('Ready')
    # The destination is named even before the successor exists.
    expect(steps.nth(1)).to_contain_text('astra-test')
    assert page.evaluate('document.documentElement.scrollWidth<=innerWidth')
    page.screenshot(path=f'/tmp/lectern-continuity-progress-{width}.png')

    # A reload must not lose the switch: the phase comes back from the API.
    page.reload()
    expect(page.locator('.switch-progress')).to_be_visible()

    expect(page).not_to_have_url(re.compile(f'/session/{source["id"]}$'), timeout=30000)
    successor_id = int(page.url.rsplit('/', 1)[-1])
    successor = t['api']('/sessions/' + str(successor_id))
    assert successor['agent'] == 'codex' and successor['model'] == 'astra-test'
    assert successor['predecessor_id'] == source['id']
    # The original is preserved until (and after) the successor is available.
    assert t['api']('/sessions/' + str(source['id']))['ended_at'] is None
    subprocess.run(['tmux', 'has-session', '-t', '=' + source['tmux_session']], env=t['env'], check=True)
    assert page.evaluate("JSON.parse(sessionStorage.getItem('lec-pending-switches'))") == {}

    # Lineage: go back to the predecessor without killing or relaunching it.
    back = page.get_by_role('button', name=re.compile('^Go back to '))
    expect(back).to_be_visible()
    back.click()
    page.wait_for_url(re.compile(f'/session/{source["id"]}$'))
    subprocess.run(['tmux', 'has-session', '-t', '=' + source['tmux_session']], env=t['env'], check=True)
    assert t['api']('/sessions/' + str(successor_id))['ended_at'] is None


def test_failed_successor_keeps_original_and_offers_retry(page, real_terminal, tmp_path):
    t = real_terminal
    install_agents(t, tmp_path)
    branch = subprocess.check_output(
        ['git', '-C', str(t['root']), 'branch', '--show-current'], text=True).strip()
    project = t['api']('/projects', {'name': 'Strict continuity', 'target_id': t['target_id'],
                                     'repo_path': str(t['root']), 'default_base_branch': branch})
    source = new_session(t, 'Strict source', {'project_id': project['id']})
    # Codex cannot launch a strict-MCP project, so the wrap succeeds and the
    # successor launch fails — after the request has already been accepted.
    _write(t, '/projects/' + str(project['id']), {'strict_mcp': True})

    sheet = open_switch_sheet(page, t, source)
    sheet.locator('section[aria-label="Codex"]').get_by_role('button', name='astra-test', exact=True).click()
    expect(sheet).not_to_be_visible()
    panel = page.locator('.switch-progress.failed')
    expect(panel).to_be_visible(timeout=30000)
    expect(panel.get_by_role('alert')).to_contain_text('successor failed to start')
    assert t['api']('/sessions/' + str(source['id']))['ended_at'] is None
    subprocess.run(['tmux', 'has-session', '-t', '=' + source['tmux_session']], env=t['env'], check=True)
    assert t['api']('/sessions/' + str(source['id'])).get('successor_id') is None

    # Retry asks again for the same destination instead of pretending it worked.
    panel.get_by_role('button', name='Retry').click()
    expect(page.locator('.switch-progress.failed')).to_have_count(0, timeout=10000)
    expect(page.locator('.switch-progress')).to_contain_text('Saving context')
    expect(page.locator('.switch-progress.failed')).to_be_visible(timeout=30000)
    page.locator('.switch-progress').get_by_role('button', name='Dismiss switch progress').click()
    expect(page.locator('.switch-progress')).to_have_count(0)
    assert page.evaluate("JSON.parse(sessionStorage.getItem('lec-pending-switches'))") == {}


def test_switch_favorites_persist_and_deleted_profile_degrades(page, real_terminal, tmp_path):
    t = real_terminal
    install_agents(t, tmp_path)
    source = new_session(t, 'Favorite source')
    profile = t['api']('/launch-profiles', {'name': 'DeepSeek', 'agent': 'codex',
                                            'model': 'deepseek-test',
                                            'env_json': '{"SWITCH_PROVIDER":"deepseek-fixture"}'})
    page.set_viewport_size({'width': 390, 'height': 844})
    sheet = open_switch_sheet(page, t, source)
    sheet.get_by_role('button', name='Add DeepSeek to favorites').click()
    sheet.get_by_role('button', name='Add Codex · astra-test to favorites').click()
    favorites = sheet.locator('section[aria-label="Favorites"]')
    expect(favorites).to_be_visible()
    expect(favorites.get_by_role('button', name=re.compile('^DeepSeek'))).to_be_visible()
    expect(favorites.get_by_role('button', name=re.compile('^Codex · astra-test'))).to_be_visible()
    assert page.evaluate('document.documentElement.scrollWidth<=innerWidth')
    assert page.evaluate("getComputedStyle(document.querySelector('#switch-model')).fontSize") == '16px'
    assert page.evaluate("localStorage.getItem('lec-switch-favorites')").count('deepseek') == 1
    page.screenshot(path='/tmp/lectern-continuity-favorites-390.png')

    # Per-device: reopen the picker in the same device and the favorites are there.
    page.reload()
    sheet = open_switch_sheet(page, t, source)
    expect(sheet.locator('section[aria-label="Favorites"]').get_by_role('button', name=re.compile('^DeepSeek'))).to_be_visible()
    sheet.get_by_role('button', name='Close switcher').click()

    # A provider removed elsewhere must degrade in place, not break the picker.
    req = urllib.request.Request(t['url'] + '/api/launch-profiles/' + str(profile['id']), method='DELETE')
    with urllib.request.urlopen(req) as response:
        response.read()
    page.reload()
    sheet = open_switch_sheet(page, t, source)
    stale = sheet.locator('section[aria-label="Favorites"]').get_by_role('button', name=re.compile('^DeepSeek'))
    expect(stale).to_contain_text('Unavailable on this device')
    expect(stale).to_be_disabled()
    sheet.get_by_role('button', name='Remove DeepSeek from favorites').click()
    expect(sheet.locator('section[aria-label="Favorites"]').get_by_role('button', name=re.compile('^DeepSeek'))).to_have_count(0)
    # The discovered models and the exact-id field are still offered.
    expect(sheet.locator('section[aria-label="Codex"]').get_by_role('button', name='astra-test', exact=True)).to_be_visible()
    sheet.locator('details.switch-custom summary').click()
    expect(sheet.locator('#switch-model')).to_be_visible()


def test_failed_open_keeps_successor_and_reopens_without_a_second_handoff(page, real_terminal, tmp_path):
    t = real_terminal
    install_agents(t, tmp_path)
    source = new_session(t, 'Attach retry source')
    # Every attach attempt fails until the test lets one through: the successor
    # session is real, only this browser cannot open it.
    fail = {'on': True}

    def handler(route):
        if route.request.method == 'POST' and fail['on']:
            route.fulfill(status=503, content_type='application/json',
                          body=json.dumps({'detail': 'terminal unavailable'}))
            return
        route.continue_()

    page.route('**/api/sessions/*/terminal', handler)
    sheet = open_switch_sheet(page, t, source)
    sheet.locator('section[aria-label="Codex"]').get_by_role('button', name='astra-test', exact=True).click()
    expect(sheet).not_to_be_visible()
    panel = page.locator('.switch-progress.failed')
    expect(panel).to_be_visible(timeout=30000)
    expect(panel).to_contain_text('could not open it')
    expect(panel.get_by_role('alert')).to_contain_text('terminal unavailable')
    wraps = t['api']('/sessions/' + str(source['id']) + '/wraps')
    assert len(wraps) == 1 and wraps[0]['next_session_id']
    successor_id = wraps[0]['next_session_id']

    # The recovery offered for an existing successor only reopens it.
    fail['on'] = False
    panel.get_by_role('button', name='Open new session').click()
    page.wait_for_url(re.compile(f'/session/{successor_id}$'), timeout=20000)
    assert len(t['api']('/sessions/' + str(source['id']) + '/wraps')) == 1
    assert t['api']('/sessions/' + str(successor_id))['predecessor_id'] == source['id']
    assert page.evaluate("JSON.parse(sessionStorage.getItem('lec-pending-switches'))") == {}


def test_edited_provider_favorite_uses_current_metadata(page, real_terminal, tmp_path):
    t = real_terminal
    install_agents(t, tmp_path)
    source = new_session(t, 'Edited favorite source')
    profile = t['api']('/launch-profiles', {'name': 'DeepSeek', 'agent': 'codex',
                                            'model': 'deepseek-test',
                                            'env_json': '{"SWITCH_PROVIDER":"deepseek-fixture"}'})
    page.set_viewport_size({'width': 390, 'height': 844})
    sheet = open_switch_sheet(page, t, source)
    sheet.get_by_role('button', name='Add DeepSeek to favorites').click()
    # The provider is edited elsewhere: same identity, different agent and model.
    _write(t, '/launch-profiles/' + str(profile['id']),
           {'name': 'DeepSeek', 'agent': 'claude', 'model': 'deepseek-v2',
            'env_json': '{"SWITCH_PROVIDER":"deepseek-fixture"}'}, method='PUT')
    page.reload()
    sheet = open_switch_sheet(page, t, source)
    favorite = sheet.locator('section[aria-label="Favorites"]').get_by_role('button', name=re.compile('^DeepSeek'))
    expect(favorite).to_contain_text('Claude · deepseek-v2')
    expect(sheet.locator('section[aria-label="Saved providers"]').get_by_role('button', name='Remove DeepSeek from favorites')).to_have_attribute('aria-pressed', 'true')
    favorite.click()
    expect(sheet).not_to_be_visible()
    expect(page).not_to_have_url(re.compile(f'/session/{source["id"]}$'), timeout=30000)
    successor = t['api']('/sessions/' + page.url.rsplit('/', 1)[-1])
    assert successor['agent'] == 'claude' and successor['model'] == 'deepseek-v2'
    assert successor['launch_profile'] == 'DeepSeek'
    assert successor['predecessor_id'] == source['id']


def seed_pending_switch(page, source):
    """A switch the app believes is running: the state a reload restores."""
    page.evaluate(
        """([id]) => sessionStorage.setItem('lec-pending-switches', JSON.stringify({
            [id]: {after: 0, generation: 0, destination: 'Codex · astra-test',
                   agent: 'codex', model: 'astra-test', profile: 0}
        }))""",
        [source['id']],
    )
    page.reload()


def test_retry_after_failure_before_first_wrap_restarts_progress(page, real_terminal, tmp_path):
    t = real_terminal
    install_agents(t, tmp_path)
    source = new_session(t, 'Never wrapped source')
    page.goto(f"{t['url']}/#terminals/session/{source['id']}")
    seed_pending_switch(page, source)
    panel = page.locator('.switch-progress.failed')
    expect(panel).to_be_visible(timeout=15000)
    expect(panel.get_by_role('alert')).to_contain_text('stopped before a new session started')
    assert t['api']('/sessions/' + str(source['id']) + '/wraps') == []

    panel.get_by_role('button', name='Retry').click()
    # The wrap watermark does not change here (there was never a wrap), so only a
    # new request generation can restart the progress surface.
    expect(page.locator('.switch-progress.failed')).to_have_count(0, timeout=10000)
    expect(page.locator('.switch-progress')).to_contain_text('Saving context')
    expect(page).not_to_have_url(re.compile(f'/session/{source["id"]}$'), timeout=30000)
    assert len(t['api']('/sessions/' + str(source['id']) + '/wraps')) == 1
    assert t['api']('/sessions/' + str(source['id']))['ended_at'] is None


def test_rejected_retry_shows_the_post_error_and_starts_nothing(page, real_terminal, tmp_path):
    t = real_terminal
    install_agents(t, tmp_path)
    source = new_session(t, 'Rejected retry source')
    page.goto(f"{t['url']}/#terminals/session/{source['id']}")
    seed_pending_switch(page, source)
    panel = page.locator('.switch-progress.failed')
    expect(panel).to_be_visible(timeout=15000)
    # The API rejects the retry: the operator has to see that, not a generic
    # failure, and no handoff may start.
    page.route('**/api/sessions/*/handoff', lambda route: route.fulfill(
        status=422, content_type='application/json',
        body=json.dumps({'detail': 'no such agent'})))
    panel.get_by_role('button', name='Retry').click()
    expect(panel.get_by_role('alert')).to_contain_text('no such agent', timeout=10000)
    assert t['api']('/sessions/' + str(source['id']) + '/wraps') == []
    assert t['api']('/sessions/' + str(source['id']))['ended_at'] is None
    pending = page.evaluate("JSON.parse(sessionStorage.getItem('lec-pending-switches'))")
    assert pending[str(source['id'])]['error']


def test_fresh_mobile_landing_is_sessions(page, server):
    page.set_viewport_size({'width': 390, 'height': 844})
    page.goto(server)
    expect(page.locator('.tab[data-tab="sessions"]')).to_have_class(re.compile(r'\bon\b'))
    # Desktop keeps the board as its landing view.
    page.set_viewport_size({'width': 1440, 'height': 900})
    page.goto(server)
    expect(page.locator('.tab[data-tab="board"]')).to_have_class(re.compile(r'\bon\b'))
