"""The seven new-user walkthroughs from the usability audit
(/mnt/bulk/ux-audit/audit.md §2), replayed on the simpler web app
(docs/design/simple-ui.md) at 1440 px and 390 px. Each one counts the clicks
a person makes once they know the way, and fails if a flow grows longer again
or hits a dead end.

| Task | Before (audit) | Now |
|---|---|---|
| (a) first agent in a project, then talk | 4 + Send; Enter did not send | 3 + Enter |
| (b) see what agents are doing | status words inverted | 0; truthful words |
| (c) approve something | 4 to turn asking on, then 1 | on by default; 1 + Y |
| (d) end, then come back | 2 + a confirm dialog | 2, with Undo; Restore 1 |
| (e) a second agent on another project | ~9 + 1,800 px of scrolling | 5, no Settings |
| (f) see what changed and commit | Commit disabled on main | 4 |
| (g) connect a phone | QR code for 127.0.0.1 | 3; never loopback |

Real processes where the flow depends on them (a stub agent standing in for
the model, real git, real tmux); the mock server where it does not.
"""
import json
import re
import subprocess
import time
from pathlib import Path

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE
from test_terminal_workspace import real_terminal  # noqa: F401  (fixture)
from test_session_permission_request import STUB_AGENT as APPROVAL_AGENT
from test_light_mode_sweep import Sweep, light
from session_sheet import session_tool

WIDTHS = [DESKTOP, PHONE]
IDS = ["desk-1440", "phone-390"]

# Answers every line typed at it, like a chat agent, and logs it.
CHAT_AGENT = '''#!/bin/bash
echo "stub agent ready"
while IFS= read -r line; do
  printf 'typed:%s\\n' "$line" >> "$PWD/../agent-log.txt"
  echo "ok: $line"
done
'''


class Clicks:
    """Counts the clicks (and taps) a walkthrough takes."""

    def __init__(self):
        self.n = 0

    def __call__(self, locator):
        locator.click()
        self.n += 1


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], text=True).strip()


def wait_for_file(path, want="", limit=20):
    deadline = time.monotonic() + limit
    while time.monotonic() < deadline:
        if path.exists() and want in path.read_text():
            return path.read_text()
        time.sleep(0.1)
    raise AssertionError(f"{path} never contained {want!r}")


def project_for(t, name="myapp"):
    """What `lectern up` does in a repository: register it as a project."""
    return t["api"]("/projects", {"name": name, "target_id": t["target_id"], "repo_path": str(t["root"])})


# ---- (a) + (b) ---------------------------------------------------------------


@pytest.mark.parametrize("real_terminal", [{"agent_script": CHAT_AGENT}], indirect=True)
@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_a_start_an_agent_and_talk_to_it(page, real_terminal, size):
    t = real_terminal
    project_for(t)
    page.set_viewport_size(size)
    click = Clicks()
    page.goto(t["url"])
    # Home is Sessions, on every device.
    expect(page.locator('.tab[data-tab="sessions"]')).to_have_class("tab on")

    click(page.locator("#sess-new"))
    sheet = page.get_by_role("dialog", name="Start an agent", exact=True)
    expect(sheet.locator("#ns-agent")).to_have_value("claude")
    with page.expect_response(lambda r: r.request.method == "POST" and r.url.endswith("/api/sessions")) as created:
        click(sheet.locator("#ns-go"))
    session = created.value.json()
    card = page.locator(f'.scard[data-session-id="{session["id"]}"]')
    # (b) What it is doing, in the words every surface uses: never "Needs you"
    # for an agent that is only working or waiting at its prompt.
    expect(card.locator(".status-badge")).to_have_text(re.compile("^(Working.*|Idle)$"), timeout=20000)
    expect(card.locator('.status-badge[data-state="needs_you"]')).to_have_count(0)

    click(card.get_by_role("button", name="Chat", exact=True))
    box = page.locator("#conversation-input")
    box.fill("hello agent")
    if size is PHONE:
        # A touch screen keeps Enter for new lines; Send sends.
        click(page.get_by_role("button", name="Send", exact=True))
    else:
        box.press("Enter")
    wait_for_file(Path(t["root"]).parent / "agent-log.txt", "typed:hello agent")
    assert click.n <= (4 if size is PHONE else 3), click.n


