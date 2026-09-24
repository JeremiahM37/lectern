"""The simplified New session sheet and the per-device project memory.

The sheet opens with only Project and Agent; name, group, launch profile,
model, worktree, start mode, yolo and the first message live behind one
"Advanced options" disclosure. These tests are the browser's account of that
split: what a person sees first, that the summary still says what Start will
do, that a setting behind the disclosure really reaches the API, and that the
sheet and the terminal picker remember a project the way the brief promises.
"""
import time

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE
from session_sheet import open_advanced
from test_terminal_workspace import real_terminal


def _projects(page, server):
    return page.request.get(f"{server}/api/projects").json()


@pytest.mark.parametrize(
    "size",
    [{"width": 320, "height": 700}, PHONE, DESKTOP],
    ids=["small-phone", "phone", "desktop"],
)
def test_new_session_leads_with_project_and_agent(page, server, size):
    """Project, agent and one visible Start button; everything else folded."""
    page.set_viewport_size(size)
    projects = _projects(page, server)
    assert projects, "the mock server seeds projects"
    page.goto(server + "/#sessions")
    page.click("#sess-new")
    sheet = page.get_by_role("dialog", name="New session", exact=True)
    expect(sheet).to_be_visible()

    # Only the two choices and the action are visible at first.
    for sel in ("#ns-project", "#ns-agent", "#ns-go"):
        expect(sheet.locator(sel)).to_be_visible()
    for sel in ("#ns-name", "#ns-group", "#ns-profile", "#ns-model", "#ns-yolo", "#ns-prime"):
        expect(sheet.locator(sel)).not_to_be_visible()

    # The chosen project's folder and machine are spelled out right there.
    selected = sheet.locator("#ns-project").input_value()
    chosen = next(row for row in projects if str(row["id"]) == selected)
    expect(sheet.locator("#ns-proj-hint")).to_contain_text(chosen["repo_path"])

    # Measure after the bottom-sheet entrance animation has settled.
    sheet.evaluate("el => Promise.all(el.getAnimations().map(a => a.finished))")
    # Start is reachable without scrolling at every width the brief names.
    box = sheet.locator("#ns-go").bounding_box()
    assert box and box["y"] >= 0 and box["y"] + box["height"] <= size["height"] + 1, box

    # The default launch is a bypass run, and the collapsed sheet says so.
    expect(sheet.locator("#ns-launch-summary")).to_contain_text("no approval prompts")

    # The disclosure reveals the rest without a second dialog.
    open_advanced(sheet)
    expect(sheet.locator("#ns-name")).to_be_visible()
    expect(sheet.get_by_role("dialog", name="Launch profiles")).to_have_count(0)

    sheet.locator("#ns-yolo").uncheck()
    expect(sheet.locator("#ns-launch-summary")).to_contain_text("Asks before it acts")


@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True, ids=["phone", "desktop"])
def test_advanced_values_are_what_gets_sent(page, server):
    """Name, group and the permission mode chosen under Advanced reach the API."""
    projects = _projects(page, server)
    page.goto(server + "/#sessions")
    page.click("#sess-new")
    sheet = page.get_by_role("dialog", name="New session", exact=True)
    chosen = sheet.locator("#ns-project").input_value()
    open_advanced(sheet)
    sheet.locator("#ns-name").fill("Simplified launch")
    sheet.locator("#ns-group").fill("followup")
    sheet.locator("#ns-yolo").uncheck()
    with page.expect_response(
        lambda r: r.request.method == "POST" and r.url.endswith("/api/sessions")
    ) as response:
        sheet.locator("#ns-go").click()
    body = response.value.request.post_data_json
    assert body["name"] == "Simplified launch", body
    assert body["group_path"] == "followup", body
    assert body["yolo"] is False, body
    assert str(body["project_id"]) == chosen and body["scratch"] is False, body
    expect(page.locator(".scard", has_text="Simplified launch")).to_be_visible(timeout=15000)


