"""Approve/deny from the phone, end to end (docs/agent-events.md section 3):
a real server, real tmux, and a stub "claude" that behaves exactly like
Claude Code's `type: "http"` PermissionRequest hook — one held HTTP request,
answered once a human decides. No real model tokens are spent (workspace
rule): the stub only needs LECTERN_HOOK_TOKEN/LECTERN_HOOK_URL, which the
manager sets for every launched session regardless of which binary runs.
"""
import json
import time
from pathlib import Path

import pytest
from playwright.sync_api import expect

from test_terminal_workspace import real_terminal  # noqa: F401  (fixture)

# Posts a real PermissionRequest to this session's own hook endpoint and
# writes whatever comes back to hook-response.json in its cwd, exactly the
# shape internal/api/hooks_agentevents.go answers with.
STUB_AGENT = '''#!/bin/bash
echo "stub agent ready"
python3 - <<'PYEOF'
import json, os, urllib.request

token = os.environ.get("LECTERN_HOOK_TOKEN", "")
url = os.environ.get("LECTERN_HOOK_URL", "").rstrip("/")
body = json.dumps({
    "hook_event_name": "PermissionRequest",
    "tool_name": "Bash",
    "tool_input": {"command": "rm -rf important"},
}).encode()
req = urllib.request.Request(
    url + "/PermissionRequest", data=body,
    headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
    method="POST",
)
try:
    with urllib.request.urlopen(req, timeout=60) as resp:
        data = resp.read()
except Exception as e:
    data = json.dumps({"error": str(e)}).encode()
with open(os.path.join(os.getcwd(), "hook-response.json"), "wb") as f:
    f.write(data)
print("GOT_RESPONSE")
PYEOF
while IFS= read -r line; do :; done
'''


@pytest.mark.parametrize("real_terminal", [{"agent_script": STUB_AGENT}], indirect=True)
def test_approve_permission_request_from_needs_you(page, real_terminal):
    t = real_terminal
    sess = t["api"]("/sessions", {
        "target_id": t["target_id"], "workdir": str(t["root"]), "agent": "claude",
        "name": "Ask session", "permission_mode": "ask",
    })
    assert sess["permission_mode"] == "ask"

    page.goto(t["url"] + "/#sessions")
    needs = page.locator("#needs-you")
    row = needs.locator('.ny-row[data-reason="approval"]', has_text="Ask session")
    expect(row).to_contain_text("Approval needed", timeout=25000)
    expect(row).to_contain_text("Bash")

    row.get_by_role("button", name="Approve", exact=True).click()
    expect(needs.locator('.ny-row[data-reason="approval"]', has_text="Ask session")).to_have_count(
        0, timeout=20000
    )

    response_path = Path(t["root"]) / "hook-response.json"
    deadline = time.time() + 20
    while time.time() < deadline and not response_path.exists():
        time.sleep(0.2)
    assert response_path.exists(), "the stub agent never received a hook response"
    decision = json.loads(response_path.read_text())["hookSpecificOutput"]["decision"]
    assert decision["behavior"] == "allow", decision


@pytest.mark.parametrize("real_terminal", [{"agent_script": STUB_AGENT}], indirect=True)
def test_deny_permission_request_with_reason_from_needs_you(page, real_terminal):
    t = real_terminal
    t["api"]("/sessions", {
        "target_id": t["target_id"], "workdir": str(t["root"]), "agent": "claude",
        "name": "Ask session deny", "permission_mode": "ask",
    })

    page.goto(t["url"] + "/#sessions")
    needs = page.locator("#needs-you")
    row = needs.locator('.ny-row[data-reason="approval"]', has_text="Ask session deny")
    expect(row).to_contain_text("Approval needed", timeout=25000)

    row.get_by_role("button", name="Deny with reason…", exact=True).click()
    row.get_by_placeholder("Reason (optional)").fill("not from a phone")
    row.get_by_role("button", name="Send", exact=True).click()
    expect(needs.locator('.ny-row[data-reason="approval"]', has_text="Ask session deny")).to_have_count(
        0, timeout=20000
    )

    response_path = Path(t["root"]) / "hook-response.json"
    deadline = time.time() + 20
    while time.time() < deadline and not response_path.exists():
        time.sleep(0.2)
    assert response_path.exists()
    decision = json.loads(response_path.read_text())["hookSpecificOutput"]["decision"]
    assert decision["behavior"] == "deny"
    assert decision["message"] == "not from a phone"


# codex's own PermissionRequest hook is confirmed real (docs/agent-events.md
# section 2/3's correction) but is a COMMAND hook (sh -lc), not Claude's
# type:"http" — codex invokes the command and reads its stdout back, rather
# than making the HTTP request itself. The stub below stands in for that
# command, doing exactly what agentevents.CodexHookScript does: POST to
# LECTERN_HOOK_URL/PermissionRequest and print the response. This is the
# same "one held HTTP request, answered once a human decides" contract, and
# this test proves the real UI Approve flow reaches it identically for a
# codex-launched session — hookSessionEvent has no agent-specific branch.
STUB_CODEX_AGENT = '''#!/bin/bash
echo "stub agent ready"
python3 - <<'PYEOF'
import json, os, urllib.request

token = os.environ.get("LECTERN_HOOK_TOKEN", "")
url = os.environ.get("LECTERN_HOOK_URL", "").rstrip("/")
body = json.dumps({
    "hook_event_name": "PermissionRequest",
    "tool_name": "Bash",
    "tool_input": {"command": "rm -rf important-codex"},
}).encode()
req = urllib.request.Request(
    url + "/PermissionRequest", data=body,
    headers={"Authorization": "Bearer " + token, "Content-Type": "application/json"},
    method="POST",
)
try:
    with urllib.request.urlopen(req, timeout=60) as resp:
        data = resp.read()
except Exception as e:
    data = json.dumps({"error": str(e)}).encode()
with open(os.path.join(os.getcwd(), "hook-response.json"), "wb") as f:
    f.write(data)
print("GOT_RESPONSE")
PYEOF
while IFS= read -r line; do :; done
'''


@pytest.mark.parametrize(
    "real_terminal", [{"agent_script": STUB_CODEX_AGENT, "agent_script_agent": "codex"}], indirect=True
)
def test_approve_permission_request_from_needs_you_codex(page, real_terminal):
    t = real_terminal
    sess = t["api"]("/sessions", {
        "target_id": t["target_id"], "workdir": str(t["root"]), "agent": "codex",
        "name": "Codex ask session", "permission_mode": "ask",
    })
    assert sess["permission_mode"] == "ask"

    page.goto(t["url"] + "/#sessions")
    needs = page.locator("#needs-you")
    row = needs.locator('.ny-row[data-reason="approval"]', has_text="Codex ask session")
    expect(row).to_contain_text("Approval needed", timeout=25000)
    expect(row).to_contain_text("Bash")

    row.get_by_role("button", name="Approve", exact=True).click()
    expect(needs.locator('.ny-row[data-reason="approval"]', has_text="Codex ask session")).to_have_count(
        0, timeout=20000
    )

    response_path = Path(t["root"]) / "hook-response.json"
    deadline = time.time() + 20
    while time.time() < deadline and not response_path.exists():
        time.sleep(0.2)
    assert response_path.exists(), "the stub codex agent never received a hook response"
    decision = json.loads(response_path.read_text())["hookSpecificOutput"]["decision"]
    assert decision["behavior"] == "allow", decision
