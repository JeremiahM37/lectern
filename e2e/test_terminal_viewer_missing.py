"""Attach with no terminal viewer (ttyd) on the server says so, names the
command that installs it, and offers to copy it — instead of retrying in
silence. The server's answer is the real one's shape (internal/api/
terminal_errors.go); it is routed here because the fixture host has ttyd."""
import json
import urllib.request

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE
from test_light_mode_sweep import Sweep, light

ANSWER = {"detail": "Terminal viewer isn't installed — run: sudo apt-get install -y ttyd",
          "code": "terminal_viewer_missing", "fix": "sudo apt-get install -y ttyd"}


def _session(base):
    project = json.load(urllib.request.urlopen(base + "/api/projects", timeout=10))[0]
    req = urllib.request.Request(base + "/api/sessions", method="POST",
                                 data=json.dumps({"project_id": project["id"], "name": "viewer check", "agent": "claude"}).encode(),
                                 headers={"Content-Type": "application/json"})
    return json.load(urllib.request.urlopen(req, timeout=10))


@pytest.mark.parametrize("theme", ["light", "dark"])
@pytest.mark.parametrize("page", [DESKTOP, PHONE], indirect=True, ids=["desk", "phone"])
def test_attach_without_terminal_viewer_names_the_fix(page, server, theme):
    session = _session(server)
    calls = []

    def missing(route):
        calls.append(route.request.url)
        route.fulfill(status=503, content_type="application/json", body=json.dumps(ANSWER))

    page.route(f"**/api/sessions/{session['id']}/terminal", missing)
    page.context.grant_permissions(["clipboard-read", "clipboard-write"])
    light(page, theme)
    page.goto(server + "/#sessions")
    card = page.locator(".scard", has_text="viewer check").first
    card.get_by_role("button", name="⌨ Terminal", exact=True).click()
    toast = page.locator(".toast", has_text="Terminal viewer isn't installed")
    expect(toast).to_contain_text("run sudo apt-get install -y ttyd", timeout=10000)
    # Not a transient error: asked once, not retried for five seconds.
    assert len(calls) == 1, calls
    label = "phone" if page.viewport_size["width"] < 600 else "desk"
    sweep = Sweep(page, label + "-viewer-missing", theme)
    sweep.check("toast", root=".toast")
    sweep.done()
    toast.get_by_role("button", name="Copy command").click()
    assert page.evaluate("navigator.clipboard.readText()") == "sudo apt-get install -y ttyd"
