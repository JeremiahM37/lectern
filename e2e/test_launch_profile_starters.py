"""Starter launch-profile cards, richer draft fields, and launch context.

The richer profile fields and ``/api/launch-profile-presets`` are owned by the
backend integration; this worktree only ships the frontend. The tests therefore
mock the profile and preset endpoints in the browser so the draft, validation,
busy and reload behavior is exercised for real, without fabricating a passing
server contract.
"""

import json
import re

import pytest
from playwright.sync_api import expect

from test_terminal_workspace import real_terminal
from session_sheet import open_advanced

PRESETS = [
    {
        "key": "lean-builder",
        "name": "Lean Builder",
        "agent": "codex",
        "description": "Ship the smallest coherent change and keep the diff easy to review.",
        "instructions": "Restate the goal in one line, then make the smallest change that solves it.",
    },
    {
        "key": "reviewed-delivery",
        "name": "Reviewed Delivery",
        "agent": "claude",
        "description": "Deliver with a review pass before anything is called done.",
        "instructions": "Plan the change, implement it, then review your own diff before reporting.",
    },
    {
        "key": "debugging-team",
        "name": "Debugging Team",
        "agent": "codex",
        "description": "Split diagnosis from the fix so neither half guesses.",
        "instructions": "Reproduce the failure with evidence before proposing a fix.",
    },
    {
        "key": "research-plan",
        "name": "Research & Plan",
        "agent": "claude",
        "description": "Research the options and land on a plan before writing code.",
        "instructions": "Gather evidence, compare options, and write the plan before editing files.",
    },
]

PROFILE_ROUTE = re.compile(r"/api/launch-profiles$")
ITEM_ROUTE = re.compile(r"/api/launch-profiles/\d+$")
PRESET_ROUTE = re.compile(r"/api/launch-profile-presets$")


def mock_launch_profiles(page, rows=None):
    """Serve the richer profile contract in-browser; keep state in Python."""
    state = {"next": max([row["id"] for row in (rows or [])], default=0) + 1,
             "rows": list(rows or []), "posts": [], "fail_posts": False}

    def list_handler(route):
        method = route.request.method
        if method == "GET":
            return route.fulfill(status=200, content_type="application/json", body=json.dumps(state["rows"]))
        if method == "POST":
            if state["fail_posts"]:
                state["fail_posts"] = False
                return route.fulfill(status=503, content_type="application/json", body='{"detail":"Temporary save failure"}')
            body = route.request.post_data_json or {}
            row = {"id": state["next"], **body}
            state["next"] += 1
            state["rows"].append(row)
            state["posts"].append(body)
            return route.fulfill(status=201, content_type="application/json", body=json.dumps(row))
        route.continue_()

    def item_handler(route):
        body = route.request.post_data_json or {}
        match = ITEM_ROUTE.search(route.request.url)
        profile_id = int(match.group(1))
        if route.request.method == "PUT":
            row = {"id": profile_id, **body}
            state["rows"] = [row if existing["id"] == profile_id else existing for existing in state["rows"]]
            return route.fulfill(status=200, content_type="application/json", body=json.dumps(row))
        if route.request.method == "DELETE":
            state["rows"] = [existing for existing in state["rows"] if existing["id"] != profile_id]
            return route.fulfill(status=204)
        route.continue_()

    def preset_handler(route):
        route.fulfill(status=200, content_type="application/json", body=json.dumps(PRESETS))

    page.route(PRESET_ROUTE, preset_handler)
    page.route(PROFILE_ROUTE, list_handler)
    page.route(ITEM_ROUTE, item_handler)
    return state


def open_profiles(page, t):
    page.goto(t["url"] + "/#sessions")
    page.locator("#sess-new").click()
    open_advanced(page)
    page.locator("#ns-manage-profiles").click()
    return page.get_by_role("dialog", name="Launch profiles", exact=True)


