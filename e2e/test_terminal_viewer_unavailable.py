"""Attach when the web terminal server cannot start says so once, with what
helps, instead of retrying in silence. The server's answer is the real one's
shape (internal/api/terminal_errors.go), routed in because a healthy fixture
server always starts its term-server."""
import json
import urllib.request

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE
from test_light_mode_sweep import Sweep, light

ANSWER = {"detail": "The web terminal could not start (exec failed)",
          "code": "terminal_viewer_unavailable", "reason": "exec failed"}


def _session(base):
    project = json.load(urllib.request.urlopen(base + "/api/projects", timeout=10))[0]
    req = urllib.request.Request(base + "/api/sessions", method="POST",
                                 data=json.dumps({"project_id": project["id"], "name": "viewer check", "agent": "claude"}).encode(),
                                 headers={"Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req, timeout=10))


@pytest.mark.parametrize("theme", ["light", "dark"])
@pytest.mark.parametrize("page", [DESKTOP, PHONE], indirect=True, ids=["desk", "phone"])
def test_attach_when_terminal_server_cannot_start(page, server, theme):
    session = _session(server)
    calls = []

    def unavailable(route):
        calls.append(route.request.url)
        route.fulfill(status=503, content_type="application/json", body=json.dumps(ANSWER))

    page.route(f"**/api/sessions/{session['id']}/terminal", unavailable)
    light(page, theme)
    page.goto(server + "/#sessions")
    card = page.locator(".scard", has_text="viewer check").first
    card.get_by_role("button", name="⌨ Attach", exact=True).click()
    toast = page.locator(".toast", has_text="The web terminal could not start")
    expect(toast).to_contain_text("lectern local stop, then lectern up", timeout=10000)
    # Not a transient error: asked once, not retried for five seconds.
    assert len(calls) == 1, calls
    label = "phone" if page.viewport_size["width"] < 600 else "desk"
    sweep = Sweep(page, label + "-viewer-unavailable", theme)
    sweep.check("toast", root=".toast")
    sweep.done()
