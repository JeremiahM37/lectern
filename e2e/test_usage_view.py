"""A real statusline hook POST updates a session card and the account quota
chip live, over SSE, with no reload — see docs/agent-events.md section 2 and
internal/agentevents.IngestStatusline. The statusline body below is a
trimmed-down version of the real Claude Code 2.1.281 capture in
internal/agentevents/ingest_test.go's fixtureStatusline (itself copied
byte-for-byte from /mnt/bulk/lectern-events-ref/claude-statusline.json) —
every field lectern's parser actually reads is real, unused fields are
dropped, and lines_added/lines_removed are changed from the capture's 0/0 so
this test can also exercise the line-delta chip. Inlined so this test does
not depend on a path outside the repository, same as that Go test.
"""
import json
import os
import sqlite3
import subprocess
import tempfile
import time
import urllib.request
from pathlib import Path

import pytest
from playwright.sync_api import expect

from conftest import OUTSIDE_WORLD, _binary, _port_open, _unused_port
from session_sheet import open_advanced
from test_ui import _tab

ROOT = Path(__file__).resolve().parents[1]

STATUSLINE_FIXTURE = (
    b'{"session_id":"65f4714e-3848-434d-b684-9068108338d1",'
    b'"model":{"id":"claude-opus-5-5","display_name":"Opus 5.5"},'
    b'"cost":{"total_cost_usd":0.11431480000000001,"total_lines_added":3,"total_lines_removed":1},'
    b'"context_window":{"total_input_tokens":36451,"total_output_tokens":4,'
    b'"context_window_size":1000000,"used_percentage":4},'
    b'"rate_limits":{"five_hour":{"used_percentage":4,"resets_at":1790200800},'
    b'"seven_day":{"used_percentage":72,"resets_at":1790290800}}}'
)


@pytest.fixture()
def usage_server(tmp_path):
    """A private mock-mode server with a known sqlite path, so the test can
    read a session's hook_token the same way the real target process would
    receive it (LECTERN_HOOK_TOKEN) — the general API never serializes it
    (store.Session.HookToken is `json:"-"`, by design)."""
    port = _unused_port()
    db_path = tmp_path / "usage-e2e.db"
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
            raise RuntimeError("usage_server did not start")
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


def test_statusline_hook_updates_card_and_quota_chip_live(browser, usage_server):
    base, db_path = usage_server
    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "sessions")
    page.click("#sess-new")
    open_advanced(page)
    page.fill("#ns-name", "usage card")
    page.click("#ns-go")

    card = page.locator(".scard", has_text="usage card")
    expect(card).to_be_visible(timeout=15000)
    # Before any statusline, no usage badge has anything to show.
    assert card.locator(".ctx-used").count() == 0

    sessions = json.load(urllib.request.urlopen(base + "/api/sessions", timeout=10))
    session = next(s for s in sessions if s["name"] == "usage card")
    token = _hook_token(db_path, session["id"])
    assert token, "a launched session must have a hook_token"

    req = urllib.request.Request(
        f"{base}/api/hook/session/{session['id']}/statusline",
        data=STATUSLINE_FIXTURE,
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        assert resp.status == 200

    # Live over SSE — no reload here. Card shows model, context bar
    # (amber/red thresholds — 4% is neither), cost and the line delta.
    expect(card.locator(".chip", has_text="claude-opus-5-5")).to_be_visible(timeout=10000)
    ctx = card.locator(".ctx-used")
    expect(ctx).to_be_visible(timeout=10000)
    expect(ctx).to_contain_text("4%")
    assert "ctx-amber" not in (ctx.get_attribute("class") or "")
    assert "ctx-red" not in (ctx.get_attribute("class") or "")
    expect(card.locator(".chip.cost")).to_contain_text("$0.11", timeout=10000)
    expect(card.locator(".chip.ds")).to_contain_text("+3", timeout=10000)

    # The account-wide quota chip appears on the Sessions header once a
    # statusline has reported rate_limits (it renders nothing beforehand —
    # see QuotaChip.tsx).
    chip = page.locator(".quota-chip")
    expect(chip).to_be_visible(timeout=10000)
    expect(chip).to_contain_text("5h 4%")
    expect(chip).to_contain_text("7d 72%")
    context.close()


def test_high_context_used_pct_shows_the_compaction_warning(browser, usage_server):
    """The card's compaction warning appears once context_used_pct crosses
    85% (task contract threshold), independent of the amber/red bar color
    tests above."""
    base, db_path = usage_server
    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "sessions")
    page.click("#sess-new")
    open_advanced(page)
    page.fill("#ns-name", "nearly full")
    page.click("#ns-go")
    card = page.locator(".scard", has_text="nearly full")
    expect(card).to_be_visible(timeout=15000)

    sessions = json.load(urllib.request.urlopen(base + "/api/sessions", timeout=10))
    session = next(s for s in sessions if s["name"] == "nearly full")
    token = _hook_token(db_path, session["id"])
    fixture = STATUSLINE_FIXTURE.replace(b'"used_percentage":4}', b'"used_percentage":90}', 1)
    req = urllib.request.Request(
        f"{base}/api/hook/session/{session['id']}/statusline",
        data=fixture,
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
        method="POST",
    )
    urllib.request.urlopen(req, timeout=10)

    ctx = card.locator(".ctx-used")
    expect(ctx).to_contain_text("90%", timeout=10000)
    assert "ctx-red" in (ctx.get_attribute("class") or "")
    expect(card.locator(".compaction-warning")).to_be_visible(timeout=10000)
    context.close()
