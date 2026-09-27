"""The review workspace end to end (docs/review.md): real server, real git,
real tmux and a stub agent standing in for the model. Covers comment batching
(one prompt per round, re-raised comments), the side-by-side layout and its
per-device preference, three-way conflict resolution writing the file, hunk
staging, and file-by-file review on a phone.
"""
import subprocess
import time
from pathlib import Path

import pytest
from playwright.sync_api import expect

from test_terminal_workspace import real_terminal

# Echoes every line typed at it into a log next to itself, like the Go suite's
# fakeInteractiveAgent.
STUB_AGENT = '''#!/bin/bash
log="$PWD/../agent-log.txt"
echo "stub agent ready"
while IFS= read -r line; do
  printf 'typed:%s\\n' "$line" >> "$log"
done
'''


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], text=True).strip()


def start_session(t, name):
    root = t["root"]
    for key, value in (("user.name", "Test"), ("user.email", "test@example.com")):
        git(root, "config", key, value)
    (root / "app.py").write_text("def main():\n    print('hello')\n    return 0\n\n\ndef health():\n    return True\n")
    git(root, "add", ".")
    git(root, "commit", "-qm", "base")
    base = git(root, "rev-parse", "--abbrev-ref", "HEAD")
    project = t["api"]("/projects", {"name": "review-ws", "target_id": t["target_id"],
                                     "repo_path": str(root), "default_base_branch": base})
    sess = t["api"]("/sessions", {"project_id": project["id"], "agent": "claude", "name": name,
                                  "worktree": {}, "yolo": False})
    wt = Path(sess["workspace"]["path"])
    for key, value in (("user.name", "Test"), ("user.email", "test@example.com")):
        git(wt, "config", key, value)
    return sess, wt, base


def open_review(page, t, name):
    page.goto(t["url"] + "/#sessions")
    card = page.locator(".scard", has_text=name)
    expect(card).to_be_visible(timeout=15000)
    card.locator(".action-menu>summary").click()
    page.get_by_role("button", name="Review & merge").click()
    review = page.locator("#session-review")
    expect(review).to_be_visible()
    return review


def agent_log(wt, want, limit=15):
    path = wt.parent / "agent-log.txt"
    deadline = time.monotonic() + limit
    text = ""
    while time.monotonic() < deadline:
        text = path.read_text() if path.exists() else ""
        if want in text:
            return text
        time.sleep(0.1)
    raise AssertionError(f"{want!r} never reached the agent:\n{text}")


def comment(page, locator, text):
    locator.click()
    page.locator(".dl-composer textarea").fill(text)
    page.get_by_role("button", name="Add comment").click()
    expect(page.locator(".dl-note", has_text=text)).to_be_visible()


@pytest.mark.parametrize("real_terminal", [{"agent_script": STUB_AGENT}], indirect=True)
def test_comments_go_to_the_agent_as_one_prompt_and_follow_its_edits(page, real_terminal):
    t = real_terminal
    sess, wt, _ = start_session(t, "Batch review")
    (wt / "app.py").write_text("def main():\n    print('hello, review')\n    return 0\n\n\ndef health():\n    return 'ok'\n")
    (wt / "notes.md").write_text("first note\nsecond note\n")
    review = open_review(page, t, "Batch review")
    expect(review).to_contain_text("hello, review", timeout=15000)

    comment(page, review.locator(".dl-add", has_text="hello, review"), "say hello to the user by name")
    comment(page, review.locator(".dl-add", has_text="return 'ok'"), "keep returning a bool")
    comment(page, review.locator(".dl-add", has_text="second note"), "drop this line")
    expect(review.locator(".review-tray")).to_contain_text("3 comments")
    review.get_by_role("button", name="Send 3 comments to agent").click()
    expect(page.locator("#toasts")).to_contain_text("as one message (round 1)")

    log = agent_log(wt, "drop this line")
    # One prompt, holding every comment with its file:line and quoted code.
    assert log.count("Code review feedback") == 1, log
    assert "Code review feedback (3 comments)" in log, log
    for want in ("app.py:2", "app.py:7", "notes.md:2", "> print('hello, review')",
                 "say hello to the user by name", "keep returning a bool", "drop this line"):
        assert want in log, (want, log)

    # The agent edits two of the three places; one of them also moves down.
    (wt / "app.py").write_text("import os\n\n\ndef main():\n    print('hello, ' + os.getlogin())\n    return 0\n\n\ndef health():\n    return 'ok'\n")
    (wt / "notes.md").write_text("first note\n")
    review.get_by_role("button", name="Close").click()
    review = open_review(page, t, "Batch review")
    rounds = review.locator(".review-rounds")
    expect(rounds).to_contain_text("2 changed by the agent", timeout=15000)
    expect(rounds).to_contain_text("1 not changed")
    # The unchanged comment followed its line from 7 to 10.
    unchanged = rounds.locator(".review-round-item[data-state='open']")
    expect(unchanged).to_contain_text("app.py:10")
    expect(review.locator(".dl-note", has_text="keep returning a bool")).to_be_visible()

    # Resolve what the agent changed; send the rest again as round 2.
    rounds.get_by_role("button", name="Resolve 2 the agent changed").click()
    expect(rounds).to_contain_text("2 resolved")
    rounds.get_by_role("button", name="Reopen 1 unchanged").click()
    expect(review.locator(".review-tray")).to_contain_text("1 comment")
    review.get_by_role("button", name="Send to agent").click()
    expect(page.locator("#toasts")).to_contain_text("round 2")
    log = agent_log(wt, "raised again")
    assert "app.py:10 (new side) (raised again; first sent in round 1)" in log, log
    assert log.count("Code review feedback") == 2, log


