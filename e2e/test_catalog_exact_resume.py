"""A catalog agent's conversation is bound and restored exactly, in a browser.

The CLI is a fixture script, not a real third-party agent (docs/agents.md,
"Exact conversations for catalog agents", records the live checks against
the real CLIs). It behaves like one that saves its conversation to a store
Lectern is told how to list: on a fresh launch it writes a new conversation
for its working directory; on `--session ID` it records that it resumed ID.
The test proves Lectern binds the new conversation from the listing, shows
it in Saved conversations, and that Restore resumes exactly that id.
"""
import json
import time

import pytest
from playwright.sync_api import expect

from test_recent_sessions import open_recent
from test_session_restore import request
from test_terminal_workspace import real_terminal  # noqa: F401  (fixture)

STUB = r'''#!/usr/bin/env python3
import json, os, sys, time, uuid
store, log = os.environ["STUB_STORE"], os.environ["STUB_LOG"]
args = sys.argv[1:]
with open(log, "a") as f:
    f.write(json.dumps(args) + "\n")
if "--session" not in args:
    rows = json.load(open(store)) if os.path.exists(store) else []
    rows.append({"id": "stub-" + uuid.uuid4().hex[:12], "title": "fixture conversation",
                 "directory": os.getcwd(), "created": time.time() * 1000, "updated": time.time() * 1000})
    json.dump(rows, open(store, "w"))
print("STUB CATALOG READY", flush=True)
for line in sys.stdin:
    pass
'''


def wait(pred, timeout, what):
    end = time.monotonic() + timeout
    while time.monotonic() < end:
        value = pred()
        if value:
            return value
        time.sleep(0.25)
    raise AssertionError("timed out waiting for " + what)


def bound(t, sid):
    status, out = request(t, "GET", f"/sessions/{sid}/conversations")
    current = out.get("current") or {} if status == 200 else {}
    return current.get("id") if current.get("state") == "identified" else None


@pytest.mark.parametrize("width", [390, 1440])
def test_catalog_session_is_bound_and_restored_exactly(page, real_terminal, tmp_path, width):
    t = real_terminal
    stub, store, log = tmp_path / "stubcat.py", tmp_path / "store.json", tmp_path / "argv.log"
    stub.write_text(STUB)
    stub.chmod(0o755)
    status, _ = request(t, "PUT", "/agents", [{
        "name": "stubcat", "command": str(stub),
        "resume_id_args": ["--session", "{id}"], "fork_args": ["--session", "{id}", "--fork"],
        "env": {"STUB_STORE": str(store), "STUB_LOG": str(log)},
        "sessions": {"command": 'cat "$STUB_STORE"', "id": "id", "dir": "directory",
                     "created": "created", "updated": "updated", "title": "title"},
    }])
    assert status == 200
    try:
        status, s = request(t, "POST", "/sessions", {"target_id": t["target_id"], "workdir": str(t["root"]),
                                                      "agent": "stubcat", "name": "Catalog exact"})
        assert status == 201, s
        cid = wait(lambda: bound(t, s["id"]), 30, "the conversation to be bound")
        saved = json.loads(store.read_text())
        assert [row["id"] for row in saved] == [cid]

        # Saved conversations lists it for this agent, marked current, and says
        # plainly that Lectern does not read its messages.
        page.set_viewport_size({"width": width, "height": 900})
        page.goto(t["url"] + "/#sessions")
        card = page.locator(".scard", has_text="Catalog exact")
        expect(card).to_be_visible(timeout=15000)
        card.locator("summary").first.click()
        card.get_by_role("button", name="Saved conversations", exact=True).click()
        dialog = page.get_by_role("dialog", name="Saved conversations")
        expect(dialog).to_contain_text("fixture conversation", timeout=15000)
        expect(dialog).to_contain_text("does not read its messages", timeout=15000)
        page.keyboard.press("Escape")

        assert request(t, "DELETE", f"/sessions/{s['id']}")[0] == 200
        with open(log) as f:
            launches = len(f.readlines())
        open_recent(page, t, width)
        recent = page.locator(".recent-row", has_text="Catalog exact")
        expect(recent.get_by_role("button", name="Resume")).to_be_visible(timeout=15000)
        expect(recent.get_by_role("button", name="Choose history")).to_have_count(0)
        recent.get_by_role("button", name="Resume").click()
        resumed = wait(lambda: (lambda lines: json.loads(lines[-1]) if len(lines) > launches else None)(
            log.read_text().splitlines()), 20, "the resumed launch")
        assert resumed == ["--session", cid]
        assert page.evaluate("document.documentElement.scrollWidth <= innerWidth + 1")
    finally:
        for row in t["api"]("/sessions"):
            if row.get("agent") == "stubcat" and not row.get("ended_at"):
                request(t, "DELETE", f"/sessions/{row['id']}")
        request(t, "PUT", "/agents", [])
