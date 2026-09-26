"""Structured chat cards and graduated approvals in the session Chat view.

Rendering is exercised against the mock server with a route-intercepted
GET .../conversation/live response (a synthetic fixture transcript — see
docs/mobile-sessions.md "Chat cards"), the same route-interception pattern
test_react_conversation.py already uses. The approval flow is exercised for
real: real tmux, a real stub "claude" process answering PermissionRequest
exactly like Claude Code's own hook, and a real decision round trip —
following test_session_permission_request.py's STUB_AGENT pattern.
"""
import json
import time
from pathlib import Path

import pytest
from playwright.sync_api import expect

from conftest import DESKTOP, PHONE
from test_terminal_workspace import real_terminal  # noqa: F401  (fixture)
from test_session_permission_request import STUB_AGENT  # single Bash call, held once


def session_card(page, name):
    return page.locator(".scard", has_text=name)


def open_chat(page, server, name):
    page.request.post(server + "/api/sessions", data={"name": name, "scratch": True, "agent": "claude"})
    page.goto(server + "/#sessions")
    session_card(page, name).get_by_role("button", name="Chat", exact=True).click()
    expect(page.locator("#conversation")).to_be_visible()


# A synthetic transcript (never real transcript content — see the HARD RULES
# this branch was built under) covering every kind the structured endpoint
# emits: user/assistant text, thinking, a Read, an Edit with a real diff, and
# a Bash call with output to expand.
FIXTURE_ITEMS = [
    {"id": "1-0", "role": "user", "kind": "text", "text": "Please read config.yaml and fix the typo"},
    {"id": "2-0", "role": "assistant", "kind": "thinking", "text": "I should look at the file first"},
    {"id": "2-1", "role": "assistant", "kind": "text", "text": "Let me check the file."},
    {
        "id": "2-2",
        "role": "assistant",
        "kind": "tool_use",
        "tool_name": "Read",
        "tool_use_id": "tu1",
        "input": {"file_path": "/app/config.yaml"},
    },
    {"id": "3-0", "role": "tool", "kind": "tool_result", "tool_use_id": "tu1", "output": "name: myapp\nport: 8080"},
    {"id": "4-0", "role": "assistant", "kind": "text", "text": "Found it — fixing now."},
    {
        "id": "4-1",
        "role": "assistant",
        "kind": "tool_use",
        "tool_name": "Edit",
        "tool_use_id": "tu2",
        "input": {"file_path": "/app/config.yaml", "old_string": "port: 8080", "new_string": "port: 8081"},
    },
    {"id": "5-0", "role": "tool", "kind": "tool_result", "tool_use_id": "tu2", "output": ""},
    {
        "id": "5-1",
        "role": "assistant",
        "kind": "tool_use",
        "tool_name": "Bash",
        "tool_use_id": "tu3",
        "input": {"command": "pytest -q"},
    },
    {"id": "6-0", "role": "tool", "kind": "tool_result", "tool_use_id": "tu3", "output": "3 passed in 0.42s"},
    {"id": "6-1", "role": "assistant", "kind": "text", "text": "All tests pass."},
]


def stub_live_conversation(page):
    # Routed on the CONTEXT, not the page: the PWA's service worker answers
    # some GETs itself, and only context-level routing sees requests it
    # would otherwise handle before page-level routing gets a look.
    page.context.route(
        "**/api/sessions/*/conversation/live*",
        lambda route: route.fulfill(
            status=200,
            content_type="application/json",
            body=json.dumps({"conversation_id": "fixture", "items": FIXTURE_ITEMS, "cursor": 999, "truncated": False}),
        ),
    )