@pytest.mark.parametrize("real_terminal", [{"agent_script": STUB_AGENT}], indirect=True)
@pytest.mark.parametrize("width", [1280, 390])
def test_side_by_side_toggle_renders_the_same_hunk_and_persists(page, real_terminal, width):
    t = real_terminal
    page.set_viewport_size({"width": width, "height": 860})
    sess, wt, _ = start_session(t, "Layout review")
    (wt / "app.py").write_text("def main():\n    print('side by side')\n    return 0\n\n\ndef health():\n    return True\n")
    review = open_review(page, t, "Layout review")
    expect(review.locator(".dl-add", has_text="side by side")).to_be_visible(timeout=15000)
    unified = {t[1:].strip() for t in review.locator(".dfile", has_text="app.py")
               .locator(".dl-row:not(.dl-hunk):not(.dl-meta) .dl-text").all_inner_texts()}

    review.get_by_role("button", name="Side by side").click()
    rows = review.locator(".dl-split")
    expect(rows.first).to_be_visible()
    changed = review.locator(".dl-split", has=page.locator(".dl-half.dl-add"))
    expect(changed).to_have_count(1)
    expect(changed.locator(".dl-half[data-side='old']")).to_contain_text("print('hello')")
    expect(changed.locator(".dl-half[data-side='new']")).to_contain_text("print('side by side')")
    # Every line of the unified hunk is on one side or the other.
    halves = {t.strip() for t in review.locator(".dl-half .dl-text").all_inner_texts()}
    assert unified - {""} <= halves, (unified, halves)
    # A comment works from a half, too.
    changed.locator(".dl-half[data-side='new']").click()
    page.locator(".dl-composer textarea").fill("split comment")
    page.get_by_role("button", name="Add comment").click()
    expect(review.locator(".dl-note", has_text="split comment")).to_be_visible()
    assert review.evaluate("(el) => el.scrollWidth <= el.clientWidth + 1")

    # The layout is this device's preference: it survives closing and a reload.
    review.get_by_role("button", name="Close").click()
    page.reload()
    review = open_review(page, t, "Layout review")
    expect(review.get_by_role("button", name="Side by side")).to_have_attribute("aria-pressed", "true")
    expect(review.locator(".dl-split").first).to_be_visible(timeout=15000)
    review.get_by_role("button", name="Unified").click()
    expect(review.locator(".dl-split")).to_have_count(0)
    assert page.evaluate("localStorage.getItem('lec-diffmode')") == "unified"


@pytest.mark.parametrize("real_terminal", [{"agent_script": STUB_AGENT}], indirect=True)
def test_merge_conflict_resolution_writes_the_file_and_finishes_the_merge(page, real_terminal):
    t = real_terminal
    sess, wt, base = start_session(t, "Conflict review")
    root = t["root"]
    (wt / "app.py").write_text("def main():\n    print('from the session')\n    return 0\n\n\ndef health():\n    return True\n")
    git(wt, "commit", "-qam", "session change")
    (root / "app.py").write_text("def main():\n    print('from main')\n    return 0\n\n\ndef health():\n    return True\n")
    git(root, "commit", "-qam", "main change")
    subprocess.run(["git", "-C", str(wt), "merge", base], capture_output=True)
    assert "UU app.py" in git(wt, "status", "--porcelain")

    review = open_review(page, t, "Conflict review")
    review.get_by_role("tab", name="Conflicts (1)").click()
    region = review.locator(".conflict-region")
    expect(region).to_contain_text("print('from the session')", timeout=15000)
    expect(region.locator(".conflict-base")).to_contain_text("print('hello')")
    expect(region.locator(".conflict-theirs")).to_contain_text("print('from main')")

    region.get_by_role("button", name="Accept both").click()
    result = review.locator("textarea.conflict-result")
    expect(result).to_have_value("def main():\n    print('from the session')\n    print('from main')\n    return 0\n\n\ndef health():\n    return True\n")
    # A manual edit on top of the choice is what gets written.
    result.fill("def main():\n    print('from both')\n    return 0\n\n\ndef health():\n    return True\n")
    review.get_by_role("button", name="Mark resolved").click()
    expect(page.locator("#toasts")).to_contain_text("Resolved app.py")
    assert (wt / "app.py").read_text() == "def main():\n    print('from both')\n    return 0\n\n\ndef health():\n    return True\n"
    assert git(wt, "diff", "--name-only", "--diff-filter=U") == ""

    review.get_by_role("tab", name="Commit").click()
    expect(review.locator(".git-operation")).to_contain_text("All conflicts are resolved")
    expect(review.get_by_label("Commit message", exact=True)).to_have_value(f"Merge branch '{base}' into {sess['workspace']['branch']}")
    review.locator(".review-checkbox", has_text="Push to origin").locator("input").uncheck()
    review.get_by_role("button", name="⎇ Commit").click()
    expect(review.locator(".review-commit-steps")).to_contain_text("commit: ok", timeout=15000)
    assert len(git(wt, "log", "-1", "--format=%P").split()) == 2


