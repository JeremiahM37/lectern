"""Any coding CLI, end to end: catalog preset -> custom agent -> shown/hidden
menus -> a real session -> a real handoff switch.

The executable is a tiny fixture script, not a real third-party CLI — Lectern
never assumes an operator's chosen agent is installed on this host, and this
suite must not depend on one being. The Cursor Agent CLI catalog preset is
used only to prove the "Starter template" dropdown is populated from
GET /api/agents/catalog and prefills real fields; its Command is then
overridden to the stub so the session that follows is a genuine tmux launch.
"""
import json
import re
import subprocess
import time
import urllib.request

import pytest
from playwright.sync_api import expect

from session_sheet import open_advanced
from test_terminal_workspace import real_terminal


def _wait_for_pane_text(t, tmux_session, text, timeout=20):
    """Poll the real tmux pane directly rather than the web terminal — a
    freshly launched scratch session can take a few seconds longer to reach
    the point where its terminal iframe/websocket is ready than the process
    itself takes to print its first line, and this is what every other
    real_terminal test (e.g. test_terminal_workspace.py's own `capture`)
    checks against instead.
    """
    deadline = time.monotonic() + timeout
    last = ""
    while time.monotonic() < deadline:
        last = subprocess.run(
            ["tmux", "capture-pane", "-p", "-J", "-S", "-1000", "-t", "=" + tmux_session + ":"],
            env=t["env"], capture_output=True, text=True,
        ).stdout
        if text in last:
            return
        time.sleep(0.2)
    raise AssertionError(f"{text!r} did not appear in pane {tmux_session!r} within {timeout}s: {last!r}")


def _stub_agent_script(tmp_path):
    """A CLI stub that: prints a READY line naming itself and its argv (so a
    real launch is provably a real launch), and answers a handoff request the
    same way test_quick_switch.py's runner does — the WHERE WE ARE/NEXT
    pattern useSwitch's backend scans the pane for.
    """
    path = tmp_path / "stub-cli.py"
    path.write_text(r'''#!/usr/bin/env python3
import re, sys
print('STUB AGENT READY argv=' + ' '.join(sys.argv[1:]), flush=True)
for line in sys.stdin:
    match = re.search(r'/tmp/lectern-handoff-[0-9]+-[a-z0-9]+\.md', line)
    if match:
        from pathlib import Path
        path = Path(match.group())
        body = '## WHERE WE ARE\nStub state.\n## NEXT\nStub next step.\n'
        part = Path(str(path) + '.partial')
        part.write_text(body + '<!-- lectern:complete ' + str(path) + ' -->\n')
        part.replace(path)
        print('WRAPPED', flush=True)
''')
    path.chmod(0o755)
    return path


def _override_claude_with_stub(t, stub):
    """The fixture's adopted "Real terminal" session is a bare `bash --norc`
    labelled claude — switching FROM it would hang waiting for a handoff
    that bash can never produce. Overriding the claude definition with the
    stub (same trick as test_quick_switch.py) makes the switch source a real,
    scriptable agent instead.
    """
    req = urllib.request.Request(
        t["url"] + "/api/agents", method="PUT",
        data=json.dumps([
            {"name": "claude", "command": str(stub), "prompt_arg": True,
             "resume_args": ["--continue"], "yolo_args": ["--yolo"]},
        ]).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req) as response:
        json.load(response)


