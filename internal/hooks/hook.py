#!/usr/bin/env python3
"""lectern PreToolUse hook — blocks a tool call until the operator decides.

Claude Code invokes this with the tool call as JSON on stdin. Exit 0 allows the
call; exit 2 blocks it and feeds stderr back to Claude as the reason.
Configuration via env: LECTERN_URL, LECTERN_TOKEN (per-attempt secret).
Stdlib only — targets need nothing beyond python3.
"""
import json
import os
import sys
import time
import urllib.error
import urllib.request

URL = os.environ.get("LECTERN_URL", "").rstrip("/")
TOKEN = os.environ.get("LECTERN_TOKEN", "")


def api(method: str, path: str, body: dict | None = None) -> dict:
    req = urllib.request.Request(URL + path, method=method,
                                 headers={"Content-Type": "application/json"})
    data = json.dumps(body).encode() if body is not None else None
    with urllib.request.urlopen(req, data=data, timeout=60) as resp:
        return json.load(resp)


def main() -> int:
    if not URL or not TOKEN:
        return 0   # unconfigured → don't block local runs
    try:
        payload = json.load(sys.stdin)
    except ValueError:
        return 0
    try:
        created = api("POST", "/api/hook/approval", {
            "token": TOKEN,
            "tool_name": payload.get("tool_name", "?"),
            "tool_input": payload.get("tool_input", {})})
    except (urllib.error.URLError, OSError) as e:
        print(f"lectern approval server unreachable ({e}); blocking for safety",
              file=sys.stderr)
        return 2
    aid = created["id"]
    deadline = time.time() + float(os.environ.get("LECTERN_APPROVAL_TIMEOUT", "900"))
    while time.time() < deadline:
        try:
            d = api("GET", f"/api/hook/approval/{aid}/decision")
        except (urllib.error.URLError, OSError):
            time.sleep(5)
            continue
        status = d.get("status")
        if status == "approved":
            return 0
        if status in ("denied", "expired"):
            note = d.get("note") or "operator denied this action"
            print(f"Blocked by operator: {note}", file=sys.stderr)
            return 2
    print("Approval timed out with no operator decision; action blocked.",
          file=sys.stderr)
    return 2


if __name__ == "__main__":
    sys.exit(main())
