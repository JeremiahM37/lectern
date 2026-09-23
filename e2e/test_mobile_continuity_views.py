"""The same handoff can be followed from a session card and from its chat."""
import time
from playwright.sync_api import expect
from test_terminal_workspace import real_terminal
from test_session_continuity import install_agents, new_session


def test_chat_lineage_keeps_each_draft_and_terminal_escape(page, real_terminal, tmp_path):
    t = real_terminal
    install_agents(t, tmp_path)
    source = new_session(t, 'Conversation thread')
    t['api']('/sessions/' + str(source['id']) + '/handoff', {
        'successor': True, 'kill_old': False, 'quick_switch': True,
        'agent': 'codex', 'model': 'astra-test'})
    deadline = time.monotonic() + 25
    while time.monotonic() < deadline:
        view = t['api']('/sessions/' + str(source['id']))
        if view.get('successor_id'):
            break
        time.sleep(.2)
    assert view.get('successor_id'), view
    page.set_viewport_size({'width': 320, 'height': 844})
    page.goto(t['url'] + '/#sessions')
    card = page.locator(f'.scard[data-session-id="{source["id"]}"]')
    expect(card.get_by_role('navigation', name='Conversation lineage')).to_be_visible()
    card.locator('.chat-open').click()
    expect(page.locator('#conversation-status')).to_contain_text('claude')
    page.locator('#conversation-input').fill('Keep my first-agent draft')
    page.locator('.conversation-lineage .lineage-open').click()
    expect(page.locator('#conversation-status')).to_contain_text('codex')
    expect(page.locator('#conversation-input')).to_have_value('')
    page.locator('.conversation-lineage .lineage-open').click()
    expect(page.locator('#conversation-status')).to_contain_text('claude')
    expect(page.locator('#conversation-input')).to_have_value('Keep my first-agent draft')
    assert page.evaluate('document.documentElement.scrollWidth <= innerWidth')
    page.get_by_role('button', name='⌨ Open terminal', exact=True).click()
    expect(page).to_have_url(t['url'] + '/#terminals/session/' + str(source['id']))
    assert t['api']('/sessions/' + str(source['id']))['ended_at'] is None


def test_terminal_bootstrap_recovers_without_an_online_event(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({'width': 390, 'height': 844})
    requests = []
    def initial_failure(route):
        requests.append(route.request.url)
        route.fulfill(status=503, content_type='application/json', body='{"detail":"temporary startup outage"}')
    page.route('**/api/term/**/info', initial_failure, times=1)
    page.goto(t['url'] + '/#terminals/session/' + str(t['id']))
    frame = page.frame_locator('iframe')
    expect(frame.locator('#connection')).to_have_text('Connected', timeout=20000)
    assert len(requests) == 1
    expect(frame.locator('#notice')).not_to_be_visible()
