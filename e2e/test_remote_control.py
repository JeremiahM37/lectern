"""Claude Remote Control (docs/remote-control.md) from the web: a checkbox in
the New session dialog for claude only, an "Enable Remote Control" entry in a
card's ⋯ menu, a quiet mark on cards that have it and an "Open in Claude" link
in their menu. The mock agent answers /remote-control the way claude does."""
from playwright.sync_api import expect

PHONE_412 = {"width": 412, "height": 839}


def _session(page, server, name, **extra):
    body = {"name": name, "scratch": True, "agent": "claude", **extra}
    r = page.request.post(server + "/api/sessions", data=body)
    assert r.ok, r.text()
    return r.json()


def test_new_session_dialog_offers_remote_control_for_claude_only(page, server):
    page.goto(server + "/#sessions")
    page.click("#sess-new")
    expect(page.locator("#ns-agent")).to_be_visible(timeout=10000)
    page.select_option("#ns-agent", "claude")
    box = page.locator("#ns-remote-control")
    expect(box).to_be_visible()
    expect(box).not_to_be_checked()
    box.check()
    expect(page.locator("#ns-remote-control-hint")).to_contain_text("claude.ai/code")
    page.select_option("#ns-agent", "codex")
    expect(box).to_have_count(0)


def test_enable_from_the_card_menu_then_mark_and_link(page, server):
    _session(page, server, "Phone later")
    page.set_viewport_size(PHONE_412)
    page.goto(server + "/#sessions")
    card = page.locator("#sesslist .scard", has_text="Phone later")
    expect(card).to_be_visible(timeout=30000)
    expect(card.locator(".session-rc")).to_have_count(0)
    expect(card.locator(".spane")).not_to_be_empty(timeout=15000)
    card.locator(".action-menu > summary").click()
    card.get_by_role("button", name="Enable Remote Control").click()
    expect(card.locator(".session-rc")).to_be_visible(timeout=15000)
    card.locator(".action-menu > summary").click()
    link = card.get_by_role("link", name="Open in Claude")
    expect(link).to_have_attribute("href", __import__("re").compile(r"^https://claude\.ai/code/session_"), timeout=15000)
    expect(card.get_by_role("button", name="Enable Remote Control")).to_have_count(0)


def test_remote_control_cards_keep_the_phone_budget(page, server):
    """The mark sits in the title row like the favorite star, so four cards
    still fit on a phone screen."""
    for i in range(6):
        _session(page, server, "RC row %d" % i, remote_control=True)
    page.set_viewport_size(PHONE_412)
    page.goto(server + "/#sessions")
    expect(page.locator("#sesslist .scard")).to_have_count(6, timeout=30000)
    expect(page.locator("#sesslist .session-rc")).to_have_count(6)
    visible = page.evaluate(
        """() => {
          const bar = document.querySelector('#tabbar');
          const bottom = bar && getComputedStyle(bar).display !== 'none'
            ? bar.getBoundingClientRect().top : innerHeight;
          return [...document.querySelectorAll('#sesslist .scard')].filter(card => {
            const r = card.getBoundingClientRect();
            return r.height > 0 && r.top >= 0 && r.bottom <= bottom + 1;
          }).length;
        }"""
    )
    assert visible >= 4, visible


def test_codex_card_has_no_remote_control_entry(page, server):
    r = page.request.post(server + "/api/sessions", data={"name": "Codex only", "scratch": True, "agent": "codex"})
    assert r.ok, r.text()
    page.goto(server + "/#sessions")
    card = page.locator("#sesslist .scard", has_text="Codex only")
    expect(card).to_be_visible(timeout=30000)
    card.locator(".action-menu > summary").click()
    expect(card.get_by_role("button", name="Enable Remote Control")).to_have_count(0)