def test_b_status_words_are_truthful_on_the_home_page(page, server):
    page.request.post(server + "/api/sessions", data={"name": "Quiet agent", "scratch": True, "agent": "claude"})
    for size in WIDTHS:
        page.set_viewport_size(size)
        page.goto(server)
        card = page.locator(".scard", has_text="Quiet agent")
        # An agent at its prompt is idle; only an approval makes "Needs you".
        expect(card.locator(".status-badge")).to_have_text("Idle", timeout=25000)
        expect(page.locator('.status-badge[data-state="needs_you"]')).to_have_count(0)


# ---- (c) -------------------------------------------------------------------


@pytest.mark.parametrize("real_terminal", [{"agent_script": APPROVAL_AGENT}], indirect=True)
@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_c_approve_from_the_approvals_page(page, real_terminal, size):
    t = real_terminal
    # A new install asks before risky actions: no switch to find first.
    assert t["api"]("/settings")["session_permission_mode"] == "ask"
    session = t["api"]("/sessions", {"target_id": t["target_id"], "workdir": str(t["root"]), "agent": "claude", "name": "Careful"})
    assert session["permission_mode"] == "ask"
    page.set_viewport_size(size)
    click = Clicks()
    page.goto(t["url"])
    # The main navigation carries the count, and the card says why.
    expect(page.locator("#appr-badge")).to_have_text("1", timeout=25000)
    card = page.locator(f'.scard[data-session-id="{session["id"]}"]')
    expect(card.locator(".status-badge")).to_have_text("Needs you")
    expect(card.locator(".approval-card")).to_contain_text("rm -rf important")

    click(page.locator('.tab[data-tab="approvals"]'))
    approval = page.locator("#approvals-page .approval-card")
    expect(approval).to_contain_text("Careful")
    for name in ("Allow once", "Allow for this session", "Deny…"):
        expect(approval.get_by_role("button", name=name, exact=True)).to_be_visible()
    if size is PHONE:
        click(approval.get_by_role("button", name="Allow once", exact=True))
    else:
        # The first card has focus; its key is shown on the button.
        expect(approval).to_be_focused()
        page.keyboard.press("y")
    expect(page.locator("#approvals-page .approval-card")).to_have_count(0, timeout=20000)
    decision = json.loads(wait_for_file(Path(t["root"]) / "hook-response.json"))
    assert decision["hookSpecificOutput"]["decision"]["behavior"] == "allow"
    assert click.n <= (2 if size is PHONE else 1), click.n


# ---- (d) -------------------------------------------------------------------


@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_d_end_offers_undo_and_restore_finds_it(page, server, size):
    name = f"Leaving now {size['width']}"
    page.request.post(server + "/api/sessions", data={"name": name, "scratch": True, "agent": "claude"})
    page.set_viewport_size(size)
    dialogs = []
    page.on("dialog", lambda d: (dialogs.append(d.message), d.dismiss()))
    click = Clicks()
    page.goto(server + "/#sessions")
    expect(page.locator(".scard", has_text=name)).to_be_visible(timeout=20000)
    click(page.locator(f'summary[aria-label="More actions for {name}"]'))
    click(page.get_by_role("button", name="End", exact=True))
    # No confirmation dialog: Undo is the safety net.
    expect(page.locator("#toasts .toast-action")).to_be_visible()
    assert not dialogs, dialogs
    expect(page.locator(".scard", has_text=name)).to_have_count(0, timeout=20000)
    assert click.n == 2, click.n
    click(page.locator("#sess-recent"))
    expect(page.locator(".recent-closed")).to_contain_text(name, timeout=20000)