@pytest.mark.parametrize("real_terminal", [{"agent_script": STUB_AGENT}], indirect=True)
def test_hunk_staging_and_file_by_file_review_on_a_phone(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({"width": 390, "height": 844})
    sess, wt, _ = start_session(t, "Phone review")
    lines = [f"line {i}" for i in range(1, 31)]
    (wt / "long.txt").write_text("\n".join(lines) + "\n")
    git(wt, "add", "long.txt")
    git(wt, "commit", "-qm", "long")
    lines[1], lines[27] = "EARLY", "LATE"
    (wt / "long.txt").write_text("\n".join(lines) + "\n")
    (wt / "app.py").write_text("def main():\n    print('phone')\n    return 0\n\n\ndef health():\n    return True\n")

    review = open_review(page, t, "Phone review")
    review.get_by_role("tab", name="Commit").click()
    file = review.locator(".git-file", has_text="long.txt")
    file.locator(".git-file-name").click()
    hunks = file.locator(".dl-hunk")
    expect(hunks).to_have_count(2)
    hunks.nth(1).get_by_role("button", name="Stage hunk").click()
    expect(review.locator(".git-section", has_text="Staged (1)")).to_be_visible(timeout=15000)
    staged = git(wt, "diff", "--cached")
    assert "+LATE" in staged and "EARLY" not in staged, staged
    # Discard asks first, inline.
    unstaged = review.locator(".git-section", has_text="Changes (").locator(".git-file", has_text="app.py")
    unstaged.get_by_role("button", name="Discard").click()
    unstaged.get_by_role("button", name="Cancel").click()
    assert "phone" in (wt / "app.py").read_text()

    review.get_by_role("tab", name="Changes").click()
    # The toolbar's hunk buttons scroll the panel so a hunk header lands just
    # under the pinned sheet header.
    expect(review.locator(".review-pane .dl-hunk").first).to_be_attached(timeout=15000)
    review.get_by_role("button", name="Next hunk", exact=True).click()
    assert review.evaluate("""(d) => {
        const head = d.querySelector('.sheet-head').getBoundingClientRect().bottom;
        return [...d.querySelectorAll('.review-pane .dl-hunk')]
          .some((e) => Math.abs(e.getBoundingClientRect().top - head) < 24) && d.scrollTop > 0;
    }""")
    review.get_by_role("button", name="Review file by file").click()
    focus = page.get_by_role("region", name="Review one file at a time")
    expect(focus).to_be_visible()
    expect(focus.locator(".focus-count")).to_contain_text("File 1 of 2")
    first = focus.locator(".focus-path").inner_text()
    focus.get_by_role("button", name="Mark viewed").click()
    expect(focus.locator(".focus-count")).to_contain_text("File 2 of 2 · 1 viewed")
    assert focus.locator(".focus-path").inner_text() != first
    # Next hunk walks this file, then wraps back to the other one.
    focus.get_by_role("button", name="Next hunk ▼").click()
    focus.get_by_role("button", name="Next hunk ▼").click()
    focus.get_by_role("button", name="Next hunk ▼").click()
    # Comment from the phone: tap a line.
    focus.locator(".dl-add").first.click()
    page.locator(".dl-composer textarea").fill("phone comment")
    page.get_by_role("button", name="Add comment").click()
    expect(focus.get_by_role("button", name="Send 1 💬")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth <= window.innerWidth + 1")
    focus.get_by_role("button", name="Leave file-by-file review").click()
    expect(review.locator(".ftree-check, .dfile-viewed")).not_to_have_count(0)
