"""Restore (formerly Recently closed) through the real browser and a fixture-only API."""
import re
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
    expect(page.locator(".recent-closed")).not_to_contain_text("Loading", timeout=10000)


def seed_closed_rows(t, count=9):
    now = time.time()
    with sqlite3.connect(t["env"]["LECTERN_DB"]) as db:
        for i in range(count):
            ended = now - (i + 2) * 60
            db.execute(
                """INSERT INTO sessions
                   (target_id,name,agent,workdir,tmux_session,status,origin,
                    created_at,updated_at,ended_at,end_reason,pane_tail)
                   VALUES (?,?,?,?,?,?,?,?,?,?,'stopped',?)""",
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
                    f"> last words of fixture {i + 1}{' zebracorn' if i == 11 else ''}\n> ",
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


def test_restore_lists_groups_searches_and_tracks_again_narrow(page, real_terminal):
    t = real_terminal
    assert request(t, "DELETE", f"/sessions/{t['id']}")[0] == 200
    seed_closed_rows(t, count=35)
    open_recent(page, t, width=390)

    expect(page.locator("#getting-started")).to_contain_text("Start your first agent")
    rows = page.locator(".recent-row")
    expect(rows).to_have_count(36)
    expect(rows.first).to_contain_text("Real terminal")
    expect(rows.first).to_contain_text("Tracking stopped")
    expect(page.locator('.recent-row[data-session-id="4"]')).to_contain_text("last words of fixture 3")
    assert len(t["api"]("/sessions/restorable")) == 36
    # Search matches the last message, not only the name. The word is one no
    # path can contain: every row's folder is the test's tmp_path, which under
    # xdist includes the worker name (popen-gw12) and would match a digit.
    page.locator("#restore-search").fill("zebracorn words")
    expect(rows).to_have_count(1)
    expect(rows.first).to_contain_text("Closed fixture 12")
    page.locator("#restore-search").fill("")
    expect(rows.first.get_by_role("button", name="Track again")).to_be_visible()
    expect(rows.first.get_by_role("button", name="Other agent…")).to_have_count(0)
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1")

    rows.first.get_by_role("button", name="Track again").click()
    # Restore rows reopen and attach the tracked terminal. The refreshed card
    # can appear briefly before navigation, so waiting for it here races the
    # intended terminal view rather than verifying the completed action.
    expect(page.locator("#terminal-workspace")).to_be_visible(timeout=20000)
    expect(page).to_have_url(t["url"] + f"/#terminals/session/{t['id']}")
    expect(page.get_by_role("tab", name="Real terminal", exact=True)).to_have_attribute(
        "aria-selected", "true"
    )
    restored = t["api"](f"/sessions/{t['id']}")
    assert restored["ended_at"] is None
    assert restored["origin"] == "discovered"

    page.goto(t["url"] + "/#sessions")
    expect(page.locator(".scard", has_text="Real terminal")).to_contain_text(
        "adopted", timeout=15000
    )


def test_stop_tracking_offers_undo(page, real_terminal):
    t = real_terminal
    page.set_viewport_size({"width": 390, "height": 844})
    page.goto(t["url"] + "/#sessions")
    card = page.locator(".scard", has_text="Real terminal")
    expect(card).to_be_visible(timeout=15000)
    card.locator("summary", has_text="More").click()
    card.get_by_role("button", name="Stop tracking").click()
    toast = page.locator(".toast.has-action", has_text="Stopped tracking")
    expect(toast).to_be_visible(timeout=10000)
    assert t["api"](f"/sessions/{t['id']}")["ended_at"] is not None
    toast.get_by_role("button", name="Undo").click()
    expect(page.locator("#toasts")).to_contain_text("Tracking restored", timeout=10000)
    assert t["api"](f"/sessions/{t['id']}")["ended_at"] is None


def test_interrupted_banner_restores_a_shell(page, real_terminal):
    t = real_terminal
    shell = t["api"]("/shells", {"target_id": t["target_id"]})
    # A restart took its terminal: point the record at one that no longer
    # exists and mark it the way restart recovery does.
    with sqlite3.connect(t["env"]["LECTERN_DB"]) as db:
        db.execute("UPDATE sessions SET status='interrupted', tmux_session='gone-after-restart' WHERE id=?", (shell["id"],))
        db.commit()
    page.set_viewport_size({"width": 390, "height": 844})
    page.goto(t["url"] + "/#sessions")
    banner = page.locator(".restore-banner")
    expect(banner).to_contain_text("1 session was interrupted by a restart", timeout=15000)
    card = page.locator(f'.scard[data-session-id="{shell["id"]}"]')
    expect(card.get_by_role("button", name="↺ Restore")).to_be_visible()
    banner.get_by_role("button", name="Restore it").click()
    expect(page.locator("#toasts")).to_contain_text("Restored 1 of 1", timeout=15000)
    expect(banner).to_have_count(0)
    old = t["api"](f"/sessions/{shell['id']}")
    assert old["ended_at"] is not None and old["end_reason"] == "restart"
    replacement = [s for s in t["api"]("/sessions") if s["agent"] == "shell" and s["id"] != shell["id"]]
    assert replacement and replacement[0]["workdir"] == shell["workdir"]


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
    expect(recent).to_contain_text("Continues the saved conversation")
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