# ---- (e) -------------------------------------------------------------------


@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_e_second_agent_in_another_folder(page, server, size):
    page.set_viewport_size(size)
    click = Clicks()
    page.goto(server)
    click(page.locator("#sess-new"))
    sheet = page.get_by_role("dialog", name="Start an agent", exact=True)
    click(sheet.locator("#ns-browse"))
    picker = page.get_by_role("dialog", name="Choose a folder", exact=True)
    expect(picker.locator("#folder-path")).to_have_text("~")
    click(picker.locator('.folder-row[data-folder="website"]'))
    expect(picker.locator("#folder-path")).to_have_text("~/website")
    click(picker.locator("#folder-use"))
    expect(picker).to_have_count(0)
    with page.expect_response(lambda r: r.request.method == "POST" and r.url.endswith("/api/sessions")) as created:
        click(sheet.locator("#ns-go"))
    session = created.value.json()
    # Starting in the folder made it a project; nobody visited Settings.
    projects = page.request.get(server + "/api/projects").json()
    website = next(p for p in projects if p["repo_path"] == "/mock/home/website")
    assert session["project_id"] == website["id"], (session, website)
    expect(page.locator(f'.scard[data-session-id="{session["id"]}"]')).to_contain_text("website", timeout=20000)
    assert click.n == 5, click.n
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")


# ---- (f) -------------------------------------------------------------------


@pytest.mark.parametrize("real_terminal", [{"agent_script": CHAT_AGENT}], indirect=True)
@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_f_commit_from_main_on_a_new_branch(page, real_terminal, size):
    t = real_terminal
    root = Path(t["root"])
    git(root, "config", "user.name", "Test")
    git(root, "config", "user.email", "test@example.com")
    git(root, "add", ".")
    git(root, "commit", "-qm", "base")
    base = git(root, "rev-parse", "--abbrev-ref", "HEAD")
    project = t["api"]("/projects", {"name": "myapp", "target_id": t["target_id"], "repo_path": str(root), "default_base_branch": base})
    t["api"]("/sessions", {"project_id": project["id"], "agent": "claude", "name": "Fix login"})
    (root / "NOTES.md").write_text("what the agent changed\n")

    page.route("**/api/sessions/*/git/commit-message", lambda route: route.fulfill(
        json={"message": "Add project notes", "agent": "stub", "model": "test"}))
    page.set_viewport_size(size)
    click = Clicks()
    page.goto(t["url"] + "/#sessions")
    expect(page.locator(".scard", has_text="Fix login")).to_be_visible(timeout=20000)
    click(page.locator('summary[aria-label="More actions for Fix login"]'))
    click(page.get_by_role("button", name="Review & merge"))
    review = page.locator("#session-review")
    click(review.get_by_role("tab", name="Commit"))
    # On main there is a choice, never a silently disabled button.
    expect(review.locator(".commit-on-main")).to_contain_text(f"works directly on {base}")
    # It is the person's own folder: say so before switching it to a branch.
    expect(review.locator(".commit-own-folder")).to_contain_text(f"creates branch lectern/fix-login in your folder {root}")
    push = review.get_by_role("checkbox", name="Push to origin")
    expect(push).to_be_disabled()
    expect(review).to_contain_text("no remote to push to")
    # The message is drafted from what changed, not from the session's name.
    expect(review.locator(".commit-message")).to_have_value("Add project notes")
    commit = review.get_by_role("button", name="Commit on lectern/fix-login", exact=True)
    expect(commit).to_be_enabled()
    click(commit)
    expect(page.locator("#toasts")).to_contain_text(f"Your folder {root} is now on branch lectern/fix-login", timeout=20000)
    assert git(root, "rev-parse", "--abbrev-ref", "HEAD") == "lectern/fix-login"
    assert git(root, "log", "-1", "--format=%s", base) == "base"
    assert "NOTES.md" in git(root, "show", "--name-only", "--format=", "lectern/fix-login")
    assert git(root, "log", "-1", "--format=%s") == "Add project notes"
    assert click.n == 4, click.n



