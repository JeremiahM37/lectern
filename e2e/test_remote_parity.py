"""Remote parity: Race N agents, machines (SSH import, ports, editor) and
usage by provider, on a phone and on a desktop."""
import json
import urllib.request

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE
from test_ui import _tab


def _api(base, path, body=None, method=None, token=None):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = "Bearer " + token
    req = urllib.request.Request(base + "/api" + path, method=method or ("POST" if body is not None else "GET"),
                                 data=None if body is None else json.dumps(body).encode(), headers=headers)
    return json.load(urllib.request.urlopen(req, timeout=10))


def _settings(page, section):
    _tab(page, "targets")
    page.locator(f'[data-settings="{section}"]').first.click()


@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True, ids=["phone", "desktop"])
def test_race_three_agents_lands_in_compare_in_three_clicks(page, server):
    page.goto(server + "/#board")
    page.click("#fab")                                   # 1
    page.click('#f-race [data-race="3"]')                # 2
    expect(page.locator("#f-variants .variant-row")).to_have_count(2)
    page.fill("#f-prompt", "Add a /health endpoint")
    page.click("#f-race-go")                             # 3 — no title needed
    compare = page.locator(".compare-view")
    expect(compare).to_be_visible(timeout=10000)
    expect(compare.locator(".compare-card")).to_have_count(3)
    task = next(t for t in _api(server, "/tasks") if t["title"] == "Add a /health endpoint")
    assert len(task["attempts"]) == 3


@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True, ids=["phone", "desktop"])
def test_quick_bar_race_mode(page, server):
    page.goto(server + "/#board")
    page.click("#qb-race")
    page.fill("#qb-input", "Race the quick bar")
    page.press("#qb-input", "Enter")
    expect(page.locator(".compare-view .compare-card")).to_have_count(3, timeout=10000)


def test_machine_ports_editor_and_usage_by_provider(page, server):
    page.goto(server)
    target = _api(server, "/targets")[0]
    _settings(page, "machines")
    expect(page.locator("article.rowcard").filter(has_text=target["name"]).first).to_be_visible()
    _settings(page, "about")
    usage = page.locator(".provider-usage")
    expect(usage).to_be_visible(timeout=10000)
    for name in ("Claude Code", "Codex", "Gemini CLI"):
        expect(usage.locator(".pu-card h4", has_text=name)).to_be_visible()
    expect(usage.locator(".pu-head input")).to_have_value("80")


def test_ssh_import_sheet_imports_a_host(browser, tmp_path):
    """A second server with its own SSH config: import from the sheet, then the
    machine's panels are there."""
    from conftest import _start, _stop, _unused_port
    cfg = tmp_path / "ssh_config"
    cfg.write_text("Host build-box\n  HostName 10.0.0.5\n  User dev\n  Port 2222\n  ProxyJump bastion\n")
    port = _unused_port()
    # Token mode: importing reads this server's SSH config, which only a
    # signed-in person may do, and a forwarded port needs the explicit opt-in.
    token = "remote-parity-token"
    proc = _start(port, {"LECTERN_SSH_CONFIG": str(cfg), "LECTERN_LIVE": "1", "LECTERN_AUTH": "token",
                         "LECTERN_AUTH_TOKEN": token, "LECTERN_LIVE_UNAUTHENTICATED": "1"})
    base = f"http://127.0.0.1:{port}"
    try:
        for viewport in (PHONE, DESKTOP):
            ctx = browser.new_context(viewport=viewport)
            ctx.add_init_script(f"localStorage.setItem('lec-token', '{token}')")
            page = ctx.new_page()
            page.goto(base)
            _settings(page, "machines")
            existing = [t for t in _api(base, "/targets", token=token) if t["name"] == "build-box"]
            if not existing:
                page.click("#ssh-import")
                host = page.locator('.ssh-hosts li[data-alias="build-box"]')
                expect(host).to_contain_text("via bastion")
                page.click("#ssh-import-go")
            card = page.locator("article.rowcard").filter(has_text="build-box").first
            expect(card.locator(".remote-tabs")).to_be_visible(timeout=10000)
            card.locator('[data-remote-tab="connection"]').click()
            expect(card.get_by_label("Jump hosts (ProxyJump)")).to_have_value("bastion")
            expect(card.get_by_label("ssh_config alias")).to_have_value("build-box")
            card.locator('[data-remote-tab="ports"]').click()
            card.get_by_role("button", name="Detect listening ports").click()
            expect(card.locator(".remote-listening li")).to_have_count(2, timeout=10000)
            # Mock machines have no loopback to reach, and say so rather than
            # pretending (real forwards are covered against a real target).
            card.locator(".remote-listening li", has_text="5173").get_by_role("button", name="Forward").click()
            expect(page.get_by_text("cannot forward ports").first).to_be_visible(timeout=10000)
            card.locator('[data-remote-tab="files"]').click()
            card.get_by_label("Path on build-box").fill("/srv/app")
            link = card.get_by_role("link", name="Download")
            expect(link).to_have_attribute("href", "/api/targets/%d/download?path=%%2Fsrv%%2Fapp&token=%s"
                                           % (next(t["id"] for t in _api(base, "/targets", token=token) if t["name"] == "build-box"), token))
            ctx.close()
    finally:
        _stop(proc, port)
