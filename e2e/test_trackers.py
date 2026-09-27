"""Tasks hub (docs/trackers.md) in a real browser against a real lectern.

The mock target answers gh with a small scripted GitHub repository
(internal/executor/mock_forge.go), so the pull request page, its checks,
reviewers and the confirmed merge run the genuine API and UI end to end with
no network. Each test gets its own server, so one test's merge cannot leak
into another's list.
"""
import json
import subprocess
import time
import urllib.request
from pathlib import Path

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, OUTSIDE_WORLD, PHONE, _binary, _port_open, _unused_port
from test_ui import _tab

ROOT = Path(__file__).resolve().parents[1]


@pytest.fixture()
def forge_server(tmp_path):
    port = _unused_port()
    (tmp_path / "home").mkdir()
    (tmp_path / "tmux").mkdir()
    env = {
        **__import__("os").environ, **OUTSIDE_WORLD,
        "LECTERN_MOCK": "1", "LECTERN_TICK": "0.1", "LECTERN_MOCK_DELAY": "0.1",
        "LECTERN_PORT": str(port), "LECTERN_DB": str(tmp_path / "trackers.db"),
        "LECTERN_BASE_URL": f"http://127.0.0.1:{port}", "HOME": str(tmp_path / "home"),
        "TMUX": "", "TMUX_TMPDIR": str(tmp_path / "tmux"),
    }
    proc = subprocess.Popen([_binary()], cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        for _ in range(150):
            if proc.poll() is not None:
                raise RuntimeError(f"server exited with {proc.returncode}")
            if _port_open(port):
                break
            time.sleep(0.1)
        yield f"http://127.0.0.1:{port}"
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait()


def _page(browser, viewport):
    ctx = browser.new_context(viewport=viewport)
    return ctx, ctx.new_page()


def _api(base, path):
    return json.load(urllib.request.urlopen(base + "/api" + path, timeout=15))


def _row(page, title):
    return page.locator(".th-row").filter(has_text=title)


@pytest.mark.parametrize("viewport", [PHONE, DESKTOP], ids=["phone", "desktop"])
def test_pr_page_checks_reviewers_and_confirmed_merge(browser, forge_server, viewport):
    ctx, page = _page(browser, viewport)
    try:
        page.goto(forge_server + "/")
        _tab(page, "tasks")
        expect(page.locator(".th-heading")).to_contain_text("mock/repo")
        expect(_row(page, "Stream sync progress to the dashboard")).to_contain_text("checks failing")
        expect(_row(page, "Rename the config loader")).to_contain_text("conflicts")

        _row(page, "Stream sync progress to the dashboard").click()
        detail = page.locator(".th-detail")
        expect(detail.locator("[data-pr='14']")).to_be_visible()
        expect(detail.locator(".th-stack")).to_contain_text("#12")
        # drill into the failing job's log
        failing = detail.locator(".th-checks li").filter(has_text="unit tests")
        failing.get_by_role("button", name="Log").click()
        expect(failing.locator(".th-log")).to_contain_text("AssertionError: expected 3 of 5 retries used")
        # ask for a review
        detail.get_by_role("button", name="+ Request review").click()
        detail.get_by_label("Request review").fill("devon")
        detail.get_by_role("button", name="Add", exact=True).click()
        expect(detail.locator(".th-reviewers")).to_contain_text("devon")
        assert any(r["login"] == "devon" for r in _api(forge_server, "/projects/1/forge/prs/14")["reviewers"])

        # the stacked parent opens from the stack, and merges behind a confirmation
        detail.locator(".th-stack").get_by_role("button").filter(has_text="Add a retry budget").click()
        expect(detail.locator("[data-pr='12']")).to_be_visible()
        detail.locator("#th-merge").click()
        dialog = page.locator("dialog.th-confirm")
        expect(dialog).to_contain_text("a1b2c3d")
        expect(dialog).to_contain_text("squash and merge")
        assert _api(forge_server, "/projects/1/forge/prs/12")["state"] == "open", "merged before confirming"
        dialog.locator("#th-confirm-go").click()
        expect(page.locator("#toasts")).to_contain_text("Merged #12")
        expect(detail.locator(".th-pr-head .th-state")).to_have_text("merged")
        assert _api(forge_server, "/projects/1/forge/prs/12")["state"] == "merged"
        # the page links straight to its item
        assert "#tasks/1/github/pr/12" in page.url
    finally:
        ctx.close()


def test_conflicts_resolve_and_fix_checks_with_agent(browser, forge_server):
    ctx, page = _page(browser, DESKTOP)
    try:
        page.goto(forge_server + "/#tasks/1/github/pr/15")
        detail = page.locator(".th-detail")
        expect(detail.locator(".th-conflicts-box")).to_contain_text("src/config.py")
        expect(detail.locator("#th-merge")).to_have_count(0)
        detail.locator("#th-resolve").click()
        expect(page.locator("#toasts")).to_contain_text('Started session "PR #15 · resolve conflicts"')
        sessions = _api(forge_server, "/sessions?all=true")
        assert any(s["name"] == "PR #15 · resolve conflicts" for s in sessions)

        page.goto(forge_server + "/#tasks/1/github/pr/14")
        page.reload()
        detail = page.locator(".th-detail")
        detail.locator("#th-fix").click()
        expect(page.locator("#toasts")).to_contain_text('Started session "PR #14 · fix checks"')
        page.goto(forge_server + "/#tasks/1/github/pr/14")
        page.reload()
        expect(page.locator(".th-detail .th-mergebox")).to_contain_text("CI")
        expect(page.locator(".th-detail #th-fix")).to_be_disabled()
    finally:
        ctx.close()


def test_issue_starts_a_task_on_phone(browser, forge_server):
    ctx, page = _page(browser, PHONE)
    try:
        page.goto(forge_server + "/")
        _tab(page, "tasks")
        page.locator(".th-tabs").get_by_role("tab", name="Issues").click()
        _row(page, "Document the retry budget").click()
        detail = page.locator(".th-detail")
        expect(detail).to_contain_text("Explain the window")
        detail.locator("#th-start").click()
        dialog = page.locator("dialog.th-start")
        expect(dialog.locator("#th-start-branch")).to_have_value("8-document-the-retry-budget")
        dialog.get_by_role("radio", name="Task").click()
        dialog.get_by_label("Dispatch now").uncheck()
        dialog.locator("#th-start-go").click()
        expect(page.locator("#toasts")).to_contain_text("Created task #")
        tasks = _api(forge_server, "/tasks")
        assert any(t["title"] == "[8] Document the retry budget" and "Explain the window" in t["prompt"] for t in tasks)
    finally:
        ctx.close()
