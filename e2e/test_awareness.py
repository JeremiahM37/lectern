"""Cross-agent awareness (docs/agent-events.md "Cross-agent awareness"):
two live sessions in one repository, wired through the REAL hook endpoints
with real per-session hook tokens — the same pattern test_usage_view.py uses
for the statusline hook. repo_key/repo_toplevel are poked directly into
sqlite (mirroring _hook_token's direct read there) rather than resolved via
a real git checkout: the mock target's executor does not understand git, so
this is how every mock-mode e2e test stands in for a resolved repository —
see internal/awareness's own real-git test for the resolution logic itself.

This exercises: PostToolUse recording an edit for session A, PreToolUse on
session B for the SAME file returning an advisory warning naming A, and the
"⚠ overlaps" chip appearing live on both cards once the resulting hook
events refresh the board over SSE — no reload.
"""
import json
import os
import sqlite3
import subprocess
import time
import urllib.request
from pathlib import Path

import pytest
from playwright.sync_api import expect

from conftest import OUTSIDE_WORLD, _binary, _port_open, _unused_port
from test_ui import _tab

ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture()
def awareness_server(tmp_path):
    port = _unused_port()
    db_path = tmp_path / "awareness-e2e.db"
    home = tmp_path / "home"
    home.mkdir()
    tmux_dir = tmp_path / "tmux"
    tmux_dir.mkdir()
    env = {
        **os.environ,
        **OUTSIDE_WORLD,
        "LECTERN_MOCK": "1",
        "LECTERN_TICK": "0.1",
        "LECTERN_MOCK_DELAY": "0.1",
        "LECTERN_PORT": str(port),
        "LECTERN_HANDOFF_POLL": "0.1",
        "LECTERN_DB": str(db_path),
        "LECTERN_BASE_URL": f"http://127.0.0.1:{port}",
        "HOME": str(home),
        "TMUX": "",
        "TMUX_TMPDIR": str(tmux_dir),
    }
    proc = subprocess.Popen(
        [_binary()], cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL
    )
    base = f"http://127.0.0.1:{port}"
    try:
        for _ in range(100):
            if proc.poll() is not None:
                raise RuntimeError(f"server exited with {proc.returncode} before listening")
            if _port_open(port):
                break
            time.sleep(0.1)
        else:
            raise RuntimeError("awareness_server did not start")
        yield base, db_path
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()


def _hook_token(db_path, session_id):
    conn = sqlite3.connect(str(db_path))
    try:
        row = conn.execute("SELECT hook_token FROM sessions WHERE id=?", (session_id,)).fetchone()
    finally:
        conn.close()
    return row[0] if row else None


def _set_repo(db_path, session_id, repo_key, toplevel, workdir):
    """Stand in for internal/awareness's background git resolution: the mock
    target has no real git checkout, so this pokes the same two columns that
    resolution would have written (see internal/awareness/real_test.go for
    the real thing)."""
    conn = sqlite3.connect(str(db_path))
    try:
        conn.execute(
            "UPDATE sessions SET repo_key=?, repo_toplevel=?, workdir=? WHERE id=?",
            (repo_key, toplevel, workdir, session_id),
        )
        conn.commit()
    finally:
        conn.close()


