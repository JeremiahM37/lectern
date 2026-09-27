"""Real browser coverage for the searchable agent catalog in Settings → Agents.

With ~30 catalog agents the "Add agent" starter list is a grouped, searchable
list rather than a dropdown. This drives the real server: search narrows the
list, picking Grok CLI prefills its verified flags (including the new
prompt-arguments field), and saving it registers a real agent — which, like
every newly added agent, stays out of the pickers until it is shown.
"""
import json
import urllib.request

import pytest
from playwright.sync_api import expect


def _agents(server):
    return json.load(urllib.request.urlopen(f"{server}/api/agents", timeout=10))


def _put_agents(server, agents):
    custom = [{k: v for k, v in a.items() if k != "builtin"} for a in agents if not a.get("builtin")]
    req = urllib.request.Request(f"{server}/api/agents", method="PUT",
                                 data=json.dumps(custom).encode(),
                                 headers={"Content-Type": "application/json"})
    urllib.request.urlopen(req, timeout=10).read()


@pytest.mark.parametrize("width", [390, 1440])
def test_search_the_catalog_and_add_one_agent(page, server, width):
    before = _agents(server)
    assert not any(a["name"] == "grok" for a in before)
    try:
        page.set_viewport_size({"width": width, "height": 844})
        page.goto(server + "/#targets")
        page.locator('[data-settings="agents"]').click()
        page.get_by_role("button", name="Add agent", exact=True).click()
        dialog = page.get_by_role("dialog", name="Add agent", exact=True)

        # Every group is present before searching, and the list is long enough
        # that search is what makes it usable.
        for group in ("Popular", "Vendor agents", "Open source & community", "ACP adapters"):
            expect(dialog.get_by_role("group", name=group, exact=True)).to_be_visible(timeout=10000)
        assert dialog.locator(".agent-catalog-item[data-preset]").count() >= 30

        search = dialog.get_by_label("Search agent catalog")
        search.fill("xai")
        items = dialog.locator(".agent-catalog-item[data-preset]")
        expect(items).to_have_count(1)
        grok = dialog.locator('[data-preset="grok"]')
        expect(grok).to_contain_text("Grok CLI")
        expect(grok).to_contain_text("xAI")
        # capabilities are stated up front, including the ones it lacks
        expect(grok.locator(".agent-cap.on", has_text="ACP")).to_have_count(1)
        expect(grok.locator(".agent-cap.off", has_text="Skills")).to_have_count(1)

        search.fill("no-such-agent-anywhere")
        expect(items).to_have_count(0)
        expect(dialog.get_by_text("No catalog agent matches")).to_be_visible()

        search.fill("grok")
        grok.click()
        expect(grok).to_have_attribute("aria-pressed", "true")
        expect(dialog.get_by_label("Name", exact=True)).to_have_value("grok")
        expect(dialog.get_by_label("Command", exact=True)).to_have_value("grok")
        expect(dialog.get_by_label("Model flag", exact=True)).to_have_value("-m")
        prompt_args = dialog.locator("label", has_text="Opening prompt arguments").locator("textarea")
        expect(prompt_args).to_have_value('[\n  "--",\n  "{prompt}"\n]')
        expect(dialog.get_by_label("ACP command", exact=True)).to_have_value("grok")
        expect(dialog.locator(".agent-preset-hint")).to_contain_text("grok sessions list")
        assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")

        dialog.get_by_role("button", name="Save runner", exact=True).click()
        expect(page.locator(".agent-card", has_text="grok")).to_be_visible()
        saved = next(a for a in _agents(server) if a["name"] == "grok")
        assert saved["prompt_args"] == ["--", "{prompt}"]
        assert saved["resume_id_args"] == ["--resume", "{id}"]
        assert saved["acp"] == {"command": "grok", "args": ["agent", "stdio"]}
        # new agents are opt-in for pickers
        expect(page.locator(".agent-menu-hidden").get_by_label("grok", exact=True)).not_to_be_checked()

        # Once added, the catalog entry is shown as already added.
        page.get_by_role("button", name="Add agent", exact=True).click()
        dialog = page.get_by_role("dialog", name="Add agent", exact=True)
        dialog.get_by_label("Search agent catalog").fill("grok")
        expect(dialog.locator('[data-preset="grok"]')).to_be_disabled()
        expect(dialog.locator('[data-preset="grok"]')).to_contain_text("already added")
    finally:
        _put_agents(server, before)