@pytest.mark.parametrize("width", [390, 1440])
def test_catalog_preset_menu_visibility_session_and_switch(page, real_terminal, tmp_path, width):
    t = real_terminal
    stub = _stub_agent_script(tmp_path)
    _override_claude_with_stub(t, stub)
    # The fixture's own adopted "Real terminal" session is a bare bash shell
    # that happens to be labelled claude; it can never answer a handoff
    # request. A fresh session started now genuinely runs the (just
    # overridden) claude stub, the same way test_quick_switch.py's `prepare`
    # does.
    switch_source = t["api"]("/sessions", {
        "target_id": t["target_id"], "workdir": str(t["root"]),
        "agent": "claude", "name": "Switch source",
    })
    page.set_viewport_size({"width": width, "height": 844})

    # ---- Settings -> Agents: add a preset-seeded custom agent ------------
    page.goto(t["url"] + "/#targets")
    page.locator('[data-settings="agents"]').click()
    page.get_by_role("button", name="Add agent", exact=True).click()
    dialog = page.get_by_role("dialog", name="Add agent", exact=True)
    starter = dialog.get_by_label("Starter template", exact=False)
    expect(starter.get_by_role("option", name=re.compile("Cursor Agent CLI"))).to_have_count(1, timeout=10000)
    starter.select_option("cursor-agent")
    # The preset really did populate real, researched fields...
    expect(dialog.get_by_label("Model flag", exact=True)).to_have_value("--model")
    expect(dialog.get_by_label("Opening prompt is a positional argument", exact=True)).to_be_checked()
    expect(dialog.locator(".agent-preset-hint")).to_contain_text("cursor.com")
    # ...then becomes a genuinely runnable stub for this test, same shape.
    dialog.get_by_label("Name", exact=True).fill("stub-agent")
    dialog.get_by_label("Command", exact=True).fill(str(stub))
    dialog.get_by_role("button", name="Save runner", exact=True).click()
    expect(page.locator(".agent-card", has_text="stub-agent")).to_be_visible()

    # New/custom agents are not shown in pickers until opted in.
    expect(page.locator(".agent-menu-hidden").get_by_label("stub-agent", exact=True)).not_to_be_checked()
    expect(page.locator(".agent-menu-order").get_by_label("stub-agent", exact=True)).to_have_count(0)

    # ---- New session: hidden by default, reachable via "More agents…" ----
    page.goto(t["url"] + "/#sessions")
    page.click("#sess-new")
    expect(page.locator("#ns-agent")).to_be_visible(timeout=10000)
    expect(page.locator("#ns-agent option", has_text="stub-agent")).to_have_count(0)
    more = page.locator("#ns-agent option", has_text="More agents")
    expect(more).to_have_count(1)
    page.select_option("#ns-agent", label=more.inner_text())
    picker = page.get_by_role("dialog", name="All agents", exact=True)
    expect(picker).to_be_visible()
    picker.get_by_role("button", name=re.compile("^stub-agent")).click()
    expect(picker).not_to_be_visible()
    open_advanced(page)
    page.fill("#ns-name", "stub session")
    page.click("#ns-go")
    card = page.locator(".scard", has_text="stub session")
    expect(card).to_be_visible(timeout=15000)
    expect(card.locator(".chip", has_text="stub-agent")).to_be_visible()
    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")

    new_session_id = page.evaluate(
        "async () => { const ss = await (await fetch('/api/sessions')).json();"
        " return ss.find(s => s.name === 'stub session').id; }"
    )
    new_session = t["api"](f"/sessions/{new_session_id}")
    _wait_for_pane_text(t, new_session["tmux_session"], "STUB AGENT READY")

    # ---- Switch: still hidden, still reachable via "More agents…" --------
    source_id = switch_source["id"]
    _wait_for_pane_text(t, switch_source["tmux_session"], "STUB AGENT READY")
    page.goto(f"{t['url']}/#terminals/session/{source_id}")
    trigger = page.get_by_role("button", name="Switch agent or model")
    trigger.click()
    sheet = page.get_by_role("dialog", name="Switch agent", exact=True)
    expect(sheet.locator('section[aria-label="stub-agent"]')).to_have_count(0)
    switch_more = sheet.get_by_role("button", name="More agents…")
    expect(switch_more).to_be_visible()
    switch_more.click()
    switch_picker = page.get_by_role("dialog", name="All agents", exact=True)
    expect(switch_picker).to_be_visible()
    switch_picker.get_by_role("button", name=re.compile("^stub-agent")).click()
    expect(page).not_to_have_url(re.compile(f"/session/{source_id}$"), timeout=20000)
    next_id = int(page.url.rsplit("/", 1)[-1])
    successor = t["api"](f"/sessions/{next_id}")
    assert successor["agent"] == "stub-agent"
    assert t["api"](f"/sessions/{source_id}")["ended_at"] is None

    # ---- Toggle visibility on: now a first-class picker entry ------------
    page.goto(t["url"] + "/#targets")
    page.locator('[data-settings="agents"]').click()
    # A plain .click() rather than .check(): toggling this checkbox moves its
    # whole row out of the "hidden" list into the "shown" one once the save
    # round-trips — the checkbox never becomes checked=true in place (its
    # hidden-list rendering is checked={false} by construction), so
    # Locator.check()'s own postcondition polling would retry forever.
    page.locator(".agent-menu-hidden").get_by_label("stub-agent", exact=True).click()
    expect(page.locator(".agent-menu-order").get_by_label("stub-agent", exact=True)).to_be_checked()

    page.goto(t["url"] + "/#sessions")
    page.click("#sess-new")
    expect(page.locator("#ns-agent")).to_be_visible(timeout=10000)
    expect(page.locator("#ns-agent option", has_text="stub-agent")).to_have_count(1)
    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth")
