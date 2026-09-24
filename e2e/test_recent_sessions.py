"""Recently closed sessions through the real browser and a fixture-only API."""
import sqlite3
import time

import pytest
from playwright.sync_api import expect

from test_native_history import prepare
from test_native_resume import stopped, argv
from test_session_restore import request
from test_terminal_workspace import real_terminal


def open_recent(page, t, width=1440):
    page.set_viewport_size({"width": width, "height": 844})
    page.goto(t["url"] + "/#sessions")
    expect(page.locator("#sess-recent")).to_be_visible(timeout=15000)
    page.locator("#sess-recent").click()
    expect(page.locator(".recent-closed")).to_be_visible(timeout=10000)


def seed_closed_rows(t, count=9):
    now = time.time()
    with sqlite3.connect(t["env"]["LECTERN_DB"]) as db:
        for i in range(count):
            ended = now - (i + 2) * 60
            db.execute(
                """INSERT INTO sessions
                   (target_id,name,agent,workdir,tmux_session,status,origin,
                    created_at,updated_at,ended_at)
                   VALUES (?,?,?,?,?,?,?,?,?,?)""",
                (
                    t["target_id"],
                    f"Closed fixture {i + 1}",
                    "codex",
                    str(t["root"]),
                    f"closed-fixture-{i + 1}",
                    "dead",
                    "lectern",
                    ended - 60,
                    ended,
                    ended,
                ),
            )
        db.commit()


def mark_as_durable_dead(t):
    """Make the fixture represent a closed Lectern-owned terminal.

    DELETE preserves a discovered terminal as a released row so the safe
    action is Restore tracking.  The Resume/Choose history branches need the
    separate durable dead state.
    """
    with sqlite3.connect(t["env"]["LECTERN_DB"]) as db:
        db.execute(
            "UPDATE sessions SET origin='lectern', status='dead' WHERE id=?",
            (t["id"],),
        )
        db.commit()


def test_recent_closed_empty_live_list_last_thirty_restore_and_narrow(page, real_terminal):
    t = real_terminal
    assert request(t, "DELETE", f"/sessions/{t['id']}")[0] == 200
    seed_closed_rows(t, count=35)
    open_recent(page, t, width=390)

    expect(page.locator("#regular-sessions .hint")).to_contain_text("No sessions yet")
    rows = page.locator(".recent-row")
    expect(rows).to_have_count(30)
    expect(rows.last).to_contain_text("Closed fixture 29")
    assert len(t["api"]("/sessions/recent")) == 30
    expect(rows.first).to_contain_text("Real terminal")
    expect(rows.first).to_contain_text("claude")
    expect(rows.first.get_by_role("button", name="Restore tracking")).to_be_visible()
    expect(rows.first.get_by_role("button", name="Choose history")).to_have_count(0)
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1")

    rows.first.get_by_role("button", name="Restore tracking").click()
    # Restore tracking re-adopts the still-running process.  The existing
    # session card is the stable UI proof; attaching remains an explicit
    # action so a restore cannot unexpectedly take over the current tab.
    expect(page.locator(".scard", has_text="Real terminal")).to_contain_text(
        "adopted"
    )
    assert t["api"](f"/sessions/{t['id']}")["ended_at"] is None


def test_recent_closed_exact_binding_resumes_and_attaches(page, real_terminal):
    t = real_terminal
    cid, _, _ = prepare(t, "claude")
    with sqlite3.connect(t["env"]["LECTERN_DB"]) as db:
        db.execute("UPDATE sessions SET resume_id=? WHERE id=?", (cid, t["id"]))
        db.commit()
    stopped(t)
    mark_as_durable_dead(t)
    open_recent(page, t)
    recent = page.locator(".recent-row", has_text="Real terminal")
    expect(recent.get_by_role("button", name="Resume")).to_be_visible()
    expect(recent.get_by_role("button", name="Choose history")).to_have_count(0)
    recent.get_by_role("button", name="Resume").click()
    expect(page.locator("#terminal-workspace")).to_be_visible(timeout=20000)
    assert cid in argv(t)


@pytest.mark.parametrize("width", [390, 1440])
def test_recent_closed_without_binding_opens_history_picker(page, real_terminal, width):
    t = real_terminal
    stopped(t)
    mark_as_durable_dead(t)
    open_recent(page, t, width)
    recent = page.locator(".recent-row", has_text="Real terminal")
    expect(recent.get_by_role("button", name="Choose history")).to_be_visible()
    recent.get_by_role("button", name="Choose history").click()
    dialog = page.get_by_role("dialog", name="Saved conversations")
    expect(dialog).to_be_visible(timeout=10000)
    expect(dialog).to_contain_text("Real terminal")
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1")