@pytest.mark.parametrize("width", [390, 1440])
def test_chat_cards_render_a_fixture_transcript(page, server, width):
    page.set_viewport_size({"width": width, "height": 844 if width == 390 else 900})
    stub_live_conversation(page)
    open_chat(page, server, f"Cards {width}")
    log = page.locator("#conversation-cards")
    expect(log).to_contain_text("Please read config.yaml", timeout=15000)
    expect(log).to_contain_text("All tests pass.")
    # A raw JSON dump of the tool_use/tool_result must never appear.
    expect(log).not_to_contain_text('"tool_name"')
    expect(log).not_to_contain_text('"file_path"')

    # Every tool call became a named card, not a blob.
    expect(log.locator(".tool-card", has_text="/app/config.yaml")).to_have_count(2)  # Read + Edit
    bash_card = log.locator(".tool-card", has_text="pytest -q")
    expect(bash_card).to_be_visible()

    # Thinking is present but visually distinct (collapsed by default).
    expect(log.locator(".reader-message.thinking")).to_contain_text("Thinking")

    assert page.evaluate("document.documentElement.scrollWidth<=innerWidth"), "horizontal overflow"
    page.locator("#conversation-close").click()


def test_expanding_a_bash_card_shows_its_output(page, server):
    page.set_viewport_size(PHONE)
    stub_live_conversation(page)
    open_chat(page, server, "Bash card")
    bash_card = page.locator("#conversation-cards .tool-card", has_text="pytest -q")
    expect(bash_card).to_be_visible()
    output = bash_card.locator(".tool-output")
    expect(output).not_to_be_visible()  # collapsed by default
    bash_card.locator("> summary").click()
    expect(output).to_be_visible()
    expect(output).to_contain_text("3 passed in 0.42s")


def test_edit_card_shows_a_real_diff(page, server):
    page.set_viewport_size(PHONE)
    stub_live_conversation(page)
    open_chat(page, server, "Edit card")
    edit_card = page.locator("#conversation-cards .tool-card", has_text="/app/config.yaml").nth(1)
    edit_card.locator("> summary").click()
    expect(edit_card.locator(".dl-del")).to_contain_text("-port: 8080")
    expect(edit_card.locator(".dl-add")).to_contain_text("+port: 8081")


def test_terminal_text_is_one_tap_away_and_back(page, server):
    page.set_viewport_size(PHONE)
    stub_live_conversation(page)
    open_chat(page, server, "Toggle view")
    expect(page.locator("#conversation-cards")).to_be_visible()
    page.locator("#conversation-view-toggle").click()
    expect(page.locator(".session-reader")).to_be_visible()
    expect(page.locator("#conversation-cards")).to_have_count(0)
    page.locator("#conversation-view-toggle").click()
    expect(page.locator("#conversation-cards")).to_be_visible()


def test_terminal_text_is_the_fallback_when_structured_chat_is_unavailable(page, server):
    page.set_viewport_size(PHONE)
    page.context.route(
        "**/api/sessions/*/conversation/live*",
        lambda route: route.fulfill(
            status=409, content_type="application/json", body='{"detail":"structured chat unavailable"}'
        ),
    )
    open_chat(page, server, "No structured chat")
    expect(page.locator(".session-reader")).to_be_visible(timeout=15000)
    expect(page.locator("#conversation-view-toggle")).to_have_count(0)


# ---- graduated approvals: real hold/decide round trip -------------------------

# Sends the SAME PermissionRequest twice in a row, writing each response to
# its own file — the second call proves (or disproves) that "Allow for this
# session" short-circuited it without a person deciding again.
STUB_AGENT_TWO_CALLS = '''#!/bin/bash
echo "stub agent ready"
python3 - <<'PYEOF'
import json, os, urllib.request

token = os.environ.get("LECTERN_HOOK_TOKEN", "")
url = os.environ.get("LECTERN_HOOK_URL", "").rstrip("/")

def ask(command):
    body = json.dumps({
        "hook_event_name": "PermissionRequest",
        "tool_name": "Bash",
        "tool_input": {"command": command},
    }).encode()
    req = urllib.request.Request(
        url + "/PermissionRequest", data=body,
        headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
        method="POST",
    )
    with urllib.request.urlopen(req, timeout=60) as resp:
        return resp.read()

first = ask("git status")
with open(os.path.join(os.getcwd(), "hook-response.json"), "wb") as f:
    f.write(first)
second = ask("git status --short")
with open(os.path.join(os.getcwd(), "hook-response-2.json"), "wb") as f:
    f.write(second)
print("GOT_RESPONSES")
PYEOF
while IFS= read -r line; do :; done
'''