def _post_hook(base, session_id, token, event, tool_name, file_path):
    body = json.dumps(
        {"hook_event_name": event, "tool_name": tool_name, "tool_input": {"file_path": file_path}}
    ).encode()
    req = urllib.request.Request(
        f"{base}/api/hook/session/{session_id}/{event}",
        data=body,
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        assert resp.status == 200
        return json.loads(resp.read())


def _new_session(page, name):
    page.click("#sess-new")
    page.fill("#ns-name", name)
    page.click("#ns-go")
    card = page.locator(".scard", has_text=name)
    expect(card).to_be_visible(timeout=15000)
    return card


def test_edit_warning_and_overlap_chip(browser, awareness_server):
    base, db_path = awareness_server
    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "sessions")

    card_a = _new_session(page, "agent-a")
    card_b = _new_session(page, "agent-b")

    sessions = json.load(urllib.request.urlopen(base + "/api/sessions", timeout=10))
    session_a = next(s for s in sessions if s["name"] == "agent-a")
    session_b = next(s for s in sessions if s["name"] == "agent-b")

    # Two worktrees of ONE repository: same repo_key, different workdirs —
    # the "separate worktree, merge-conflict-risk" wording, not the stronger
    # same-directory one (internal/awareness/peers.go's EditWarning).
    _set_repo(db_path, session_a["id"], "1:/repo/.git", "/repo", "/repo")
    _set_repo(db_path, session_b["id"], "1:/repo/.git", "/repo", "/repo-wt")

    token_a = _hook_token(db_path, session_a["id"])
    token_b = _hook_token(db_path, session_b["id"])
    assert token_a and token_b and token_a != token_b

    # A finishes editing shared.go.
    _post_hook(base, session_a["id"], token_a, "PostToolUse", "Edit", "/repo/shared.go")

    # B is about to edit the SAME file (PreToolUse) — real hook wire call,
    # asserting the actual advisory text an agent would see injected into
    # its context via hookSpecificOutput.additionalContext.
    reply = _post_hook(base, session_b["id"], token_b, "PreToolUse", "Edit", "/repo/shared.go")
    ctx = reply.get("hookSpecificOutput", {}).get("additionalContext", "")
    assert "agent-a" in ctx
    assert "shared.go" in ctx
    assert "separate worktree" in ctx

    # Live, no reload: both hook calls changed agent_state, which publishes
    # a "session" bus event App.tsx refreshes on — the overlap chip is
    # server-computed fresh on every /api/sessions fetch.
    chip_b = card_b.locator(".awareness-overlap-chip")
    expect(chip_b).to_be_visible(timeout=10000)
    expect(chip_b).to_contain_text(f"#{session_a['id']}")

    chip_a = card_a.locator(".awareness-overlap-chip")
    expect(chip_a).to_be_visible(timeout=10000)
    expect(chip_a).to_contain_text(f"#{session_b['id']}")

    # Tap to expand: shared file and peer name are already in hand, no
    # extra request.
    chip_b.click()
    detail = card_b.locator(".awareness-overlap-detail")
    expect(detail).to_be_visible()
    expect(detail).to_contain_text("shared.go")
    expect(detail).to_contain_text("agent-a")

    context.close()


def test_briefing_context_on_session_start(browser, awareness_server):
    """SessionStart/UserPromptSubmit get a peer briefing when another live
    session shares the repository — the other half of the hook contract
    (edit-time warnings are covered above)."""
    base, db_path = awareness_server
    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "sessions")

    card_a = _new_session(page, "writer-a")
    card_b = _new_session(page, "writer-b")

    sessions = json.load(urllib.request.urlopen(base + "/api/sessions", timeout=10))
    session_a = next(s for s in sessions if s["name"] == "writer-a")
    session_b = next(s for s in sessions if s["name"] == "writer-b")
    _set_repo(db_path, session_a["id"], "2:/repo2/.git", "/repo2", "/repo2")
    _set_repo(db_path, session_b["id"], "2:/repo2/.git", "/repo2", "/repo2")

    token_a = _hook_token(db_path, session_a["id"])
    token_b = _hook_token(db_path, session_b["id"])
    _post_hook(base, session_a["id"], token_a, "PostToolUse", "Write", "/repo2/feature.py")

    req = urllib.request.Request(
        f"{base}/api/hook/session/{session_b['id']}/SessionStart",
        data=b"{}",
        headers={"Authorization": f"Bearer {token_b}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        reply = json.loads(resp.read())
    ctx = reply.get("hookSpecificOutput", {}).get("additionalContext", "")
    assert "writer-a" in ctx
    assert "feature.py" in ctx
    assert "Coordinate before duplicating their work" in ctx

    context.close()
