"""Real browser coverage for the ACP (`acp: {command, args, env}`) agent
registry fields in Settings -> Agents: adding an ACP agent, the task/acp
mutual exclusivity in the form, that a preset needing a binary the current
target has NOT confirmed (npx, per the harness fixture) is disabled while one
that was never probed (gemini) stays selectable, and that the assembled
PUT body carries the exact `acp: {command, args, env}` shape the backend
expects (internal/sessions.ACPSpec).

This drives the settings-harness fixture (a static, client-side-only mocked
API, same pattern as test_react_settings.py) rather than a full lectern
server + real ACP binary. The harness's `/agents` GET always returns the same
two fixture agents regardless of what was PUT, so this test asserts against
the captured `window.calls` PUT body — exactly how test_react_settings.py's
own agent-editing coverage already works — rather than a re-rendered agent
card. Real backend masking/retention/validation for `acp.env` is covered by
internal/api/acp_agent_test.go (a real Go server, not this mock); the actual
JSON-RPC protocol, permissions, fs confinement and steering are covered by
internal/drivers/acp_real_test.go.
"""
from pathlib import Path
import subprocess
import time
import urllib.request

import pytest
from playwright.sync_api import expect

R = Path(__file__).parents[1]


@pytest.fixture
def harness(browser):
    p = subprocess.Popen(
        ["npm", "exec", "vite", "--", "--host", "127.0.0.1", "--port", "4192"],
        cwd=R / "frontend", stdout=subprocess.DEVNULL, stderr=subprocess.STDOUT,
    )
    try:
        url = "http://127.0.0.1:4192/react/settings-harness.html"
        for _ in range(50):
            try:
                urllib.request.urlopen(url)
                break
            except Exception:
                time.sleep(0.1)
        with browser.new_context() as context:
            page = context.new_page()
            page.goto(url)
            yield page
    finally:
        p.terminate()
        p.wait()


def test_acp_agent_form(harness):
    page = harness
    page.get_by_role("tab", name="Agents").click()
    page.get_by_role("button", name="Add agent", exact=True).click()
    dialog = page.get_by_role("dialog", name="Add agent", exact=True)

    # The harness's one target probed npx and did not find it; gemini was
    # never probed at all. "Unknown" must not read as "missing". Playwright's
    # to_be_disabled()/to_be_enabled() do not reliably read an <option>'s
    # native `disabled` property (its own accessibility snapshot shows the
    # attribute correctly; the state assertion does not), so this reads the
    # DOM property directly instead.
    template = dialog.locator("select").first
    assert template.locator('option[value="claude-code-acp"]').evaluate("el => el.disabled") is True
    assert template.locator('option[value="codex-acp"]').evaluate("el => el.disabled") is True
    assert template.locator('option[value="gemini-acp"]').evaluate("el => el.disabled") is False

    dialog.get_by_label("Name", exact=True).fill("my-acp-agent")
    dialog.get_by_label("Command", exact=True).fill("my-acp-agent")

    # ACP and background tasks are mutually exclusive in the form.
    tasks_checkbox = dialog.get_by_label("Enable background tasks for this agent", exact=True)
    acp_checkbox = dialog.get_by_label(
        "Use the Agent Client Protocol (ACP) instead of a task command", exact=True
    )
    expect(tasks_checkbox).to_be_enabled()
    acp_checkbox.check()
    expect(tasks_checkbox).to_be_disabled()

    dialog.get_by_label("ACP command", exact=True).fill("npx")
    dialog.get_by_label("ACP arguments (one per line; JSON array accepted)", exact=True).fill(
        '["-y", "@zed-industries/claude-code-acp"]'
    )
    dialog.get_by_label(
        "ACP environment (KEY=value lines; existing values are masked and retained)", exact=True
    ).fill("ACP_PROXY_TOKEN=synthetic-acp-secret\nACP_LOG_LEVEL=debug")

    dialog.get_by_role("button", name="Save runner", exact=True).click()
    page.wait_for_function("calls.some(c => c[0] === '/agents' && c[1]?.method === 'PUT')")
    page.screenshot(path="/tmp/lectern-acp-agent-settings.png", full_page=True)

    put_bodies = page.evaluate(
        "calls.filter(c => c[0] === '/agents' && c[1]?.method === 'PUT').map(c => c[1].body)"
    )
    saved = next((a for a in put_bodies[-1] if a.get("name") == "my-acp-agent"), None)
    assert saved is not None, "the PUT body did not include the new acp agent"
    assert saved.get("task") is None, "acp and task must be mutually exclusive on save"
    acp = saved.get("acp")
    assert acp["command"] == "npx"
    assert acp["args"] == ["-y", "@zed-industries/claude-code-acp"]
    assert acp["env"]["ACP_LOG_LEVEL"] == "debug"
    assert acp["env"]["ACP_PROXY_TOKEN"] == "synthetic-acp-secret"


def test_acp_preset_fills_command_and_args(harness):
    page = harness
    page.get_by_role("tab", name="Agents").click()
    page.get_by_role("button", name="Add agent", exact=True).click()
    dialog = page.get_by_role("dialog", name="Add agent", exact=True)
    dialog.locator("select").first.select_option("gemini-acp")
    expect(dialog.get_by_label("Name", exact=True)).to_have_value("gemini-acp")
    expect(dialog.get_by_label("ACP command", exact=True)).to_have_value("gemini")
    expect(dialog.get_by_label("ACP arguments (one per line; JSON array accepted)", exact=True)).to_have_value(
        '[\n  "--experimental-acp"\n]'
    )
    expect(dialog.get_by_label("Enable background tasks for this agent", exact=True)).to_be_disabled()