@pytest.mark.parametrize("real_terminal", [{"agent_script": STUB_AGENT_TWO_CALLS}], indirect=True)
def test_approval_shows_the_command_and_allow_for_session_answers_the_next_call(page, real_terminal):
    t = real_terminal
    t["api"](
        "/sessions",
        {"target_id": t["target_id"], "workdir": str(t["root"]), "agent": "claude",
         "name": "Graduated approval", "permission_mode": "ask"},
    )
    page.set_viewport_size(PHONE)
    page.goto(t["url"] + "/#sessions")
    session_card(page, "Graduated approval").get_by_role("button", name="Chat", exact=True).click()

    approval = page.locator(".approval-card")
    expect(approval).to_contain_text("Approval needed: Bash", timeout=25000)
    # The command is shown, never a JSON blob of the tool_input.
    expect(approval).to_contain_text("git status")
    expect(approval).not_to_contain_text('"command"')
    expect(approval.get_by_role("button", name="Allow once", exact=True)).to_be_visible()
    expect(approval.get_by_role("button", name="Deny…", exact=True)).to_be_visible()
    allow_for_session = approval.get_by_role("button", name="Allow for this session", exact=True)
    expect(allow_for_session).to_be_visible()
    allow_for_session.click()
    expect(page.locator(".approval-card")).to_have_count(0, timeout=20000)

    first_path = Path(t["root"]) / "hook-response.json"
    second_path = Path(t["root"]) / "hook-response-2.json"
    deadline = time.time() + 20
    while time.time() < deadline and not second_path.exists():
        time.sleep(0.2)
    assert first_path.exists() and second_path.exists(), "the stub agent's two requests were not both answered"
    first_decision = json.loads(first_path.read_text())["hookSpecificOutput"]["decision"]
    second_decision = json.loads(second_path.read_text())["hookSpecificOutput"]["decision"]
    assert first_decision["behavior"] == "allow", first_decision
    assert second_decision["behavior"] == "allow", second_decision
    # The short-circuited second call must never have shown up as a new
    # approval on the phone at all.
    expect(page.locator(".approval-card")).to_have_count(0)


@pytest.mark.parametrize("real_terminal", [{"agent_script": STUB_AGENT}], indirect=True)
def test_deny_with_feedback_carries_the_note_back_to_the_agent(page, real_terminal):
    t = real_terminal
    t["api"](
        "/sessions",
        {"target_id": t["target_id"], "workdir": str(t["root"]), "agent": "claude",
         "name": "Deny feedback", "permission_mode": "ask"},
    )
    page.set_viewport_size(DESKTOP)
    page.goto(t["url"] + "/#sessions")
    session_card(page, "Deny feedback").get_by_role("button", name="Chat", exact=True).click()

    approval = page.locator(".approval-card")
    expect(approval).to_contain_text("Approval needed: Bash", timeout=25000)
    expect(approval).to_contain_text("rm -rf important")
    approval.get_by_role("button", name="Deny…", exact=True).click()
    approval.get_by_placeholder("e.g. not touching prod from a phone").fill("not from a phone")
    approval.get_by_role("button", name="Deny with feedback", exact=True).click()
    expect(page.locator(".approval-card")).to_have_count(0, timeout=20000)

    response_path = Path(t["root"]) / "hook-response.json"
    deadline = time.time() + 20
    while time.time() < deadline and not response_path.exists():
        time.sleep(0.2)
    assert response_path.exists()
    decision = json.loads(response_path.read_text())["hookSpecificOutput"]["decision"]
    assert decision["behavior"] == "deny"
    assert decision["message"] == "not from a phone"
