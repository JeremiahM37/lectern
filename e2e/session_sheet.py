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
