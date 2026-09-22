"""Project workflow settings through the real browser with a mocked workflow API."""

import json
import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE
from test_ui import _tab


@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True, ids=["phone390", "desktop1440"])
def test_project_workflows_provider_toggles_commands_retry_and_attribution(page, server):
    page.goto(server)
    target = page.request.get(f"{server}/api/targets").json()[0]
    project = page.request.post(
        f"{server}/api/projects",
        data={
            "name": "Workflow browser fixture",
            "target_id": target["id"],
            "repo_path": "/mock/workflows-ui",
            "default_agent": "claude",
        },
    ).json()
    project_id = project["id"]
    state = {
        "fail_next": False,
        "hold_next": False,
        "held_route": None,
        "enabled": {"claude": {"spec-kit": False, "maestro": False}, "codex": {"spec-kit": False, "maestro": False}},
        "puts": [],
    }

    def route_workflows(route):
        request = route.request
        if request.method == "GET":
            if state["hold_next"]:
                state["hold_next"] = False
                state["held_route"] = route
                return
            if state["fail_next"]:
                state["fail_next"] = False
                return route.fulfill(status=503, content_type="application/json", body=json.dumps({"detail": "workflow service unavailable"}))
            agent = request.url.split("agent=", 1)[-1]
            rows = [
                {
                    "id": "spec-kit",
                    "name": "Spec Kit",
                    "description": "Specification-first development.",
                    "version": "d848fb4e18f44640ad6b42e60a280551ee90cdce",
                    "upstream_url": "https://github.com/github/spec-kit",
                    "enabled": state["enabled"][agent]["spec-kit"],
                    "commands": ["lectern-spec-kit constitution", "lectern-spec-kit specify"],
                },
                {
                    "id": "maestro",
                    "name": "Maestro",
                    "description": "Curated workflow guidance.",
                    "version": "00f9115d446a8ba26b8f18f6ed306bc4a21807c3",
                    "upstream_url": "https://github.com/sharpdeveye/maestro",
                    "enabled": state["enabled"][agent]["maestro"],
                    "commands": ["lectern-maestro diagnose", "lectern-maestro teach-maestro"],
                },
            ]
            return route.fulfill(status=200, content_type="application/json", body=json.dumps({"workflows": rows, "reload_required": True}))
        if request.method == "PUT":
            workflow_id = request.url.rstrip("/").rsplit("/", 1)[-1]
            body = request.post_data_json
            state["enabled"][body["agent"]][workflow_id] = bool(body["enabled"])
            state["puts"].append((workflow_id, body))
            return route.fulfill(status=200, content_type="application/json", body=json.dumps({"enabled": body["enabled"], "reload_required": True}))
        return route.continue_()

    page.route(f"**/api/projects/{project_id}/workflows**", route_workflows)
    try:
        page.reload()
        _tab(page, "targets")
        page.get_by_role("tab", name="Projects").click()
        page.fill("#pj-search", "Workflow browser fixture")
        page.locator(".pjrow", has_text="Workflow browser fixture").click()

        card = page.locator("#sheet .project-workflows")
        expect(card.locator(".workflows-status")).to_contain_text("2 workflows available", timeout=10000)
        expect(card.locator(".workflow-card").first.locator("code")).to_contain_text("d848fb4e18f44640ad6b42e60a280551ee90cdce")
        expect(card.locator(".workflow-card").first.get_by_role("link", name="Upstream project")).to_have_attribute("href", "https://github.com/github/spec-kit")
        expect(card.get_by_role("button", name="Enable Spec Kit")).to_be_visible()

        card.get_by_role("button", name="Enable Spec Kit").click()
        expect(card.locator(".workflows-status")).to_contain_text("Spec Kit enabled", timeout=10000)
        expect(card.locator(".workflow-card").first.locator(".workflow-commands code")).to_have_count(2)
        expect(card.locator(".workflow-card").first.locator(".workflow-commands code").first).to_have_text("/lectern-spec-kit constitution")
        expect(card.locator(".workflow-reload-note")).to_contain_text("new Claude Code session")
        assert state["puts"][-1][1] == {"agent": "claude", "enabled": True}

        # Provider state is independent and receives its own exact API commands.
        card.locator(".workflows-agent").select_option("codex")
        expect(card.locator(".workflows-status")).to_contain_text("2 workflows available for Codex", timeout=10000)
        expect(card.get_by_role("button", name="Enable Spec Kit")).to_be_visible()
        card.locator(".workflow-card").nth(1).get_by_role("button", name="Enable Maestro").click()
        expect(card.locator(".workflows-status")).to_contain_text("Maestro enabled", timeout=10000)
        expect(card.locator(".workflow-card").nth(1).locator(".workflow-commands code").first).to_have_text("$lectern-maestro diagnose")

        # Disabling hides future command guidance while retaining the workflow card.
        card.locator(".workflow-card").nth(1).get_by_role("button", name="Disable Maestro").click()
        expect(card.locator(".workflow-card").nth(1).locator(".workflow-commands")).to_have_count(0)

        state["fail_next"] = True
        card.locator(".workflows-reload").click()
        expect(card.locator(".workflows-status")).to_have_text("workflow service unavailable", timeout=10000)
        card.locator(".workflows-reload").click()
        expect(card.locator(".workflows-status")).to_contain_text("2 workflows available for Codex", timeout=10000)
        state["hold_next"] = True
        card.locator(".workflows-reload").click()
        expect(card.locator(".workflows-agent")).to_be_disabled()
        expect(card.locator(".workflows-reload")).to_be_disabled()
        held = state["held_route"]
        assert held is not None
        held.fulfill(
            status=200,
            content_type="application/json",
            body=json.dumps({
                "workflows": [
                    {
                        "id": "spec-kit",
                        "name": "Spec Kit",
                        "description": "Specification-first development.",
                        "version": "d848fb4e18f44640ad6b42e60a280551ee90cdce",
                        "upstream_url": "https://github.com/github/spec-kit",
                        "enabled": False,
                        "commands": ["lectern-spec-kit constitution", "lectern-spec-kit specify"],
                    },
                    {
                        "id": "maestro",
                        "name": "Maestro",
                        "description": "Curated workflow guidance.",
                        "version": "00f9115d446a8ba26b8f18f6ed306bc4a21807c3",
                        "upstream_url": "https://github.com/sharpdeveye/maestro",
                        "enabled": False,
                        "commands": ["lectern-maestro diagnose", "lectern-maestro teach-maestro"],
                    },
                ],
                "reload_required": True,
            }),
        )
        expect(card.locator(".workflows-status")).to_contain_text("2 workflows available for Codex", timeout=10000)
        assert state["enabled"]["claude"]["spec-kit"] is True
        assert state["enabled"]["codex"]["spec-kit"] is False
    finally:
        page.request.delete(f"{server}/api/projects/{project_id}")
