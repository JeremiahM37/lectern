"""Light mode (and dark), every screen, desk and phone: each screen is opened and every
visible piece of text is checked for WCAG AA contrast against what it is
actually drawn on (contrast.py).

To keep the screenshots of a sweep, create e2e/.sweep-shots before running
through the isolated runner; each screen is then printed as a SHOT line that
e2e/doc_screenshots_workspace.py can extract.
"""
import base64
import json
from pathlib import Path

import pytest
from playwright.sync_api import expect
from conftest import DESKTOP, PHONE
from contrast import audit
from test_terminal_workspace import real_terminal  # noqa: F401
from test_terminal_split import attach, frame, ready

def appearance(theme):
    return {"appearance": {"theme": theme, "accent": "", "zoom": 1, "language": ""}}
SHOTS = (Path(__file__).parent / ".sweep-shots").exists()


def light(page, theme="light"):
    # Marked unsent, so the app keeps it and saves it rather than taking the
    # server's (empty) preferences over it.
    page.add_init_script(f"localStorage.setItem('lec-ui-prefs-v1', {json.dumps(json.dumps(appearance(theme)))});"
                         "localStorage.setItem('lec-ui-prefs-unsent-v1', JSON.stringify({appearance: true}))")


class Sweep:
    def __init__(self, page, label, theme="light"):
        self.page, self.label, self.theme, self.failures = page, label + "-" + theme, theme, []

    def check(self, name, target=None, root="body"):
        page = self.page
        page.wait_for_timeout(250)
        expect(page.locator("html")).to_have_attribute("data-theme", self.theme)
        found = audit(target or page, root)
        self.failures += [f"[{self.label} {name}] {row}" for row in found]
        if SHOTS:
            data = page.screenshot()
            print(f"\nSHOT {self.label}-{name}.png {base64.b64encode(data).decode()}", flush=True)

    def done(self):
        assert not self.failures, "\n".join(self.failures)


def nav(page, name):
    if page.get_by_role("button", name="Show navigation", exact=True).is_visible():
        page.get_by_role("button", name="Show navigation", exact=True).click()
    button = page.locator(f'.tab[data-tab="{name}"]')
    if button.is_visible():
        button.click()
    else:
        page.locator("#nav-overflow > summary").click()
        page.locator(f'[data-nav-target="{name}"]').click()


@pytest.mark.parametrize("theme", ["light", "dark"])
@pytest.mark.parametrize("page", [DESKTOP, PHONE], indirect=True, ids=["desk", "phone"])
def test_light_mode_board_settings_and_dialogs(page, server, theme):
    light(page, theme)
    label = "phone" if page.viewport_size["width"] < 600 else "desk"
    sweep = Sweep(page, label, theme)
    page.goto(server + "/#board")
    # A task that reaches review, and one waiting on an approval, so the board,
    # the task sheet and the approvals list all have content.
    page.click("#fab")
    sweep.check("new-task")
    page.fill("#f-title", "Light sweep task")
    page.fill("#f-prompt", "add health endpoint")
    page.click("#f-go")
    expect(page.locator(".card", has_text="Light sweep task").first).to_be_visible(timeout=20000)
    page.click("#fab")
    page.fill("#f-title", "Light sweep gated")
    page.fill("#f-prompt", "deploy [mock:approval]")
    page.select_option("#f-perm", "default")
    page.click("#f-go")
    page.wait_for_timeout(3000)
    sweep.check("board")
    page.locator(".card", has_text="Light sweep task").first.click()
    expect(page.locator("#sheet")).to_be_visible()
    sweep.check("task-detail")
    diff = page.locator("#actions button:has-text('Diff')")
    if diff.count():
        diff.first.click()
        page.wait_for_timeout(500)
        sweep.check("task-diff")
    page.keyboard.press("Escape")
    for name in ("sessions", "media", "approvals"):
        nav(page, name)
        sweep.check(name)
    if label == "desk":
        nav(page, "deck")
        sweep.check("deck")
    nav(page, "targets")
    for section in ("machines", "projects", "notifications", "devices", "about", "budgets", "accounts", "agents", "appearance", "workspace", "shortcuts"):
        tab = page.locator(f'[data-settings="{section}"]')
        if not tab.count():
            continue
        tab.click()
        sweep.check("settings-" + section)
    page.get_by_role("combobox", name="Search settings").fill("theme")
    sweep.check("settings-search")
    page.get_by_role("combobox", name="Search settings").fill("")
    page.keyboard.press("Control+k")
    page.get_by_role("combobox", name="Search sessions, tasks, and actions").fill("new")
    sweep.check("palette")
    page.keyboard.press("Escape")
    nav(page, "sessions")
    new = page.get_by_role("button", name="New session")
    if new.count():
        new.first.click()
        page.wait_for_timeout(400)
        sweep.check("new-session")
        page.keyboard.press("Escape")
    page.goto(server + "/#evals")
    page.wait_for_timeout(600)
    sweep.check("agent-tests")
    sweep.done()