@pytest.mark.parametrize("page", [PHONE, DESKTOP], indirect=True, ids=["phone", "desktop"])
def test_project_memory_survives_reload_blank_and_a_deleted_project(page, server):
    """Blank stays blank, a real choice reloads, and a deleted one falls back
    to the normal default instead of crashing or selecting something wrong."""
    errors = []
    page.on("pageerror", lambda error: errors.append(str(error)))
    projects = _projects(page, server)
    assert len(projects) >= 1
    first = projects[0]

    page.goto(server + "/#sessions")
    page.click("#sess-new")
    select = page.locator("#ns-project")
    # Nothing remembered yet: the ordinary default, the first project.
    assert select.input_value() == str(first["id"])

    # A deliberate blank room does not snap back to the first project.
    select.select_option("")
    expect(page.locator("#ns-proj-hint")).to_contain_text("throwaway directory")
    page.keyboard.press("Escape")
    page.click("#sess-new")
    assert page.locator("#ns-project").input_value() == ""
    page.keyboard.press("Escape")

    # A project choice survives a reload.
    page.reload()
    page.click("#sess-new")
    page.locator("#ns-project").select_option(str(first["id"]))
    page.keyboard.press("Escape")
    page.reload()
    page.click("#sess-new")
    assert page.locator("#ns-project").input_value() == str(first["id"])
    page.keyboard.press("Escape")

    # Corrupt storage costs the memory, never the sheet.
    page.evaluate("localStorage.setItem('lec-project-preference-v1', '{not json')")
    page.reload()
    page.click("#sess-new")
    assert page.locator("#ns-project").input_value() == str(first["id"])
    page.keyboard.press("Escape")

    # A remembered project that is later deleted cannot be selected again.
    stale = page.request.post(
        f"{server}/api/projects",
        data={
            "name": "Zz stale pick",
            "target_id": first["target_id"],
            "repo_path": "/mock/stale-pick",
        },
    ).json()
    page.reload()
    page.click("#sess-new")
    page.locator("#ns-project").select_option(str(stale["id"]))
    page.keyboard.press("Escape")
    assert page.request.delete(f"{server}/api/projects/{stale['id']}").ok
    page.reload()
    page.click("#sess-new")
    value = page.locator("#ns-project").input_value()
    assert value != str(stale["id"]), "a deleted project must not stay selected"
    assert value == str(first["id"]), value
    assert not errors, errors


def _picker_rows(panel):
    """The picker as a person sees it: each row and the group heading above it."""
    return panel.evaluate(
        """el => {
            const rows = []; let group = '';
            for (const node of el.children) {
              if (node.classList.contains('terminal-new-group')) { group = node.textContent.trim(); continue; }
              if (node.classList.contains('terminal-new-project'))
                rows.push({group, name: node.querySelector('.terminal-new-project-name').textContent});
            }
            return rows;
        }"""
    )


def _open_picker(page):
    picker = page.locator(".terminal-tabbar .terminal-new-machines")
    expect(picker).to_have_count(1)
    if not picker.evaluate("el => el.open"):
        picker.locator("summary").click()
    panel = picker.locator(".terminal-new-picker")
    expect(panel).to_be_visible()
    return panel


def _wait_for_shell(t, project_id):
    deadline = time.monotonic() + 15
    while time.monotonic() < deadline:
        found = [
            s
            for s in t["api"]("/sessions")
            if s.get("project_id") == project_id and s.get("agent") == "shell"
        ]
        if found:
            return found[0]
        time.sleep(0.1)
    raise AssertionError("the project shell never appeared")


def test_project_picker_keeps_successful_projects_on_top(page, real_terminal):
    """Opening a shell in a project puts it in a Recent group; a failed
    creation must not."""
    t = real_terminal
    alpha = t["api"]("/projects", {"name": "Recency alpha", "target_id": t["target_id"], "repo_path": str(t["root"])})
    beta = t["api"]("/projects", {"name": "Recency beta", "target_id": t["target_id"], "repo_path": str(t["root"])})
    page.goto(t["url"] + "/#terminals")

    # Nothing opened yet: one plain, alphabetical list.
    rows = _picker_rows(_open_picker(page))
    assert rows and all(row["group"] == "Projects" for row in rows), rows
    page.keyboard.press("Escape")

    # A shell that really opened is what makes a project recent.
    panel = _open_picker(page)
    panel.locator(".terminal-new-project", has_text="Recency beta").click()
    _wait_for_shell(t, beta["id"])
    panel = _open_picker(page)
    expect(panel.locator(".terminal-new-group").filter(has_text="Recent")).to_be_visible()
    rows = _picker_rows(panel)
    assert rows[0] == {"group": "Recent", "name": "Recency beta"}, rows
    assert {"group": "Other projects", "name": "Recency alpha"} in rows, rows
    page.keyboard.press("Escape")

    # A creation that fails leaves the recents exactly as they were.
    def refuse(route):
        if route.request.method == "POST":
            route.fulfill(status=503, content_type="application/json", body='{"detail":"Temporary shell failure"}')
        else:
            route.continue_()

    page.route("**/api/shells", refuse)
    try:
        panel = _open_picker(page)
        panel.locator(".terminal-new-project", has_text="Recency alpha").click()
        expect(page.locator("#toasts")).to_contain_text("Temporary shell failure", timeout=10000)
    finally:
        page.unroute("**/api/shells", refuse)
    rows = _picker_rows(_open_picker(page))
    assert {"group": "Recent", "name": "Recency alpha"} not in rows, rows
    assert rows[0] == {"group": "Recent", "name": "Recency beta"}, rows
