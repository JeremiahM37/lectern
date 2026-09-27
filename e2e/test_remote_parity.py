"""Remote parity: Race N agents, machines (SSH import, ports, editor) and
usage by provider, on a phone and on a desktop."""
import json
import urllib.request

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE
from test_ui import _tab


def _api(base, path, body=None, method=None):
    req = urllib.request.Request(base + "/api" + path, method=method or ("POST" if body is not None else "GET"),
                                 data=None if body is None else json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"})
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
    page.click("#qb-mode button >> nth=0")  # leave the bar in dispatch mode for other tests


def test_machine_ports_editor_and_usage_by_provider(page, server):
    page.goto(server)
    target = _api(server, "/targets")[0]
    _api(server, f"/targets/{target['id']}/ssh", {"editor_host": "devbox"}, method="PUT")
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
    proc = _start(port, {"LECTERN_SSH_CONFIG": str(cfg), "LECTERN_LIVE": "1"})
    base = f"http://127.0.0.1:{port}"
    try:
        for viewport in (PHONE, DESKTOP):
            ctx = browser.new_context(viewport=viewport)
            page = ctx.new_page()
            page.goto(base)
            _settings(page, "machines")
            existing = [t for t in _api(base, "/targets") if t["name"] == "build-box"]
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
            card.locator(".remote-listening li", has_text="5173").get_by_role("button", name="Forward").click()
            expect(card.locator('[data-forward="5173"]')).to_be_visible(timeout=10000)
            card.locator('[data-forward="5173"]').get_by_role("button", name="Stop").click()
            expect(card.locator('[data-forward="5173"]')).to_have_count(0, timeout=10000)
            card.locator('[data-remote-tab="files"]').click()
            expect(card.get_by_role("link", name="Download")).to_be_visible()
            ctx.close()
    finally:
        _stop(proc, port)