@pytest.mark.parametrize("width", [320, 390, 1440])
def test_starter_cards_open_an_unsaved_draft(page, real_terminal, width):
    t = real_terminal
    state = mock_launch_profiles(page)
    page.set_viewport_size({"width": width, "height": 844 if width < 500 else 900})
    dialog = open_profiles(page, t)
    expect(dialog.get_by_role("heading", name="Starter profiles")).to_be_visible()
    for preset in PRESETS:
        card = dialog.locator(".lp-starter", has_text=preset["name"])
        expect(card).to_be_visible()
        expect(card.locator(".lp-starter-purpose")).to_have_text(preset["description"])
    lean = dialog.locator(".lp-starter", has_text="Lean Builder")
    lean.get_by_role("button", name="Use starter").click()
    expect(dialog.get_by_label("Name", exact=True)).to_have_value("Lean Builder")
    expect(dialog.get_by_label("Description", exact=True)).to_have_value(PRESETS[0]["description"])
    expect(dialog.get_by_label("Workflow instructions", exact=True)).to_have_value(PRESETS[0]["instructions"])
    expect(dialog.get_by_label("Saved profile")).to_have_value("0")
    expect(dialog.locator(".lp-draft-note")).to_be_visible()
    # Applying a starter writes nothing and touches no saved profile.
    assert state["posts"] == [] and state["rows"] == []
    assert dialog.evaluate("(element) => element.scrollWidth <= element.clientWidth")


def test_switching_profiles_keeps_env_and_starter_stays_a_draft(page, real_terminal):
    t = real_terminal
    saved = {"id": 1, "name": "Work account", "agent": "claude", "command": "", "model": "sonnet",
             "env_json": '{"API_KEY":"kept"}', "description": "Use the work account.",
             "instructions": "Ask before spending credits."}
    state = mock_launch_profiles(page, [saved])
    page.set_viewport_size({"width": 390, "height": 844})
    dialog = open_profiles(page, t)
    dialog.locator(".lp-select").select_option("1")
    expected_env = json.dumps({"API_KEY": "kept"}, indent=2)
    expect(dialog.get_by_label("Environment (JSON)")).to_have_value(expected_env)
    expect(dialog.get_by_label("Description", exact=True)).to_have_value(saved["description"])
    # A starter is an unsaved draft: it never overwrites the selected profile.
    dialog.locator(".lp-starter", has_text="Debugging Team").get_by_role("button", name="Use starter").click()
    expect(dialog.get_by_label("Name", exact=True)).to_have_value("Debugging Team")
    expect(dialog.get_by_label("Environment (JSON)")).to_have_value("{}")
    assert state["rows"] == [saved]
    # Selecting the saved profile again restores its retained environment.
    dialog.locator(".lp-select").select_option("1")
    expect(dialog.get_by_label("Environment (JSON)")).to_have_value(expected_env)
    # Save the starter as a new profile and confirm description/instructions round-trip.
    dialog.locator(".lp-starter", has_text="Research & Plan").get_by_role("button", name="Use starter").click()
    dialog.get_by_label("Description", exact=True).fill("Research before building.")
    dialog.get_by_label("Workflow instructions", exact=True).fill("Write the plan first.")
    with page.expect_response(lambda r: r.request.method == "POST" and r.url.endswith("/launch-profiles")) as response:
        dialog.get_by_role("button", name="Save profile", exact=True).click()
    created = response.value.json()
    expect(dialog.locator(".lp-status")).to_contain_text("Profile saved")
    expect(dialog.get_by_label("Description", exact=True)).to_have_value("Research before building.")
    expect(dialog.get_by_label("Workflow instructions", exact=True)).to_have_value("Write the plan first.")
    # Reload and reopen: the saved fields come back from the list endpoint.
    page.reload()
    dialog = open_profiles(page, t)
    dialog.locator(".lp-select").select_option(str(created["id"]))
    expect(dialog.get_by_label("Description", exact=True)).to_have_value("Research before building.")
    expect(dialog.get_by_label("Workflow instructions", exact=True)).to_have_value("Write the plan first.")


