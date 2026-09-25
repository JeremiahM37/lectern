"""Replay evals ("New replay suite from merged PRs", docs/replay-evals.md)
end to end against the mock target. The mock target has no `gh`/git merge
data scripted (internal/executor/mock.go), so the import pipeline correctly
degrades to "0 candidates" rather than erroring — the same contract
internal/api/replay_validation_test.go pins down at the API layer. That
makes this test a genuine UI-wiring smoke test (panel opens, request fires,
response renders, Create is disabled with nothing to import) rather than a
duplicate of the real-PR-building coverage in internal/api/replay_real_test.go
and internal/replay's own unit tests, which is where that logic actually lives.
"""
from playwright.sync_api import expect

from test_ui import _tab


def test_replay_import_panel_previews_against_the_mock_target(page, server):
    page.goto(server)
    _tab(page, "evals")
    expect(page.locator("#evals-sheet")).to_be_visible()

    page.click("#ev-replay-import-open")
    expect(page.locator(".ev-replay-import")).to_be_visible()

    page.fill("#ev-replay-n", "5")
    page.click("#ev-replay-preview")

    expect(page.locator("text=0 of 0 PRs would be imported")).to_be_visible(timeout=10000)
    expect(page.locator("text=No merged PRs found for this project.")).to_be_visible()
    expect(page.locator("#ev-replay-create")).to_be_disabled()

    # closing and reopening the panel does not leave stale state behind
    page.click("#ev-replay-import-open")
    expect(page.locator(".ev-replay-import")).to_be_hidden()
