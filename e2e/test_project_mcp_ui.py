"""MCP project editing through the real PWA and the real terminal client."""

import json
import fcntl
import os
import pty
import select
import subprocess
import termios
import time

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE, _binary
from test_terminal_workspace import real_terminal
from test_terminal_dashboard import Dashboard
from test_ui import _tab

@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True, ids=["phone390", "desktop1440"])
def test_project_mcp_editor_adds_edits_removes_without_secret_leak(page, server):
    target = page.request.get(f"{server}/api/targets").json()[0]
    project = page.request.post(f"{server}/api/projects", data={
        "name": "MCP UI fixture", "target_id": target["id"], "repo_path": "/mock/mcp-ui",
        "mcp": {"tokenizer": {"command": "fixture-mcp", "env": {"TOKEN": "browser-secret"}}},
    }).json()
    try:
        page.goto(server)
        _tab(page, "targets")
        page.locator('[data-settings="projects"]').click()
        page.fill("#pj-search", "MCP UI fixture")
        page.locator(".pjrow", has_text="MCP UI fixture").click()
        card = page.locator("#sheet .project-mcp")
        expect(card).to_be_visible()
        expect(card.locator(".project-mcp-status")).to_contain_text("loaded", timeout=10000)
        expect(card.locator(".mcp-command")).to_have_value("fixture-mcp")
        assert "__lectern_retained" in card.locator(".mcp-extra").input_value()
        assert "browser-secret" not in card.inner_text()

        card.locator(".project-mcp-add").click()
        rows = card.locator(".mcp-row")
        new_row = rows.last
        new_row.locator(".mcp-name").fill("http_fixture")
        new_row.locator(".mcp-type").select_option("http")
        new_row.locator(".mcp-command").fill("https://example.test/mcp")
        new_row.locator(".mcp-extra").fill('{"headers":{"X-Fixture":"ok"}}')
        card.locator(".project-mcp-save").click()
        expect(card.locator(".project-mcp-status")).to_contain_text("Saved", timeout=10000)

        # Edit the existing stdio command while retaining the masked token.
        rows = card.locator(".mcp-row")
        stdio_row = next(rows.nth(i) for i in range(rows.count())
                         if rows.nth(i).locator(".mcp-name").input_value() == "tokenizer")
        stdio_row.locator(".mcp-command").fill("fixture-mcp-v2")
        card.locator(".project-mcp-save").click()
        expect(card.locator(".project-mcp-status")).to_contain_text("Saved", timeout=10000)

        # Remove the HTTP row and verify it is gone from the authoritative API.
        rows = card.locator(".mcp-row")
        http_row = next(rows.nth(i) for i in range(rows.count())
                        if rows.nth(i).locator(".mcp-name").input_value() == "http_fixture")
        http_row.locator(".mcp-remove").click()
        card.locator(".project-mcp-save").click()
        expect(card.locator(".project-mcp-status")).to_contain_text("Saved", timeout=10000)
        saved = page.request.get(f"{server}/api/projects/{project['id']}/mcp").json()
        assert set(saved["mcp"]) == {"tokenizer"}
        assert saved["mcp"]["tokenizer"]["command"] == "fixture-mcp-v2"
        assert "browser-secret" not in json.dumps(saved)
        assert page.evaluate("document.body.scrollWidth <= window.innerWidth")
        page.screenshot(path=f"/tmp/lectern-mcp-{page.viewport_size['width']}.png", full_page=True)
    finally:
        page.request.delete(f"{server}/api/projects/{project['id']}")


@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True, ids=["phone390", "desktop1440"])
def test_project_mcp_browser_conflict_preserves_draft_for_explicit_retry(page, server):
    target = page.request.get(f"{server}/api/targets").json()[0]
    project = page.request.post(f"{server}/api/projects", data={
        "name": "MCP conflict fixture", "target_id": target["id"], "repo_path": "/mock/mcp-conflict",
        "mcp": {"fixture": {"command": "fixture", "env": {"TOKEN": "browser-conflict-secret"}}},
    }).json()
    try:
        page.goto(server)
        _tab(page, "targets")
        page.locator('[data-settings="projects"]').click()
        page.fill("#pj-search", "MCP conflict fixture")
        page.locator(".pjrow", has_text="MCP conflict fixture").click()
        card = page.locator("#sheet .project-mcp")
        expect(card.locator(".project-mcp-status")).to_contain_text("loaded", timeout=10000)

        # A separate writer changes strict_mcp after the editor's GET. The
        # first save must 409, keep the draft, and refresh only its revision.
        changed = page.request.patch(f"{server}/api/projects/{project['id']}", data={"strict_mcp": True})
        assert changed.ok
        row = card.locator(".mcp-row").first
        row.locator(".mcp-command").fill("fixture-v2")
        card.locator(".project-mcp-save").click()
        expect(card.locator(".project-mcp-status")).to_contain_text("draft is preserved", timeout=10000)
        expect(row.locator(".mcp-command")).to_have_value("fixture-v2")

        # Saving again is the explicit conflict resolution. The refreshed
        # revision is accepted without losing the user's command edit.
        card.locator(".project-mcp-save").click()
        expect(card.locator(".project-mcp-status")).to_contain_text("Saved", timeout=10000)
        saved = page.request.get(f"{server}/api/projects/{project['id']}/mcp").json()
        assert saved["mcp"]["fixture"]["command"] == "fixture-v2"
        assert saved["strict_mcp"] is False
        assert "browser-conflict-secret" not in json.dumps(saved)
    finally:
        page.request.delete(f"{server}/api/projects/{project['id']}")


