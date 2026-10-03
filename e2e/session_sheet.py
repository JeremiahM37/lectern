"""Shared helpers for the New session sheet.

The sheet leads with project and agent; the rarer settings (name, group, launch
profile, model, worktree, start mode, yolo, first message) live behind one
native "Advanced options" disclosure. Tests that need one of those settings ask
for it the way a person would, rather than expecting the sheet to unfold itself.
"""
from playwright.sync_api import expect


def open_advanced(root):
    """Expand the Advanced options group inside the New session sheet.

    ``root`` is the page or the sheet locator; the call is idempotent so a test
    that reopens the sheet (or runs on both phone and desktop) can call it
    unconditionally.
    """
    details = root.locator("#ns-advanced")
    expect(details).to_be_visible()
    if not details.evaluate("el => el.open"):
        details.locator("summary").click()
    expect(details).to_have_js_property("open", True)
    return details


def nav_to(page, name):
    """Open a page from the navigation: the main bar, or More (docs/design/
    simple-ui.md — Terminals always, and Tasks until it is used, live there)."""
    tab = page.locator(f'#tabbar .tab[data-tab="{name}"]')
    if tab.count() and tab.is_visible():
        tab.click()
        return
    # More, on a narrow screen; a desktop sidebar lists every page.
    if page.locator("#nav-overflow").count():
        page.locator("#nav-overflow > summary").click()
    page.locator(f'#tabbar [data-nav-target="{name}"]:visible').click()


def session_tool(page, selector):
    """A Sessions-page tool (search, Group by, Show, Search saved, Find running).
    With only a couple of sessions they wait under the header's ⋯ menu."""
    tool = page.locator(selector)
    more = page.locator("#sess-more")
    if more.count() and not tool.is_visible():
        if not more.evaluate("el => el.open"):
            more.locator("summary").click()
    return tool


def card_action(page, card, name, exact=True):
    """A session card action that lives in its ⋯ menu (Rename, Change agent, Run tests, Notes)."""
    button = card.get_by_role("button", name=name, exact=exact)
    if not button.is_visible():
        card.locator(".action-menu > summary").first.click()
    return button
