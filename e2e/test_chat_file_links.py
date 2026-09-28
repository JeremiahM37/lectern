"""Paths and links in agent messages in the session Chat (docs/files.md):
workspace files open in the viewer, files outside the workspace read-only,
web addresses in a new tab, and a bare name only when the workspace has it."""
import json

import pytest

from playwright.sync_api import expect

from test_terminal_file_links import OWNER_PDF, owner_files  # noqa: F401
from test_terminal_workspace import real_terminal  # noqa: F401

ITEMS = [
    {"id": "1-0", "role": "user", "kind": "text", "text": "Where are the reports?"},
    {"id": "2-0", "role": "assistant", "kind": "text", "text":
        "Both are ready for review:\n\n"
        f"- [Cerebras résumé (PDF)]({OWNER_PDF})\n"
        "- Notes in `hello.txt`, not in missing-notes.md\n"
        "- Docs at https://example.com/docs/page"},
]


@pytest.mark.parametrize("theme", ["light", "dark"])
def test_chat_paths_open_like_terminal_links(page, real_terminal, owner_files, theme):
    from test_light_mode_sweep import Sweep, light
    t = real_terminal
    light(page, theme)
    sweep = Sweep(page, 'desk-chat-links', theme)
    page.context.route(
        "**/api/sessions/*/conversation/live*",
        lambda route: route.fulfill(status=200, content_type="application/json",
                                    body=json.dumps({"conversation_id": "fixture", "items": ITEMS, "cursor": 9, "truncated": False})))
    page.goto(t['url'] + '/#sessions')
    card = page.locator('.scard').first
    card.get_by_role('button', name='Chat', exact=True).click()
    log = page.locator('#conversation-cards')
    expect(log).to_contain_text('Both are ready for review', timeout=15000)
    pdf = log.locator(f'a.md-file-link[data-path="{OWNER_PDF}"]')
    expect(pdf).to_have_text('Cerebras résumé (PDF)')
    sweep.check('chat-links')
    # Hover underlines it.
    pdf.hover()
    assert pdf.evaluate("a => getComputedStyle(a).textDecorationLine") == 'underline'
    # A bare name is checked when the pointer reaches it: one the workspace
    # has becomes a link, one it lacks goes back to plain text.
    hello = log.locator('a.md-file-link[data-path="hello.txt"]')
    hello.hover()
    expect(hello).not_to_have_class('md-file-link unverified')
    missing = log.locator('a[data-path="missing-notes.md"]')
    expect(missing).to_have_class('md-file-link unverified')
    missing.hover()
    expect(missing).to_have_count(0)
    expect(log).to_contain_text('missing-notes.md')
    # Outside the workspace: the file opens read-only.
    with page.context.expect_page() as opened:
        pdf.click()
    viewer = opened.value
    expect(viewer.locator('#pdf-page')).to_have_text('Page 1 of 1', timeout=20000)
    expect(viewer.locator('.wb-doc-state')).to_have_text('Outside workspace · read-only')
    viewer.close()
    # A workspace file opens in the viewer.
    with page.context.expect_page() as opened:
        hello.click()
    expect(opened.value.locator('.wb-doc-path')).to_have_text('hello.txt', timeout=20000)
    opened.value.close()
    # A web address is an ordinary link to a new tab.
    web = log.locator('a.md-file-link[href="https://example.com/docs/page"]')
    expect(web).to_have_attribute('target', '_blank')
    sweep.done()