@pytest.mark.parametrize("real_terminal", [{"agent_script": CHAT_AGENT}], indirect=True)
@pytest.mark.parametrize("theme", ["light", "dark"])
@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_f_commit_without_a_git_identity_moves_nothing_and_says_why(page, real_terminal, size, theme):
    """A fresh machine has no git name and email. Committing from the person's
    own folder must say so before it switches that folder to a new branch, and
    the round-two controls (the Sessions ⋯ menu, inline rename, the own-folder
    note, this message) must read in light and dark."""
    t = real_terminal
    root = Path(t["root"])
    git(root, "add", ".")
    git(root, "-c", "user.name=Test", "-c", "user.email=test@example.com", "commit", "-qm", "base")
    for key, value in (("user.useConfigOnly", "true"), ("user.name", ""), ("user.email", "")):
        git(root, "config", key, value)
    base = git(root, "rev-parse", "--abbrev-ref", "HEAD")
    project = t["api"]("/projects", {"name": "myapp", "target_id": t["target_id"], "repo_path": str(root), "default_base_branch": base})
    t["api"]("/sessions", {"project_id": project["id"], "agent": "claude", "name": "Fix login"})
    (root / "NOTES.md").write_text("what the agent changed\n")

    light(page, theme)
    page.set_viewport_size(size)
    sweep = Sweep(page, f"round2-{size['width']}", theme)
    page.goto(t["url"] + "/#sessions")
    card = page.locator(".scard", has_text="Fix login")
    expect(card).to_be_visible(timeout=20000)
    session_tool(page, "#sess-search")
    sweep.check("sessions-more-menu")
    page.locator("#sess-more > summary").click()
    sid = card.get_attribute("data-session-id")
    card = page.locator(f'.scard[data-session-id="{sid}"]')
    card.locator("button.nm").click()
    expect(card.locator(".nm-edit")).to_be_focused()
    sweep.check("inline-rename")
    card.locator(".nm-edit").press("Escape")

    card.locator(".action-menu > summary").first.click()
    page.get_by_role("button", name="Review & merge").click()
    review = page.locator("#session-review")
    review.get_by_role("tab", name="Commit").click()
    expect(review.locator(".commit-own-folder")).to_contain_text("in your folder")
    sweep.check("own-folder-note", root="#session-review")
    review.get_by_role("button", name="Commit on lectern/fix-login", exact=True).click()
    note = review.locator(".commit-no-identity")
    expect(note).to_contain_text("doesn't know your name and email", timeout=20000)
    expect(note).to_contain_text("Nothing was changed")
    expect(note.locator("pre")).to_contain_text("git config --global user.email")
    # Sample the resting state, not the button still under the pointer.
    page.mouse.move(0, 0)
    sweep.check("no-git-identity", root="#session-review")
    # Nothing moved: the folder is on its branch, no new branch exists.
    assert git(root, "rev-parse", "--abbrev-ref", "HEAD") == base
    assert git(root, "branch", "--list", "lectern/fix-login") == ""
    assert page.evaluate("document.documentElement.scrollWidth <= innerWidth")
    sweep.done()

# ---- (g) -------------------------------------------------------------------