@pytest.mark.parametrize("theme", ["light", "dark"])
@pytest.mark.parametrize("page", [DESKTOP, PHONE], indirect=True, ids=["desk", "phone"])
def test_light_mode_workspace_terminal_and_chat(page, real_terminal, theme):
    t = real_terminal
    light(page, theme)
    label = "phone" if page.viewport_size["width"] < 600 else "desk"
    sweep = Sweep(page, label, theme)
    (t["root"] / "notes.md").write_text("# Notes\n")
    page.goto(t["url"] + "/#sessions")
    sweep.check("sessions-live")
    card = page.locator(".scard", has_text="Real terminal")
    card.get_by_role("button", name="Chat", exact=True).click()
    expect(page.locator("dialog.conversation")).to_be_visible()
    sweep.check("conversation")
    page.keyboard.press("Escape")
    attach(page, "Real terminal"); f = frame(page, t["id"]); ready(f)
    f.locator("#agent-terminal").click()
    page.keyboard.type("echo light-sweep"); page.keyboard.press("Enter")
    sweep.check("workspace")
    sweep.check("terminal-page", f, "body")
    f.locator("#terminal-tools-summary").click()
    sweep.check("terminal-tools", f, "body")
    for button, dialog in (("Appearance", "#settings-dialog"), ("Terminal connection setup", "dialog"), ("Session history", "dialog")):
        if f.locator("#terminal-tools").get_attribute("open") is None:
            f.locator("#terminal-tools-summary").click()
        f.get_by_role("button", name=button).click()
        expect(f.locator(dialog).first).to_be_visible()
        sweep.check("terminal-" + button.lower().replace(" ", "-"), f, "body")
        f.locator("dialog[open] [data-close], dialog[open] button:has-text('Close')").first.click()
    f.locator("#agent-terminal").click()
    page.keyboard.press("Control+f")
    f.locator("#search-input").fill("light")
    sweep.check("terminal-find", f, "body")
    page.keyboard.press("Escape")
    if label == "phone":
        f.locator('[data-terminal-key="snippets"]').click()
        sweep.check("terminal-snippets", f, "body")
        f.locator("#snippets-dialog [data-close]").click()
        page.get_by_role("button", name="Show navigation").click()
    else:
        page.locator(".terminal-actions > summary").click()
        page.get_by_role("menuitem", name="Open changes beside").click()
        expect(page.locator('.terminal-tabpanel[data-pane-kind="diff"]')).to_be_visible()
        page.get_by_role("tab", name="Real terminal", exact=True).click()
        page.locator(".terminal-actions > summary").click()
        page.get_by_role("menuitem", name="Open chat beside").click()
        expect(page.locator('.terminal-tabpanel[data-pane-kind="chat"]')).to_be_visible()
        sweep.check("workspace-split")
    page.keyboard.press("Control+Backquote")
    expect(page.locator("#floating-terminal")).to_be_visible(timeout=15000)
    sweep.check("floating")
    sweep.done()
