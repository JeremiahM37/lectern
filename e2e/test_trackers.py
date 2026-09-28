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
    proc = subprocess.Popen([_binary(), "serve"], cwd=ROOT, env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
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
        _tab(page, "issues")
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
        assert "#issues/1/github/pr/12" in page.url
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
        _tab(page, "issues")
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


def _post(base, path, body):
    req = urllib.request.Request(base + "/api" + path, data=json.dumps(body).encode(),
                                 headers={"Content-Type": "application/json"}, method="POST")
    return json.load(urllib.request.urlopen(req, timeout=15))


@pytest.mark.parametrize("viewport", [PHONE, DESKTOP], ids=["phone", "desktop"])
def test_reactions_and_merge_queue(browser, forge_server, viewport):
    ctx, page = _page(browser, viewport)
    try:
        page.goto(forge_server + "/#tasks/1/github/pr/14")
        detail = page.locator(".th-detail")
        expect(detail.locator("[data-pr='14']")).to_be_visible()
        detail.get_by_role("button", name="Add a reaction to #14").click()
        detail.get_by_role("menuitem", name="React hooray").click()
        expect(page.locator("#toasts")).to_contain_text("Reaction added")
        expect(detail.locator(".th-pr-head .th-reactions")).to_contain_text("🎉 1")
        comment = detail.locator(".th-comment").first
        comment.get_by_role("button", name="Add a reaction to riley's comment").click()
        comment.get_by_role("menuitem", name="React heart").click()
        expect(comment).to_contain_text("❤️ 1")

        for n in (17, 12):
            pr = _api(forge_server, f"/projects/1/forge/prs/{n}")
            _post(forge_server, f"/projects/1/forge/prs/{n}/merge", {"method": "merge", "auto": True, "confirm": True, "head_sha": pr["head_sha"]})
        page.goto(forge_server + "/#tasks/1")
        page.reload()
        page.locator(".th-tabs").get_by_role("tab", name="Merge queue").click()
        queue = page.locator(".th-queue-list")
        expect(queue.locator("li")).to_have_count(2)
        expect(queue.locator("li").first).to_contain_text("#17")
        expect(queue.locator("li").first).to_contain_text("awaiting checks")
        queue.get_by_role("button", name="Remove #17 from the merge queue").click()
        page.locator("#th-dequeue-go").click()
        expect(page.locator("#toasts")).to_contain_text("#17 removed from the merge queue")
        expect(queue.locator("li")).to_have_count(1)
        expect(queue.locator(".th-queue-pos")).to_have_text("1")
        assert _api(forge_server, "/projects/1/forge/prs/17")["auto_merge"] is None
    finally:
        ctx.close()


class _FakeLinear:
    """A tiny Linear GraphQL stand-in on a local port for the editor test."""

    def __init__(self):
        import http.server
        import threading
        state = self.state = {"description": "Search is slow.", "saved": []}

        class H(http.server.BaseHTTPRequestHandler):
            def log_message(self, *a):
                pass

            def do_POST(self):
                body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"{}")
                q = body.get("query", "")
                issue = {"id": "u1", "identifier": "ENG-21", "title": "Index titles", "url": "https://linear.example/ENG-21",
                         "branchName": "eng-21-index-titles", "description": state["description"],
                         "state": {"id": "s1", "name": "Todo", "type": "unstarted"}, "team": {"id": "t", "key": "ENG"},
                         "labels": {"nodes": []}, "children": {"nodes": []}, "comments": {"nodes": []}, "updatedAt": "2026-09-27T09:00:00Z"}
                if "issueUpdate" in q:
                    state["description"] = body["variables"]["d"]
                    state["saved"].append(body["variables"]["d"])
                    out = {"data": {"issueUpdate": {"success": True}}}
                elif "issues(filter" in q:
                    out = {"data": {"issues": {"nodes": [issue]}}}
                elif "workflowStates" in q:
                    out = {"data": {"workflowStates": {"nodes": [{"id": "s1", "name": "Todo", "type": "unstarted"}]}}}
                elif "teams(" in q:
                    out = {"data": {"teams": {"nodes": [{"id": "t", "key": "ENG", "name": "Engineering"}]}}}
                else:
                    out = {"data": {"issue": issue}}
                raw = json.dumps(out).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(raw)))
                self.end_headers()
                self.wfile.write(raw)

        self.srv = http.server.HTTPServer(("127.0.0.1", 0), H)
        self.url = f"http://127.0.0.1:{self.srv.server_address[1]}/graphql"
        threading.Thread(target=self.srv.serve_forever, daemon=True).start()

    def close(self):
        self.srv.shutdown()


@pytest.mark.parametrize("viewport", [PHONE, DESKTOP], ids=["phone", "desktop"])
def test_linear_description_rich_editor(browser, forge_server, viewport):
    fake = _FakeLinear()
    ctx, page = _page(browser, viewport)
    try:
        conn = _post(forge_server, "/projects/1/trackers", {"kind": "linear", "config": {"team_key": "ENG", "api_url": fake.url},
                                                           "secrets": {"api_key": "lin_api_e2e"}})
        page.goto(forge_server + f"/#tasks/1/linear/issue/ENG-21/{conn['id']}")
        detail = page.locator(".th-detail")
        expect(detail).to_contain_text("Search is slow.")
        desc = detail.locator(".th-desc")
        desc.get_by_role("button", name="Edit").click()
        box = desc.get_by_role("textbox", name="Description")
        box.fill("Search is slow on big workspaces.")
        # select "big" and make it bold with the toolbar
        box.evaluate("t => { const i = t.value.indexOf('big'); t.setSelectionRange(i, i + 3); }")
        desc.get_by_role("button", name="Bold (Ctrl+B)").click()
        expect(box).to_have_value("Search is slow on **big** workspaces.")
        desc.get_by_role("button", name="Preview").click()
        expect(desc.locator(".th-editor-preview strong")).to_have_text("big")
        desc.get_by_role("button", name="Save description").click()
        expect(page.locator("#toasts")).to_contain_text("Description saved")
        assert fake.state["saved"] == ["Search is slow on **big** workspaces."]
        expect(detail.locator(".th-desc strong")).to_have_text("big")
    finally:
        ctx.close()
        fake.close()
