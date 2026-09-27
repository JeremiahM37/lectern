"""Accounts (docs/accounts.md) in the real UI against a private mock-mode
server: add a second Claude login in Settings -> Accounts, see it listed with
the CLI's own login as "Default" and no directory anywhere on the page, open
its sign-in terminal, turn the swap switch on in the usage-limit policy, and
see a session card name its account once there is more than one."""
import json
import time
import urllib.request

from playwright.sync_api import expect

from test_usage_view import usage_server  # noqa: F401  (fixture)
from test_ui import _tab


def _get(base, path):
    return json.load(urllib.request.urlopen(base + path, timeout=10))


def test_accounts_settings_swap_switch_and_card_chip(browser, usage_server):
    base, _db = usage_server
    project = _get(base, "/api/projects")[0]
    target = next(t for t in _get(base, "/api/targets") if t["id"] == project["target_id"])
    context = browser.new_context()
    page = context.new_page()
    page.goto(base)
    _tab(page, "targets")
    page.locator('[data-settings="accounts"]').click()
    panel = page.locator("#accounts-panel")
    expect(panel).to_contain_text("Only add accounts that are your own", timeout=10000)
    page.get_by_label("Account machine").select_option(str(target["id"]))
    page.get_by_label("Account label").fill("work")
    page.get_by_role("button", name="Add account").click()
    rows = panel.locator(".accounts-row")
    expect(rows).to_have_count(2, timeout=10000)
    expect(rows.nth(0)).to_contain_text("Default")
    expect(rows.nth(1)).to_contain_text("work")
    expect(rows.nth(1)).to_contain_text("free")
    assert ".lectern/accounts" not in page.content(), "an account directory reached the page"

    # Sign in opens a terminal on the sign-in shell.
    rows.nth(1).get_by_role("button", name="Sign in").click()
    deadline = time.time() + 10
    while time.time() < deadline:
        if any(s["name"] == "Sign in · work" and s["agent"] == "shell" for s in _get(base, "/api/sessions")):
            break
        time.sleep(0.2)
    else:
        raise AssertionError("no sign-in terminal was started")

    # The swap switch in the global policy.
    page.goto(base)
    _tab(page, "targets")
    page.locator('[data-settings="budgets"]').click()
    page.get_by_label("Swap accounts").check()
    page.get_by_label("Usage-limit policy").select_option("wait")
    page.get_by_role("button", name="Save usage-limit policy").click()
    expect(page.locator(".limit-policy-status")).to_have_text("Saved usage-limit policy", timeout=10000)
    policy = _get(base, "/api/limits/policy")
    assert policy["effective"]["mode"] == "swap" and policy["effective"]["then"] == "wait", policy

    # A Claude session on that machine now shows which login it runs under.
    req = urllib.request.Request(base + "/api/sessions", method="POST",
                                 data=json.dumps({"project_id": project["id"], "name": "acct card", "agent": "claude"}).encode(),
                                 headers={"Content-Type": "application/json"})
    urllib.request.urlopen(req, timeout=10)
    page.goto(base + "/#sessions")
    card = page.locator(".scard", has_text="acct card").first
    expect(card.locator(".account-chip")).to_contain_text("Default", timeout=15000)
    context.close()