@pytest.mark.parametrize("width", [320, 1440])
def test_launch_payload_references_profile_and_shows_its_description(page, real_terminal, width):
    t = real_terminal
    saved = {"id": 3, "name": "Reviewed Delivery", "agent": "claude", "command": "", "model": "",
             "env_json": "{}", "description": "Every change gets a review pass.",
             "instructions": "Review the diff before you report."}
    mock_launch_profiles(page, [saved])

    def sessions(route):
        if route.request.method == "POST":
            return route.fulfill(status=400, content_type="application/json", body='{"detail":"fixture stop"}')
        route.continue_()

    page.route(re.compile(r"/api/sessions$"), sessions)
    page.set_viewport_size({"width": width, "height": 844 if width < 500 else 900})
    page.goto(t["url"] + "/#sessions")
    page.locator("#sess-new").click()
    open_advanced(page)
    page.locator("#ns-profile").select_option("3")
    expect(page.locator("#ns-profile-hint")).to_contain_text("Every change gets a review pass.")
    expect(page.locator("#ns-agent")).to_have_value("claude")
    with page.expect_response(lambda r: r.request.method == "POST" and r.url.endswith("/api/sessions")) as response:
        page.locator("#ns-go").click()
    launched = response.value.request.post_data_json
    assert launched["profile_id"] == 3
    assert launched["agent"] == "claude"
    # The failed launch keeps the chosen profile and its description context.
    expect(page.locator("#ns-profile")).to_have_value("3")
    expect(page.locator("#ns-profile-hint")).to_contain_text("Every change gets a review pass.")


def test_busy_save_disables_controls_and_failure_keeps_the_draft(page, real_terminal):
    t = real_terminal
    state = mock_launch_profiles(page)
    page.set_viewport_size({"width": 390, "height": 844})
    dialog = open_profiles(page, t)
    dialog.locator(".lp-starter", has_text="Debugging Team").get_by_role("button", name="Use starter").click()
    # A failed save keeps the draft in the form.
    state["fail_posts"] = True
    dialog.get_by_role("button", name="Save profile", exact=True).click()
    expect(dialog.locator(".lp-status")).to_contain_text("Temporary save failure")
    expect(dialog.get_by_label("Name", exact=True)).to_have_value("Debugging Team")
    expect(dialog.get_by_label("Workflow instructions", exact=True)).to_have_value(PRESETS[2]["instructions"])
    # While a save is in flight selecting, New and every Use starter are disabled.
    page.evaluate(
        """() => { const f = window.fetch; window.fetch = (u, o) => String(u).endsWith('/launch-profiles') && o?.method === 'POST'
        ? new Promise((resolve) => setTimeout(() => resolve(f(u, o)), 500)) : f(u, o); }"""
    )
    with page.expect_response(lambda r: r.request.method == "POST" and r.url.endswith("/launch-profiles")):
        dialog.get_by_role("button", name="Save profile", exact=True).click()
        expect(dialog.get_by_role("button", name="Save profile", exact=True)).to_be_disabled()
        expect(dialog.get_by_role("button", name="Close", exact=True)).to_be_disabled()
        expect(dialog.locator(".lp-select")).to_be_disabled()
        expect(dialog.get_by_role("button", name="New", exact=True)).to_be_disabled()
        expect(dialog.locator(".lp-starter-use").first).to_be_disabled()
    expect(dialog.locator(".lp-status")).to_contain_text("Profile saved")


@pytest.mark.parametrize("width", [320, 390, 1440])
def test_shipped_starters_save_and_reload_against_real_server(page, real_terminal, width):
    t = real_terminal
    catalog = t['api']('/launch-profile-presets')
    assert len(catalog) == 4
    page.set_viewport_size({'width': width, 'height': 900})
    dialog = open_profiles(page, t)
    for preset in catalog:
        dialog.locator(f'.lp-starter[data-preset="{preset["key"]}"]').get_by_role('button', name='Use starter').click()
        expect(dialog.get_by_label('Name', exact=True)).to_have_value(preset['name'])
        expect(dialog.get_by_label('Workflow instructions', exact=True)).to_have_value(preset['instructions'])
        assert not any(p['name'] == preset['name'] for p in t['api']('/launch-profiles'))
        dialog.get_by_role('button', name='Save profile', exact=True).click()
        expect(dialog.locator('.lp-status')).to_contain_text('Profile saved')
        saved = next(p for p in t['api']('/launch-profiles') if p['name'] == preset['name'])
        assert saved['description'] == preset['description']
        assert saved['instructions'] == preset['instructions']
        assert saved['env_json'] == '{}' and saved['model'] == '' and saved['command'] == ''
        assert dialog.evaluate('(e) => e.scrollWidth <= e.clientWidth')
    page.reload()
    dialog = open_profiles(page, t)
    for saved in t['api']('/launch-profiles'):
        dialog.locator('.lp-select').select_option(str(saved['id']))
        expect(dialog.get_by_label('Workflow instructions', exact=True)).to_have_value(saved['instructions'])