@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_g_connect_a_phone_never_shows_a_loopback_qr(page, server, size):
    page.set_viewport_size(size)
    click = Clicks()
    page.goto(server)
    click(page.locator('.tab[data-tab="settings"]'))
    click(page.locator("#basics-phone"))
    wizard = page.get_by_role("dialog", name="Connect your phone", exact=True)
    expect(wizard.locator(".phone-choice")).to_have_count(3)
    # Whatever this machine offers, a choice that cannot work says why, and
    # the QR button explains itself instead of silently doing nothing.
    for choice in wizard.locator('.phone-choice[data-available="false"]').all():
        expect(choice.locator(".phone-choice-why")).not_to_be_empty()
    if not wizard.locator('.phone-choice[data-available="true"]').count():
        expect(wizard.locator("#phone-show-qr")).to_be_disabled()
        expect(wizard.locator("#phone-show-why")).to_be_visible()

    # With a reachable address on offer, three clicks reach a QR code for it.
    page.route("**/api/phone/addresses", lambda route: route.fulfill(status=200, content_type="application/json", body=json.dumps({
        "listening": "0.0.0.0:9110", "loopback_only": False, "options": [
            {"kind": "tailnet", "available": False, "reason": "tailscale_off"},
            {"kind": "lan", "url": "http://192.168.0.75:9110", "available": True},
            {"kind": "relay", "available": False, "reason": "relay_not_set_up"},
        ]})))
    wizard.locator("[data-close]").click()
    page.locator("#basics-phone").click()
    wizard = page.get_by_role("dialog", name="Connect your phone", exact=True)
    expect(wizard.locator('.phone-choice[data-kind="lan"] input')).to_be_checked()
    click(wizard.locator("#phone-show-qr"))
    qr = wizard.locator('[data-testid="phone-qr"]')
    expect(qr).to_be_visible()
    url = qr.get_attribute("data-url")
    assert url.startswith("http://192.168.0.75:9110/pair#code="), url
    assert "127.0.0.1" not in url and "localhost" not in url
    assert click.n == 3, click.n
    box = wizard.bounding_box()
    assert box["x"] >= 0 and box["x"] + box["width"] <= size["width"] + 1, box


# ---- the `lectern up` landing link --------------------------------------------


@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_landing_link_opens_start_an_agent_on_the_up_project(browser, server, size):
    """`lectern up` opens #sessions/new/<project>: Start an agent, already on
    the folder it ran in — never a blank room because the project list was
    still loading when the sheet opened."""
    projects = [p for p in json.loads(__import__("urllib.request").request.urlopen(server + "/api/projects").read())]
    wanted = projects[-1]["id"]
    for landing, want in ((f"/#sessions/new/{wanted}", wanted), ("/#sessions/new", projects[0]["id"])):
        with browser.new_context(viewport=size) as context:
            page = context.new_page()
            page.goto(server + landing)
            sheet = page.get_by_role("dialog", name="Start an agent", exact=True)
            expect(sheet).to_be_visible()
            expect(sheet.locator("#ns-project")).to_have_value(str(want))



# ---- round two (re-audit /mnt/bulk/ux-audit/after/reaudit.md) ----------------


@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_only_installed_agents_start_and_the_demo_stands_in(page, server, size):
    """N2: a missing agent is never preselected; with none installed the demo
    is, with a one-line hint, and Start works."""
    page.set_viewport_size(size)
    page.route("**/api/targets/*/agents", lambda route: route.fulfill(status=200, content_type="application/json", body=json.dumps([
        {"name": n, "state": "missing"} for n in ("claude", "codex", "gemini")])))
    page.goto(server + "/#sessions")
    page.locator("#sess-new").click()
    sheet = page.get_by_role("dialog", name="Start an agent", exact=True)
    expect(sheet.locator("#ns-agent")).to_have_value("demo")
    expect(sheet.locator("#ns-no-agent")).to_contain_text("Install Claude Code or Codex")
    enabled = sheet.locator("#ns-agent option:not([disabled])").all_text_contents()
    assert not any("not installed" in o for o in enabled), enabled
    expect(sheet.locator("#ns-go")).to_be_enabled()


