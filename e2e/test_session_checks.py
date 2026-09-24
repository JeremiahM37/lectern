"""Session checks (internal/checks, docs/agent-events.md section 4): a real
server, a real git worktree and a real browser driving the session card's
check badge and "Run check" button — no simulated executor.
"""
import json
import subprocess
import urllib.request

import pytest
from playwright.sync_api import expect

from test_terminal_workspace import real_terminal


def patch(t, path, body):
    request = urllib.request.Request(
        t["url"] + path, method="PATCH",
        headers={"Content-Type": "application/json"}, data=json.dumps(body).encode())
    return json.load(urllib.request.urlopen(request))


@pytest.mark.parametrize("width", [390, 1440])
def test_session_card_check_goes_failed_then_passed(page, real_terminal, width):
    t = real_terminal
    root = t["root"]
    # An unborn branch would still fingerprint fine, but a real commit is what
    # a real project looks like — and it's what makes `git status --porcelain`
    # meaningfully change once ok.txt is added below.
    subprocess.run(["git", "-C", str(root), "add", "."], check=True)
    subprocess.run(
        ["git", "-C", str(root), "-c", "user.name=Test", "-c", "user.email=test@example.com",
         "commit", "-qm", "base"],
        check=True,
    )

    project = t["api"]("/projects", {
        "name": "checks-e2e", "target_id": t["target_id"], "repo_path": str(root),
        "verify_cmd": "test -f ok.txt",
    })
    patch(t, f"/api/sessions/{t['id']}", {"project_id": project["id"]})

    page.set_viewport_size({"width": width, "height": 900})
    page.goto(t["url"] + "/#sessions")
    card = page.locator(".scard", has_text="Real terminal")
    expect(card).to_be_visible(timeout=15000)

    # Before ok.txt exists: run now, and the badge shows failed.
    card.get_by_role("button", name="Run check").click()
    expect(card.get_by_text("check failed")).to_be_visible(timeout=15000)

    # Tap-to-expand shows the command and the captured output.
    card.get_by_text("check failed").click()
    expect(card.locator(".check-detail code")).to_have_text("test -f ok.txt")

    # The file appears; a second run-now now passes.
    (root / "ok.txt").write_text("done\n")
    card.get_by_role("button", name="Run check").click()
    expect(card.get_by_text("check passed")).to_be_visible(timeout=15000)

    # The check history is on the review-and-merge panel too.
    card.locator(".action-menu>summary").click()
    page.get_by_role("button", name="Review & merge").click()
    expect(page.get_by_role("heading", name="Checks")).to_be_visible(timeout=10000)
    expect(page.locator(".review-check-list li").first).to_contain_text("test -f ok.txt")
