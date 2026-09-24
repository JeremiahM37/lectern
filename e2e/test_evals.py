"""Agent tests ("evals") end to end: create a suite, run it, watch the matrix
and leaderboard fill in — all against the mock target, so the run finishes in
well under the test's own poll budget.
"""
from playwright.sync_api import expect

from test_ui import _tab


def test_create_suite_run_it_and_see_the_matrix_fill(page, server):
    page.goto(server)
    _tab(page, "evals")
    expect(page.locator("#evals-sheet")).to_be_visible()

    page.fill("#ev-new-suite-name", "smoke suite")
    page.click("#ev-create-suite")
    expect(page.locator("h3", has_text="Cases")).to_be_visible()

    page.fill("#ev-case-name", "does something")
    page.fill("#ev-case-prompt", "make a small change")
    page.click("#ev-add-case")
    expect(page.locator(".ev-case-row", has_text="does something")).to_be_visible()

    page.click("#ev-run-suite")
    expect(page.locator("h3", has_text="Matrix")).to_be_visible(timeout=10000)

    # one case x one (default) variant x one repeat = one cell, which should
    # settle on "1/1" once the mock agent's run lands
    cell = page.locator("td.ev-cell").first
    expect(cell).to_have_text("1/1", timeout=15000)
    expect(page.locator(".ev-badge", has_text="done")).to_be_visible(timeout=15000)

    board_row = page.locator(".ev-leaderboard tbody tr").first
    expect(board_row).to_contain_text("100%")

    cell.click()
    expect(page.locator(".ev-cell-detail")).to_be_visible()