@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_chat_keeps_the_last_line_in_view(page, server, size):
    """N6: the screen's blank rows below the cursor never push the output away."""
    created = page.request.post(server + "/api/sessions", data={"name": f"Tall screen {size['width']}", "scratch": True, "agent": "claude"}).json()
    page.set_viewport_size(size)

    def reader(route):
        body = route.fetch().json()
        body["text"] = "\n".join(f"line {i}" for i in range(60)) + "\nLAST REAL LINE" + "\n" * 80
        route.fulfill(status=200, content_type="application/json", body=json.dumps(body))
    page.route(f"**/api/sessions/{created['id']}/reader", reader)
    page.goto(server + "/#sessions")
    page.locator(f'.scard[data-session-id="{created["id"]}"]').get_by_role("button", name="Chat", exact=True).click()
    log = page.locator("#conversation-log")
    expect(log).to_contain_text("LAST REAL LINE", timeout=15000)
    page.wait_for_timeout(500)
    # The output ends at its last real line, and the view sits at the bottom of it.
    assert log.locator("pre.session-reader").evaluate("el => el.textContent.endsWith('LAST REAL LINE')")
    assert log.evaluate("el => el.scrollHeight - el.clientHeight - el.scrollTop <= 2")


@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_navigation_is_short_and_more_stays_in_its_place(page, real_terminal, size):
    """N9: on a phone, Sessions, Approvals, Settings (+ Tasks once used) and
    More, and an open terminal never takes a slot. A desktop sidebar shows every
    page directly and has no More to open over the terminal."""
    t = real_terminal
    page.set_viewport_size(size)
    page.goto(t["url"] + "/#sessions")
    tabs = page.locator("#tabbar > .tab")
    pages = 3 if size is PHONE else 11
    expect(tabs).to_have_count(pages)
    page.locator(".scard", has_text="Real terminal").get_by_role("button", name="⌨ Terminal", exact=True).click()
    expect(page.locator("#terminal-workspace")).to_be_visible()
    if size is PHONE:
        show = page.get_by_role("button", name="Show navigation", exact=True)
        if show.is_visible():
            show.click()
    expect(tabs).to_have_count(pages)
    if size is PHONE:
        expect(page.locator("#more-terminal-badge")).to_have_text("1")
        page.locator("#nav-overflow > summary").click()
        panel = page.locator("#nav-overflow > .action-menu-panel")
        expect(panel).to_be_visible()
        page.locator('#tabbar .tab[data-tab="settings"]').click()
        expect(panel).not_to_be_visible()
    else:
        expect(page.locator("#nav-overflow")).to_have_count(0)
        expect(page.locator("#terminal-badge")).to_have_text("1")
        page.locator('#tabbar .tab[data-tab="settings"]').click()
    # Sessions offers the way back to the open terminal.
    page.locator('#tabbar .tab[data-tab="sessions"]').click()
    expect(page.locator("#sess-terminals")).to_contain_text("Terminals (1)")


@pytest.mark.parametrize("size", WIDTHS, ids=IDS)
def test_a_short_sessions_page_is_calm(page, real_terminal, size):
    """N8: with one or two sessions the search and filters wait under ⋯, and a
    card shows at most three actions; its name renames in place."""
    t = real_terminal
    page.set_viewport_size(size)
    page.goto(t["url"] + "/#sessions")
    card = page.locator(f'.scard[data-session-id="{t["id"]}"]')
    expect(card).to_be_visible(timeout=15000)
    for hidden in ("#sess-search", "#sess-grouping", "#sess-scope", "#sess-saved-search", "#sess-discover"):
        expect(page.locator(hidden)).not_to_be_visible()
    page.locator("#sess-more > summary").click()
    expect(page.locator("#sess-scope")).to_be_visible()
    page.locator("#sess-more > summary").click()
    actions = card.locator(".btnrow > button:visible, .btnrow > .action-menu > summary:visible")
    assert actions.count() <= 3, actions.all_text_contents()
    expect(card.locator(".session-memory")).to_have_count(0)
    card.locator("button.nm").click()
    card.locator(".nm-edit").fill(f"My terminal {size['width']}")
    card.locator(".nm-edit").press("Enter")
    expect(card.locator("button.nm")).to_have_text(f"My terminal {size['width']}")
