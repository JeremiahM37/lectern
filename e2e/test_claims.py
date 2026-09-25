"""Claim board (docs/claims.md): two live sessions in one repository, wired
through the REAL hook endpoints and the REAL /api/claims REST surface — same
pattern test_awareness.py uses for its peer-briefing/edit-warning coverage,
since claims rides the exact same hook-response and mock-repo-key machinery.

Exercises: one session claims a directory via POST /api/claims (standing in
for the claim_work MCP tool, which is a thin wrapper around this same
endpoint — see internal/mcp/tools_test.go for MCP-level coverage), the
other's real PreToolUse hook call on a file under that claim returns the
advisory warning naming it, and the Claims panel in the UI shows the claim
live with no reload.
"""
import json
import os
import sqlite3
import subprocess
import time
import urllib.error
import urllib.request
from pathlib import Path

import pytest
from playwright.sync_api import expect

from conftest import OUTSIDE_WORLD, _binary, _port_open, _unused_port
from session_sheet import open_advanced
from test_ui import _tab

ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture()
def claims_server(tmp_path):
    port = _unused_port()
    db_path = tmp_path / "claims-e2e.db"
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
            raise RuntimeError("claims_server did not start")
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
    """Stand in for internal/awareness's background git resolution — see
    test_awareness.py's identical helper for why: the mock target has no
    real git checkout for a claim's synchronous resolve to shell out to."""
    conn = sqlite3.connect(str(db_path))
    try:
        conn.execute(
            "UPDATE sessions SET repo_key=?, repo_toplevel=?, workdir=? WHERE id=?",
            (repo_key, toplevel, workdir, session_id),
        )
        conn.commit()
    finally:
        conn.close()


def _post_json(url, body, token=None, method="POST"):
    headers = {"Content-Type": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    req = urllib.request.Request(
        url, data=json.dumps(body).encode(), headers=headers, method=method
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        assert resp.status == 200
        return json.loads(resp.read())


def _delete(url):
    req = urllib.request.Request(url, method="DELETE")
    with urllib.request.urlopen(req, timeout=10) as resp:
        assert resp.status == 200


def _new_session(page, name):
    page.click("#sess-new")
    open_advanced(page)
    page.fill("#ns-name", name)
    page.click("#ns-go")
    card = page.locator(".scard", has_text=name)
    expect(card).to_be_visible(timeout=15000)
    return card


def test_claim_pretooluse_warning_and_panel_show_it(browser, claims_server):
    base, db_path = claims_server
    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "sessions")

    card_a = _new_session(page, "codex-claimant")
    card_b = _new_session(page, "other-editor")

    sessions = json.load(urllib.request.urlopen(base + "/api/sessions", timeout=10))
    session_a = next(s for s in sessions if s["name"] == "codex-claimant")
    session_b = next(s for s in sessions if s["name"] == "other-editor")

    # Same repository, two worktrees — the same real-repo shape
    # test_awareness.py exercises.
    _set_repo(db_path, session_a["id"], "1:/repo/.git", "/repo", "/repo")
    _set_repo(db_path, session_b["id"], "1:/repo/.git", "/repo-wt", "/repo-wt")

    # A claims a directory via the real REST endpoint — the same endpoint
    # the claim_work MCP tool calls (internal/mcp/tools_test.go covers that
    # tool layer directly against a real server; this proves the endpoint
    # itself and its hook-side effects end to end).
    claim = _post_json(
        f"{base}/api/claims",
        {
            "session_id": session_a["id"],
            "scope_kind": "paths",
            "paths": ["frontend/src/sessions/**"],
            "intent": "rename button",
        },
    )
    assert claim["repo_key"] == "1:/repo/.git"

    # B is about to edit a file under that claim — real PreToolUse hook call,
    # asserting the actual advisory text injected via
    # hookSpecificOutput.additionalContext.
    token_b = _hook_token(db_path, session_b["id"])
    req = urllib.request.Request(
        f"{base}/api/hook/session/{session_b['id']}/PreToolUse",
        data=json.dumps(
            {
                "hook_event_name": "PreToolUse",
                "tool_name": "Edit",
                "tool_input": {"file_path": "/repo-wt/frontend/src/sessions/SessionCard.tsx"},
            }
        ).encode(),
        headers={"Authorization": f"Bearer {token_b}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        reply = json.loads(resp.read())
    ctx = reply.get("hookSpecificOutput", {}).get("additionalContext", "")
    assert f"session #{session_a['id']}" in ctx
    assert "codex" in ctx
    assert "frontend/src/sessions/**" in ctx
    assert "rename button" in ctx

    # The Claims panel shows it live, no reload.
    _tab(page, "board")
    page.click("#qb-claims")
    panel = page.locator(".claims-panel")
    expect(panel).to_be_visible(timeout=10000)
    row = panel.locator(".claims-row", has_text="rename button")
    expect(row).to_be_visible(timeout=10000)
    expect(row).to_contain_text(f"#{session_a['id']}")
    expect(row).to_contain_text("frontend/src/sessions/**")

    # Release it from the panel — gone from the list.
    row.get_by_role("button", name="Release").click()
    expect(panel.locator(".claims-row", has_text="rename button")).to_have_count(0, timeout=10000)

    context.close()


def test_claims_briefing_on_session_start(browser, claims_server):
    base, db_path = claims_server
    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "sessions")

    card_a = _new_session(page, "topic-claimant")
    card_b = _new_session(page, "topic-newcomer")

    sessions = json.load(urllib.request.urlopen(base + "/api/sessions", timeout=10))
    session_a = next(s for s in sessions if s["name"] == "topic-claimant")
    session_b = next(s for s in sessions if s["name"] == "topic-newcomer")
    _set_repo(db_path, session_a["id"], "2:/repo2/.git", "/repo2", "/repo2")
    _set_repo(db_path, session_b["id"], "2:/repo2/.git", "/repo2", "/repo2")

    _post_json(
        f"{base}/api/claims",
        {
            "session_id": session_a["id"],
            "scope_kind": "topic",
            "scope": "rename button in session card",
            "intent": "rename button in session card",
        },
    )

    token_b = _hook_token(db_path, session_b["id"])
    req = urllib.request.Request(
        f"{base}/api/hook/session/{session_b['id']}/SessionStart",
        data=b"{}",
        headers={"Authorization": f"Bearer {token_b}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        reply = json.loads(resp.read())
    ctx = reply.get("hookSpecificOutput", {}).get("additionalContext", "")
    assert f"session #{session_a['id']}" in ctx
    assert "rename button" in ctx
    assert "Coordinate" in ctx

    context.close()
