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
    # Every page the navigation offers, including ones added later.
    pages = page.evaluate("""() => [...new Set([...document.querySelectorAll('#tabbar .tab[data-tab], [data-nav-target]')]
        .map((el) => el.dataset.tab || el.dataset.navTarget))]""")
    for name in pages:
        if name in ("board", "targets", "evals", "terminals"):
            continue
        if name == "deck" and label == "phone":
            continue
        nav(page, name)
        page.wait_for_timeout(400)
        sweep.check(name)
    nav(page, "targets")
    for section in ("machines", "projects", "notifications", "devices", "about", "budgets", "accounts", "agents", "plugins", "appearance", "workspace", "shortcuts"):
        tab = page.locator(f'[data-settings="{section}"]')
        if not tab.count():
            continue
        tab.click()
        sweep.check("settings-" + section)
        if section == "plugins":
            # The consent preview, the one screen a person must be able to read.
            page.locator(".plugin-add .segmented label", has_text="Folder on this server").click()
            page.fill("#plugin-path", str(Path(__file__).resolve().parents[1] / "examples" / "plugins" / "hello"))
            page.click("#plugin-preview-path")
            expect(page.locator("#plugin-consent")).to_be_visible()
            sweep.check("settings-plugins-consent", root="#plugin-consent")
            page.locator("#plugin-consent [data-close]").click()
            expect(page.locator("#plugin-consent")).to_have_count(0)
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


@pytest.mark.parametrize("theme", ["light", "dark"])
@pytest.mark.parametrize("page", [DESKTOP, PHONE], indirect=True, ids=["desk", "phone"])
def test_light_mode_remote_machines_sandboxes_usage_and_race(page, theme, tmp_path):
    """SSH import, a machine's connection and ports, sandboxes, usage by
    provider (with a provider past its warning) and Race N agents."""
    import sqlite3
    import time
    import urllib.request
    from conftest import _start, _stop, _unused_port
    cfg = tmp_path / "ssh_config"
    cfg.write_text("Host build-box\n  HostName 10.0.0.5\n  User dev\n  ProxyJump bastion\n  ForwardAgent yes\n")
    port, token, db = _unused_port(), "sweep-token", tmp_path / "sweep.db"
    proc = _start(port, {"LECTERN_SSH_CONFIG": str(cfg), "LECTERN_AUTH": "token", "LECTERN_AUTH_TOKEN": token,
                         "LECTERN_LIVE": "1", "LECTERN_LIVE_UNAUTHENTICATED": "1", "LECTERN_DB": str(db)})
    base = f"http://127.0.0.1:{port}"

    def api(path, body=None, method=None):
        req = urllib.request.Request(base + "/api" + path, method=method or ("POST" if body is not None else "GET"),
                                     data=None if body is None else json.dumps(body).encode(),
                                     headers={"Content-Type": "application/json", "Authorization": "Bearer " + token})
        return json.load(urllib.request.urlopen(req, timeout=10))
    try:
        api("/ssh/import", {"aliases": ["build-box"]})
        sb = api("/targets", {"name": "docker-sb", "kind": "sandbox", "sandbox": True,
                              "sandbox_config": {"provider": "docker", "image": "ghcr.io/acme/dev:1"}})
        api("/sandboxes", {"target_id": sb["id"]})
        project = api("/projects")[0]
        sess = api("/sessions", {"project_id": project["id"], "agent": "codex", "name": "sweep codex"})
        now = time.time()
        with sqlite3.connect(db, timeout=10) as conn:
            conn.execute("UPDATE sessions SET rate_5h_pct=85, rate_5h_reset=?, rate_7d_pct=40, rate_7d_reset=?, usage_at=? WHERE id=?",
                         (now + 3600, now + 86400, now, sess["id"]))
            conn.execute("INSERT INTO usage_daily(date, session_id, agent, model, cost_usd, estimated_usd, input_tokens, output_tokens) "
                         "VALUES(date('now'), ?, 'codex', 'gpt-5-codex', 1.25, 1.25, 120000, 9000)", (sess["id"],))
        page.add_init_script(f"localStorage.setItem('lec-token', {json.dumps(token)})")
        light(page, theme)
        label = "phone" if page.viewport_size["width"] < 600 else "desk"
        sweep = Sweep(page, label, theme)
        page.goto(base + "/#board")
        page.click("#fab")
        page.click('#f-race [data-race="3"]')
        page.fill("#f-prompt", "Sweep race")
        sweep.check("race-new-task")
        page.click("#f-race-go")
        expect(page.locator(".compare-view .compare-card")).to_have_count(3, timeout=15000)
        sweep.check("race-compare")
        page.keyboard.press("Escape")
        page.locator("#qb-race").click()
        sweep.check("race-quick-bar")
        nav(page, "targets")
        page.locator('[data-settings="machines"]').click()
        card = page.locator("article.rowcard").filter(has=page.locator("h3", has_text="build-box")).first
        card.locator('[data-remote-tab="connection"]').click()
        expect(card.get_by_label("ssh_config alias")).to_have_value("build-box")
        sweep.check("machine-connection")
        card.locator('[data-remote-tab="ports"]').click()
        card.locator(".remote-ports button.b").last.click()
        expect(card.locator(".remote-listening li")).to_have_count(2, timeout=10000)
        sweep.check("machine-ports")
        card.locator('[data-remote-tab="files"]').click()
        sweep.check("machine-files")
        sbcard = page.locator("article.rowcard").filter(has=page.locator("h3", has_text="docker-sb")).first
        sbcard.locator('.sandbox-machine button.linkish').click()
        expect(sbcard.locator(".sandbox-fields")).to_be_visible()
        expect(page.locator(".sandbox-card")).to_have_count(1, timeout=10000)
        sweep.check("sandboxes")
        page.click("#ssh-import")
        expect(page.locator('.ssh-hosts li[data-alias="build-box"]')).to_be_visible(timeout=10000)
        sweep.check("ssh-import")
        page.locator("dialog.ssh-import [data-close]").click()
        page.locator('[data-settings="about"]').click()
        expect(page.locator('.pu-card.warn[data-provider="codex"]')).to_be_visible(timeout=10000)
        sweep.check("usage-providers")
        sweep.done()
    finally:
        _stop(proc, port)
