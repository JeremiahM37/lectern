"""Exited agents, the relaunch notice, through the real browser."""
import sqlite3
import time

import pytest
from playwright.sync_api import expect

from test_terminal_workspace import real_terminal  # noqa: F401  (fixture)


def mark(t, **fields):
    sets = ", ".join(f"{k}=?" for k in fields)
    with sqlite3.connect(t["env"]["LECTERN_DB"]) as db:
        db.execute(f"UPDATE sessions SET {sets} WHERE id=?", (*fields.values(), t["id"]))
        db.commit()


# Revive starts the agent again, so it must be installed: Lectern refuses an
# agent whose program is missing (sessions/agent_installed.go).
@pytest.mark.parametrize("real_terminal", [{"agent_script": "#!/bin/sh\nexec sleep 600\n"}], indirect=True)
def test_exited_agent_card_terminal_and_revive(page, real_terminal):
    t = real_terminal
    mark(t, agent_exited_at=time.time())
    page.set_viewport_size({"width": 390, "height": 844})
    page.goto(t["url"] + f"/terminal/session/{t['id']}")
    banner = page.locator("#agent-exited")
    expect(banner).to_contain_text("The agent exited", timeout=15000)
    expect(banner.get_by_role("button", name="↻ Revive")).to_be_visible()
    banner.get_by_role("button", name="Dismiss").click()
    expect(banner).to_have_count(0)

    page.goto(t["url"] + "/#sessions")
    card = page.locator(f'.scard[data-session-id="{t["id"]}"]')
    expect(card).to_contain_text("agent exited", timeout=15000)
    expect(page.locator("#now-strip")).to_contain_text("Ended · agent exited")
    card.get_by_role("button", name="↻ Revive").click()
    expect(page.locator("#toasts")).to_contain_text("Revived", timeout=20000)
    old = t["api"](f"/sessions/{t['id']}")
    # The adopted terminal is the operator's own: released, never closed.
    assert old["ended_at"] is not None and old["end_reason"] == "released"


def test_relaunch_notice_lists_and_dismisses(page, real_terminal):
    t = real_terminal
    mark(t, relaunched_at=time.time())
    page.set_viewport_size({"width": 1280, "height": 900})
    page.goto(t["url"] + "/#sessions")
    notice = page.locator(".relaunch-notice")
    expect(notice).to_contain_text("Relaunched 1 session after a restart", timeout=15000)
    expect(notice).to_contain_text("Real terminal")
    notice.get_by_role("button", name="Dismiss").click()
    expect(notice).to_have_count(0)
    assert t["api"]("/sessions/relaunched") == []
    page.reload()
    expect(page.locator(".scard", has_text="Real terminal")).to_be_visible(timeout=15000)
    expect(page.locator(".relaunch-notice")).to_have_count(0)
