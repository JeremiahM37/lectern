"""Budgets (docs/budgets.md): a tiny overall daily limit, real spend posted
through the same session statusline hook fixture test_usage_view.py uses,
and the resulting alert/blocked state shown live in the UI — Settings ->
Budgets, the Usage page's budget bars, and a blocked dispatch banner on the
board. No mocking of internal/budget itself: this drives the real HTTP API
exactly as a browser and a real Claude Code statusline would. The session
whose statusline is posted is created directly through POST /api/sessions
(a real session, real hook_token, real usage_daily row) rather than through
the New Session sheet — this test is about the budgets surfaces, not about
that sheet, which test_usage_view.py already covers end to end.
"""
import json
import sqlite3
import time
import urllib.request

from playwright.sync_api import expect

from test_usage_view import STATUSLINE_FIXTURE, _hook_token, usage_server  # noqa: F401  (fixture)
from test_ui import _tab


def _put_budgets(base, body):
    req = urllib.request.Request(
        f"{base}/api/budgets",
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json"},
        method="PUT",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        assert resp.status == 200
        return json.load(resp)


def _spend_via_statusline(base, db_path, name="budget spend"):
    """Creates a real session and posts a real statusline hook payload for
    it, the same fixture and endpoint test_usage_view.py's own test uses —
    returns the session id. This is real spend, booked into usage_daily by
    internal/agentevents.IngestStatusline, not a database poke."""
    projects = json.load(urllib.request.urlopen(base + "/api/projects", timeout=10))
    req = urllib.request.Request(
        base + "/api/sessions",
        data=json.dumps({"project_id": projects[0]["id"], "name": name}).encode(),
        headers={"Content-Type": "application/json"},
        method="POST",
    )
    session = json.load(urllib.request.urlopen(req, timeout=10))
    token = _hook_token(db_path, session["id"])
    req = urllib.request.Request(
        f"{base}/api/hook/session/{session['id']}/statusline",
        data=STATUSLINE_FIXTURE,
        headers={"Authorization": f"Bearer {token}", "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=10) as resp:
        assert resp.status == 200
    return session["id"]


def test_budgets_settings_ui_round_trips(browser, usage_server):
    base, _db_path = usage_server
    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "targets")
    page.locator('[data-settings="budgets"]').click()

    panel = page.locator("#budgets-panel")
    expect(panel).to_be_visible(timeout=10000)
    page.fill("#budget-overall-daily", "5")
    page.select_option("#budget-overall-mode", "stop")
    page.click("#budget-save")
    expect(page.locator(".toast", has_text="Budgets saved")).to_be_visible(timeout=10000)

    # Reload and confirm it persisted through a real PUT/GET round trip.
    page.reload()
    _tab(page, "targets")
    page.locator('[data-settings="budgets"]').click()
    expect(page.locator("#budget-overall-daily")).to_have_value("5", timeout=10000)
    expect(page.locator("#budget-overall-mode")).to_have_value("stop")
    context.close()


def test_stop_mode_shows_blocked_state_and_refuses_dispatch(browser, usage_server):
    base, db_path = usage_server
    _spend_via_statusline(base, db_path)
    # STATUSLINE_FIXTURE reports total_cost_usd ~ $0.114. A cap well below
    # that, in stop mode, must show as blocked and refuse a new dispatch.
    _put_budgets(base, {"overall": {"daily_usd": 0.01, "weekly_usd": 0, "mode": "stop"}})

    context = browser.new_context()
    page = context.new_page()
    page.goto(base)

    # Usage page shows the budget bar as blocked.
    _tab(page, "targets")
    page.locator('[data-settings="about"]').click()
    row = page.locator(".usage-budget-row", has_text="Overall (daily)")
    expect(row).to_be_visible(timeout=10000)
    expect(row).to_contain_text("blocked", timeout=10000)

    # A new dispatch is refused with the exhausted budget's own message, and
    # the toast banner (the same path every other API rejection surfaces
    # through) shows it. The quota-chip area on the board header also
    # carries a compact blocked indicator.
    _tab(page, "board")
    expect(page.locator(".budget-chip.budget-blocked")).to_be_visible(timeout=10000)
    page.click("#fab")
    page.fill("#f-title", "should be refused")
    page.click("#f-go")
    toast = page.locator(".toast.err")
    expect(toast).to_be_visible(timeout=10000)
    expect(toast).to_contain_text("budget", timeout=10000)
    context.close()


def test_warn_mode_does_not_block_dispatch(browser, usage_server):
    base, db_path = usage_server
    _spend_via_statusline(base, db_path)
    _put_budgets(base, {"overall": {"daily_usd": 0.01, "weekly_usd": 0, "mode": "warn"}})

    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "board")
    page.click("#fab")
    page.fill("#f-title", "warn mode still dispatches")
    page.click("#f-go")
    card = page.locator(".card", has_text="warn mode still dispatches")
    expect(card).to_be_visible(timeout=10000)
    expect(page.locator(".toast.err")).to_have_count(0)
    context.close()






def test_model_price_editor_highlights_unpriced_codex_and_saves(browser, usage_server):
    """Codex usage with no price is called out; setting input/cached/output in
    Settings → Budgets saves through /api/model-prices and clears the notice.
    A negative rate is refused before saving."""
    base, db_path = usage_server
    projects = json.load(urllib.request.urlopen(base + "/api/projects", timeout=10))
    conn = sqlite3.connect(str(db_path))
    try:
        now = time.time()
        cur = conn.execute(
            "INSERT INTO tasks(project_id, title, status, agent, model, created_at, updated_at) VALUES (?,?,?,?,?,?,?)",
            (projects[0]["id"], "codex work", "done", "codex", "gpt-5-codex", now, now),
        )
        conn.execute(
            "INSERT INTO attempts(task_id, n, status, started_at, finished_at, result_json) VALUES (?,1,'done',?,?,?)",
            (cur.lastrowid, now - 30, now, json.dumps({"input_tokens": 120000, "cached_input_tokens": 100000, "output_tokens": 3000})),
        )
        conn.commit()
    finally:
        conn.close()

    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "targets")
    page.locator('[data-settings="budgets"]').click()
    panel = page.locator("#model-prices")
    notice = panel.locator("#model-prices-unpriced")
    expect(notice).to_contain_text("Codex spend isn't shown until you set a price for gpt-5-codex", timeout=10000)
    panel.get_by_role("button", name="Set price").click()
    panel.get_by_label("gpt-5-codex input per 1M").fill("-1")
    panel.get_by_label("gpt-5-codex output per 1M").fill("10")
    expect(panel.get_by_role("alert")).to_contain_text("0 or more")
    expect(panel.locator("#model-prices-save")).to_be_disabled()
    panel.get_by_label("gpt-5-codex input per 1M").fill("1.25")
    panel.get_by_label("gpt-5-codex cached per 1M").fill("0.125")
    panel.locator("#model-prices-save").click()
    expect(notice).to_have_count(0, timeout=10000)
    saved = json.load(urllib.request.urlopen(base + "/api/model-prices", timeout=10))
    assert saved["prices"]["gpt-5-codex"] == {"input_per_1m": 1.25, "output_per_1m": 10, "cached_input_per_1m": 0.125}
    assert saved["seen"][0]["priced"] is True
    context.close()
