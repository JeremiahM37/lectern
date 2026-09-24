"""Review and merge for sessions, end to end: real server, real git, real
tmux, a real browser driving the actual Review panel — the stub agent stands
in for the model (see docs/agent-events.md section 4 and the workspace rules:
no real model tokens in tests).
"""
from session_sheet import open_advanced
import json
import subprocess
import time
import urllib.request
from pathlib import Path

import pytest
from playwright.sync_api import expect

from test_terminal_workspace import real_terminal

# Logs argv/cwd once, then echoes every line typed at it — the same contract
# the Go suite's fakeInteractiveAgent uses, so a multi-line pasted review
# prompt shows up as one "typed:" line per line of the prompt.
STUB_AGENT = '''#!/bin/bash
log="$PWD/session-log.txt"
printf 'argv:%s\\n' "$*" >> "$log"
printf 'cwd:%s\\n' "$PWD" >> "$log"
echo "stub agent ready"
while IFS= read -r line; do
  printf 'typed:%s\\n' "$line" >> "$log"
done
'''


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], text=True).strip()


@pytest.mark.parametrize("real_terminal", [{"agent_script": STUB_AGENT}], indirect=True)
def test_review_send_comments_and_commit_push_to_bare_remote(page, real_terminal, tmp_path):
    t = real_terminal
    root = t["root"]
    git(root, "add", ".")
    git(root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
    base_branch = git(root, "rev-parse", "--abbrev-ref", "HEAD")

    # A local bare remote stands in for GitHub — commit+push must reach it
    # without any network access or a real `gh`.
    bare = tmp_path / "origin.git"
    subprocess.run(["git", "init", "-q", "--bare", str(bare)], check=True)
    git(root, "remote", "add", "origin", str(bare))
    git(root, "push", "-q", "origin", base_branch)

    project = t["api"]("/projects", {
        "name": "review-e2e", "target_id": t["target_id"], "repo_path": str(root),
        "default_base_branch": base_branch,
    })

    # Launch an isolated-worktree session through the real UI, the same flow
    # test_interactive_worktree.py proves — this session gets its own branch,
    # which is what makes the commit/push step legal (see refuseOnBaseBranch).
    page.goto(t["url"] + "/#sessions")
    page.locator("#sess-new").click()
    page.get_by_label("Project", exact=True).select_option(str(project["id"]))
    open_advanced(page)
    page.get_by_label("Name", exact=True).fill("Review e2e session")
    page.get_by_label("Isolate in a new Git worktree", exact=True).check()
    page.locator("#ns-go").click()
    expect(page.locator(".scard", has_text="Review e2e session").get_by_role(
        "button", name="⌨ Attach", exact=True)).to_be_visible(timeout=15000)

    row = next(s for s in t["api"]("/sessions") if s["name"] == "Review e2e session")
    dest = Path(row["workspace"]["path"])
    branch = row["workspace"]["branch"]
    assert dest != root

    # The session's own uncommitted change — what the review is actually about.
    original = (dest / "hello.txt").read_text()
    (dest / "hello.txt").write_text(original + "third line one\nfourth line two\n")

    # Wait for the stub agent to actually be running before opening the pane —
    # the same readiness signal the Go suite waits on.
    deadline = time.monotonic() + 15
    while not (dest / "session-log.txt").exists() and time.monotonic() < deadline:
        time.sleep(0.05)
    assert (dest / "session-log.txt").exists(), "stub agent never started"

    # Open the conversation, then the review-and-merge panel.
    page.locator(".scard", has_text="Review e2e session").get_by_role(
        "button", name="Chat", exact=False).click()
    page.get_by_role("button", name="± Review & merge").click()
    expect(page.locator("#session-review")).to_be_visible()
    expect(page.locator("#session-review")).to_contain_text("third line one", timeout=15000)

    # Click two diff lines, add a distinct comment to each.
    page.locator(".dl-add", has_text="third line one").click()
    page.locator(".dl-composer textarea").fill("please explain this line")
    page.get_by_role("button", name="Add comment").click()
    expect(page.locator(".review-tray")).to_contain_text("1 comment")

    page.locator(".dl-add", has_text="fourth line two").click()
    page.locator(".dl-composer textarea").fill("and this one needs a test")
    page.get_by_role("button", name="Add comment").click()
    expect(page.locator(".review-tray")).to_contain_text("2 comments")

    page.get_by_role("button", name="Send to agent").click()
    expect(page.locator("#toasts")).to_contain_text("Sent 2 comment")

    # The stub agent received the FULL formatted prompt — file:line headers,
    # the quoted code line, and both comments — not just a truncated first line.
    deadline = time.monotonic() + 15
    log = ""
    while time.monotonic() < deadline:
        log = (dest / "session-log.txt").read_text()
        if "please explain this line" in log:
            break
        time.sleep(0.1)
    assert "Code review feedback (2 comments)" in log, log
    assert "hello.txt:3" in log or "hello.txt:4" in log, log
    assert "please explain this line" in log, log
    assert "and this one needs a test" in log, log

    # Commit, push, and land on the bare remote — no GitHub, no `gh`.
    page.get_by_label("Commit message", exact=True).fill("feat: extend hello")
    push_checkbox = page.locator(".review-checkbox", has_text="Push to origin").locator("input")
    expect(push_checkbox).to_be_checked()
    page.get_by_role("button", name="⎇ Commit").click()
    expect(page.locator(".review-commit-steps")).to_contain_text("commit: ok", timeout=15000)
    expect(page.locator(".review-commit-steps")).to_contain_text("push: ok", timeout=15000)

    branches = subprocess.check_output(["git", "-C", str(bare), "branch", "--list", branch], text=True)
    assert branch in branches, branches
    message = subprocess.check_output(
        ["git", "-C", str(bare), "log", branch, "-1", "--format=%s"], text=True).strip()
    assert message == "feat: extend hello", message