def test_project_mcp_full_dashboard_editor_keyboard_crud(real_terminal):
    """The full-screen Bubble Tea dashboard edits MCP without a browser."""
    t = real_terminal
    project = t["api"]("/projects", {
        "name": "Dashboard MCP fixture", "target_id": t["target_id"], "repo_path": str(t["root"]),
        "mcp": {"tokenizer": {"command": "fixture", "env": {"TOKEN": "dashboard-secret"}}},
    })
    d = Dashboard(t)
    try:
        d.wait("Real terminal")
        d.send("4")
        d.wait("Dashboard MCP fixture")
        def open_mcp_editor():
            d.send("m")
            d.wait("MCP settings (add / edit / remove)")
            d.send("j" * 8 + "\r")
            d.wait("Servers JSON")

        open_mcp_editor()
        # Bubble's textarea treats Ctrl-A as line start. Move to input start
        # and delete each existing wrapped line before typing the replacement.
        d.send('\x1b[1;5H' + '\x0b' * 30 + '{"tokenizer":{"command":"fixture-v2"},"http_fixture":{"url":"https://example.test/mcp"}}\x13')
        d.wait("Save MCP settings completed")
        saved = t["api"](f"/projects/{project['id']}/mcp")
        assert set(saved["mcp"]) == {"tokenizer", "http_fixture"}
        assert saved["mcp"]["tokenizer"]["command"] == "fixture-v2"
        assert "dashboard-secret" not in json.dumps(saved)

        # Reopen through the same full-screen actions, edit the stdio entry,
        # and remove the HTTP entry using only the keyboard editor.
        d.wait("Dashboard MCP fixture")
        open_mcp_editor()
        d.send('\x1b[1;5H' + '\x0b' * 30 + '{"tokenizer":{"command":"fixture-v3"}}\x13')
        d.wait("Save MCP settings completed")
        saved = t["api"](f"/projects/{project['id']}/mcp")
        assert set(saved["mcp"]) == {"tokenizer"}
        assert saved["mcp"]["tokenizer"]["command"] == "fixture-v3"
        d.quit()
    finally:
        d.close()


def test_project_mcp_plain_client_crud_over_controlling_pty(real_terminal):
    """Add/edit/remove the complete document from console --plain over a PTY."""
    t = real_terminal
    project = t["api"]("/projects", {"name": "PTY MCP fixture", "target_id": t["target_id"], "repo_path": str(t["root"])})
    def run_flow(document):
        master, slave = pty.openpty()
        def controlling_terminal():
            os.setsid()
            fcntl.ioctl(0, termios.TIOCSCTTY, 0)
        proc = subprocess.Popen([_binary(), "console", "--plain"], stdin=slave, stdout=slave,
                                stderr=slave, env={**os.environ, "LECTERN_API": t["url"]},
                                preexec_fn=controlling_terminal)
        os.close(slave)
        output = b""

        def wait_for(token, timeout=12):
            nonlocal output
            end = time.monotonic() + timeout
            while time.monotonic() < end:
                if token in output:
                    return
                if select.select([master], [], [], .1)[0]:
                    try:
                        output += os.read(master, 65536)
                    except OSError:
                        break
            raise AssertionError(f"missing {token!r}: {output.decode(errors='replace')}")

        def send(text):
            os.write(master, text.encode())

        try:
            wait_for(b"Open:"); send("4\n")
            wait_for(b"Choose:"); send(f"{project['id']}\n")
            wait_for(b"Action:"); send("mcp\n")
            wait_for(b"MCP JSON or @file"); send(document + "\n")
            wait_for(b"Claude strict replacement"); send("false\n")
            wait_for(b"Action completed.")
            send("b\nb\nq\n"); proc.wait(timeout=10)
            assert proc.returncode == 0
        finally:
            if proc.poll() is None:
                proc.terminate(); proc.wait(timeout=10)
            os.close(master)

    # First pass: add a stdio server and an HTTP server.
    run_flow('{"stdio_fixture":{"command":"fixture"},"http_fixture":{"url":"https://example.test/mcp"}}')
    assert set(t["api"](f"/projects/{project['id']}/mcp")["mcp"]) == {"stdio_fixture", "http_fixture"}
    # Second pass: edit the stdio entry and remove the HTTP entry.
    run_flow('{"stdio_fixture":{"command":"fixture-v2"}}')
    saved = t["api"](f"/projects/{project['id']}/mcp")
    assert set(saved["mcp"]) == {"stdio_fixture"}
    assert saved["mcp"]["stdio_fixture"]["command"] == "fixture-v2"
