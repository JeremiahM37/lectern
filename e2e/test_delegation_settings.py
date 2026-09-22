"""Delegated builds: the banner above the Settings tabs, its switch, and the
preset that installs a worker — against the real server in mock mode."""
import pytest
from playwright.sync_api import expect
from conftest import DESKTOP, PHONE
from test_ui import _tab


@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True, ids=["phone390", "desktop1440"])
def test_delegated_builds_banner_switch_and_preset(page, server):
    # The server is shared with every other suite: start from off, and put
    # everything back at the end (a leftover project that defaults to Codex
    # would become the task form's default for the tests that follow).
    def reset():
        page.request.put(f"{server}/api/delegation", data={"enabled": False})
        for p in page.request.get(f"{server}/api/projects").json():
            if p["name"] == "delegate fixture":
                page.request.delete(f"{server}/api/projects/{p['id']}")
        agents = page.request.get(f"{server}/api/agents").json()
        custom = [a for a in agents if not a.get("builtin") and a["name"] != "flash-builder"]
        page.request.put(f"{server}/api/agents", data=custom)

    reset()
    page.goto(server)
    _tab(page, "targets")
    banner = page.locator("#delegation")
    expect(banner).to_be_visible()
    # It is the first thing on the page, above the tabs, and says OFF in the title.
    expect(banner.locator("h3")).to_contain_text("Delegated builds")
    expect(banner.locator(".delegation-state")).to_have_text("OFF")
    tabs = page.locator('.settings-page nav[role="tablist"]')
    assert banner.bounding_box()["y"] < tabs.bounding_box()["y"]
    # The state is the largest text on the settings page.
    size = page.evaluate("getComputedStyle(document.querySelector('#delegation h3')).fontSize")
    assert float(size.rstrip("px")) >= 19
    switch = page.locator("#delegation-enabled")
    expect(switch).to_be_visible()
    # Turning it on without a worker is refused, with a notice, and stays off.
    switch.click()
    expect(banner.locator(".delegation-state")).to_have_text("OFF")
    # Set up the worker with the preset (no request leaves the box: mock mode).
    banner.get_by_role("button", name="Set up the worker").click()
    banner.get_by_placeholder("DeepSeek API key (sk-…)").fill("sk-not-a-real-key")
    banner.get_by_role("button", name="Install preset").click()
    expect(banner.locator("select").first).to_have_value("flash-builder")
    switch.click()
    expect(banner.locator(".delegation-state")).to_have_text("ON")
    expect(banner).to_have_class("delegation-banner on")
    state = page.request.get(f"{server}/api/delegation").json()
    assert state["settings"]["enabled"] is True and state["worker_ready"] is True
    # The key never comes back in clear.
    agents = page.request.get(f"{server}/api/agents").json()
    worker = next(a for a in agents if a["name"] == "flash-builder")
    assert worker["env"]["DEEPSEEK_API_KEY"] != "sk-not-a-real-key"
    # The lead skill can be installed into a project from the same card.
    target = page.request.get(f"{server}/api/targets").json()[0]
    page.request.post(f"{server}/api/projects", data={"name": "delegate fixture", "target_id": target["id"], "repo_path": "/mock/delegate", "default_agent": "codex"})
    # Staging a bundle needs a real target's home; the mock target has none,
    # so the workflow route is answered here and the request shape is checked.
    puts = []

    def route_enable(route):
        puts.append((route.request.method, route.request.url, route.request.post_data_json))
        route.fulfill(status=200, content_type="application/json", body='{"enabled": true}')

    page.route("**/api/projects/*/workflows/delegate", route_enable)
    page.reload()
    _tab(page, "targets")
    banner = page.locator("#delegation")
    banner.get_by_role("button", name="Set up the worker").click()
    banner.locator("select").last.select_option(label="delegate fixture")
    banner.get_by_role("button", name="for Codex").click()
    expect(page.get_by_text("Lead skill installed for codex")).to_be_visible()
    assert puts and puts[0][0] == "PUT" and puts[0][2] == {"agent": "codex", "enabled": True}
    reset()
    assert page.request.get(f"{server}/api/delegation").json()["settings"]["enabled"] is False
    assert not any(a["name"] == "flash-builder" for a in page.request.get(f"{server}/api/agents").json())
