"""Workshop controls against intercepted API responses; never dispatch agents.

Run only via tools/run-isolated-tests.sh, like the rest of the e2e suite.
"""
import copy
import time

import pytest
from playwright.sync_api import expect

from conftest import BASE, DESKTOP, PHONE


def snapshot():
    return {
        "config": {"enabled": False, "timezone": "America/Denver", "morning_hour": 8,
                   "reserve_percent": 10, "margin_percent": 5},
        "status": "off", "reason": "The workshop is off.",
        "state": {
            "date": "2026-09-24", "phase": "review", "revision": 0,
            "items": [{"project_id": 1, "title": '<img src=x onerror="window.attacked=1">',
                       "why": "Protect useful work", "acceptance": ["Tests pass"]}],
            "assignments": [{"task_id": 42, "role": "builder", "completed": True}],
            "reports": {"42": {"summary": "Built a useful improvement", "evidence": ["Tests passed"]}},
        },
        "jobs": [{"id": "12ad0470-c606-4b30-a13c-8fae1198b3ec", "task_id": 42,
                  "role": "builder", "provider": "codex", "status": "done",
                  "artifact_path": "/private/workshop/job42", "summary": "Ready for review"}],
        "runs": [{"date": "2026-09-23", "phase": "complete", "reason": "Nothing worthwhile proposed"}],
        "quota": {"providers": [{"id": "codex", "status": "ok", "updated_at": time.time(),
                    "buckets": [{"id": "plan", "windows": [
                        {"label": "Session", "remaining_percent": 64, "resets_at": time.time() + 3600},
                        {"label": "Weekly", "remaining_percent": 27, "resets_at": time.time() + 86400},
                    ]}]}]},
    }


def mock_api(page, state, reject=False):
    calls = []

    def route_request(route):
        request = route.request
        if request.method != "GET":
            calls.append((request.method, request.url.split("/api/autonomy", 1)[1], request.post_data_json))
            if reject:
                route.fulfill(status=403, json={"error": "Human owner identity required"})
                return
            if request.method == "PUT":
                state["config"]["enabled"] = request.post_data_json["enabled"]
            elif request.url.endswith("/stop"):
                state["config"]["enabled"] = False
            state["status"] = "on" if state["config"]["enabled"] else "off"
        route.fulfill(json=state)

    page.route("**/api/autonomy**", route_request)
    return calls


@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True)
def test_workshop_readable_records_and_budget(page):
    mock_api(page, snapshot())
    page.goto(BASE + "/autonomy.html")
    expect(page.locator("#status")).to_have_text("OFF")
    expect(page.locator("#quota")).to_contain_text("64% left")
    expect(page.locator("#quota")).to_contain_text("27% left")
    expect(page.locator("#quota")).to_contain_text("Resets")
    expect(page.locator("#threshold")).to_have_text("Above 15% remaining")
    expect(page.locator("#today")).to_contain_text('<img src=x onerror="window.attacked=1">')
    assert page.locator("#today img").count() == 0
    assert page.evaluate("window.attacked") is None
    expect(page.locator('#today a[href="/#task/42"]')).to_have_count(1)
    expect(page.get_by_role("link", name="Download files")).to_have_attribute(
        "href", "/api/autonomy/jobs/12ad0470-c606-4b30-a13c-8fae1198b3ec/archive")
    expect(page.locator("#today")).not_to_contain_text("/private/workshop")
    expect(page.locator("#today")).to_contain_text("Tests passed")
    expect(page.locator("#history")).to_contain_text("Nothing worthwhile proposed")
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")


def test_workshop_toggle_start_and_stop_use_owned_endpoints(page):
    calls = mock_api(page, snapshot())
    page.goto(BASE + "/autonomy.html")
    toggle = page.get_by_role("switch", name="Autonomous mode")
    expect(toggle).to_be_enabled()
    toggle.click()
    expect(toggle).to_have_attribute("aria-checked", "true")
    assert calls[-1] == ("PUT", "", {"enabled": True})
    page.get_by_role("button", name="Start today’s cycle").click()
    expect(toggle).to_be_enabled()
    assert calls[-1] == ("POST", "/run", {})
    toggle.click()
    expect(toggle).to_have_attribute("aria-checked", "false")
    assert calls[-1] == ("POST", "/stop", {})
    page.get_by_role("button", name="Turn off & stop work").click()
    expect(toggle).to_be_enabled()
    assert calls[-1] == ("POST", "/stop", {})


def test_workshop_forbidden_enable_stays_off(page):
    calls = mock_api(page, snapshot(), reject=True)
    page.goto(BASE + "/autonomy.html")
    toggle = page.get_by_role("switch", name="Autonomous mode")
    expect(toggle).to_be_enabled()
    toggle.click()
    expect(page.get_by_role("alert")).to_contain_text("Only the owner")
    expect(page.get_by_role("alert")).to_contain_text("Human owner identity required")
    expect(toggle).to_have_attribute("aria-checked", "false")
    assert calls == [("PUT", "", {"enabled": True})]


def test_workshop_unknown_or_stale_allowance_is_visible(page):
    state = snapshot()
    state["quota"] = None
    mock_api(page, state)
    page.goto(BASE + "/autonomy.html")
    expect(page.locator("#quota")).to_contain_text("Allowance data is unavailable")
    state["quota"] = copy.deepcopy(snapshot()["quota"])
    state["quota"]["providers"][0]["updated_at"] -= 900
    page.get_by_role("button", name="Refresh", exact=True).click()
    expect(page.locator("#quota")).to_contain_text("stale reading")
    expect(page.locator("#quota")).to_contain_text("64% left (stale)")


@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True)
def test_workshop_compact_embed_keeps_controls_visible(page):
    mock_api(page, snapshot())
    page.goto(BASE + "/autonomy.html?compact=1")
    expect(page.get_by_role("switch", name="Autonomous mode")).to_be_enabled()
    expect(page.get_by_role("switch", name="Autonomous mode")).to_be_in_viewport()
    expect(page.get_by_role("button", name="Turn off & stop work")).to_be_in_viewport()
    expect(page.locator("header")).to_be_hidden()
    expect(page.locator(".history")).to_be_hidden()
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")


def test_workshop_only_finished_artifacts_offer_downloads(page):
    state = snapshot()
    state["jobs"][0]["status"] = "running"
    mock_api(page, state)
    page.goto(BASE + "/autonomy.html")
    expect(page.locator("#today")).to_contain_text("builder · running")
    expect(page.get_by_role("link", name="Download files")).to_have_count(0)
    state["jobs"][0]["status"] = "stopped"
    page.get_by_role("button", name="Refresh", exact=True).click()
    expect(page.get_by_role("link", name="Download files")).to_be_visible()
    state["jobs"][0]["id"] = "../untrusted"
    page.get_by_role("button", name="Refresh", exact=True).click()
    expect(page.get_by_role("link", name="Download files")).to_have_count(0)
