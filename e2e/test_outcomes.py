"""Cost per outcome (docs/outcomes.md): two stub task attempts with
different costs and check outcomes, seeded directly into the same private
sqlite database the `usage_server` fixture (test_usage_view.py) already
gives every budgets/usage e2e test — real HTTP against GET /api/outcomes and
the real Settings -> Usage & about -> Outcomes table, not a mock of the API.

Attempts are stubbed directly at the database layer (task+attempt rows)
rather than run through a real mock agent: the point of this test is the
Outcomes table's ranking and derived math (already exhaustively covered at
the unit level by internal/outcomes' own tests), not the dispatch pipeline —
GET /api/usage's own e2e coverage (test_usage_view.py) already exercises a
real session's hook flow end to end.
"""
import json
import sqlite3
import time
import urllib.request

from playwright.sync_api import expect

from test_usage_view import usage_server  # noqa: F401  (fixture)
from test_ui import _tab


def _seed_attempt(db_path, project_id, agent, model, cost_usd, check_passed, accepted):
    conn = sqlite3.connect(str(db_path))
    try:
        now = time.time()
        status = "done" if accepted else "review"
        cur = conn.execute(
            "INSERT INTO tasks(project_id, title, status, agent, created_at, updated_at) "
            "VALUES (?,?,?,?,?,?)",
            (project_id, f"outcome e2e {agent}", status, agent, now, now),
        )
        task_id = cur.lastrowid
        rc = 0 if check_passed else 1
        conn.execute(
            "INSERT INTO attempts(task_id, n, status, model, started_at, finished_at, "
            "result_json, verify_json) VALUES (?,1,'done',?,?,?,?,?)",
            (
                task_id,
                model,
                now - 30,
                now,
                json.dumps({"cost_usd": cost_usd}),
                json.dumps({"cmd": "verify", "rc": rc, "output": ""}),
            ),
        )
        conn.commit()
    finally:
        conn.close()


def test_outcomes_table_ranks_attempts_by_spend_and_shows_cost_per_pass(browser, usage_server):
    base, db_path = usage_server
    projects = json.load(urllib.request.urlopen(base + "/api/projects", timeout=10))
    project_id = projects[0]["id"]

    # A cheap, passing/accepted claude attempt and a pricier, passing/
    # accepted codex attempt — codex should outrank claude by total spend,
    # and each row's own $/pass should be its own cost (one passing attempt
    # each).
    _seed_attempt(db_path, project_id, "claude", "opus", 1.0, check_passed=True, accepted=True)
    _seed_attempt(db_path, project_id, "codex", "gpt-6", 9.0, check_passed=True, accepted=True)

    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "targets")
    page.locator('[data-settings="about"]').click()

    table = page.locator(".outcomes-table")
    expect(table).to_be_visible(timeout=10000)
    rows = table.locator("tbody tr")
    expect(rows).to_have_count(2, timeout=10000)

    # Highest spend (codex, $9.00) ranks first.
    first_row = rows.nth(0)
    expect(first_row).to_contain_text("codex", timeout=10000)
    expect(first_row).to_contain_text("$9.00")
    expect(first_row).to_contain_text("$9.00")  # cost + $/pass, both $9.00 for one pass

    second_row = rows.nth(1)
    expect(second_row).to_contain_text("claude")
    expect(second_row).to_contain_text("$1.00")

    # The spend-comparison bar list reflects the same ranking.
    bars = page.locator(".outcomes-bar-row")
    expect(bars.first).to_contain_text("codex", timeout=10000)
    context.close()


def test_outcomes_group_selector_switches_to_model(browser, usage_server):
    base, db_path = usage_server
    projects = json.load(urllib.request.urlopen(base + "/api/projects", timeout=10))
    project_id = projects[0]["id"]
    _seed_attempt(db_path, project_id, "claude", "opus", 2.0, check_passed=True, accepted=True)
    _seed_attempt(db_path, project_id, "claude", "sonnet", 0.5, check_passed=False, accepted=False)

    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "targets")
    page.locator('[data-settings="about"]').click()
    expect(page.locator(".outcomes-table")).to_be_visible(timeout=10000)

    page.locator(".outcomes-panel select").first.select_option("model")
    rows = page.locator(".outcomes-table tbody tr")
    expect(rows).to_have_count(2, timeout=10000)
    expect(rows.nth(0)).to_contain_text("opus")
    expect(rows.nth(1)).to_contain_text("sonnet")
    context.close()
