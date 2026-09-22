#!/usr/bin/env python3
"""lectern nightly smoke — proves the full pipeline still works while you sleep.

1. deep-probes every non-mock target (toolchain + REAL claude auth round-trip)
2. dispatches one tiny task on the smoke project (local target, scratch repo)
3. waits for review + auto-verify, marks it done, cleans the worktree
4. reports the outcome to Discord (same bot/channel as the morning briefing)

Run by lectern-smoke.timer (04:30). Stdlib only.
"""
import json
import os
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

API = os.environ.get("LECTERN_API", "http://127.0.0.1:9110").rstrip("/") + "/api"
SMOKE_REPO = Path(os.environ.get(
    "LECTERN_SMOKE_REPO", "/home/admin/projects/lectern/smoke-repo"))
# Discord reporting is optional; set these (e.g. via EnvironmentFile) to enable.
BOT_TOKEN = os.environ.get("LECTERN_DISCORD_BOT_TOKEN", "")
CHANNEL_ID = os.environ.get("LECTERN_DISCORD_CHANNEL_ID", "")


def api(method: str, path: str, body: dict | None = None):
    req = urllib.request.Request(API + path, method=method,
                                 headers={"Content-Type": "application/json"},
                                 data=json.dumps(body).encode() if body else None)
    with urllib.request.urlopen(req, timeout=180) as r:
        return json.load(r)


def discord(msg: str) -> None:
    if not BOT_TOKEN or not CHANNEL_ID:
        print("(discord reporting disabled — no bot token/channel configured)")
        return
    try:
        req = urllib.request.Request(
            f"https://discord.com/api/v10/channels/{CHANNEL_ID}/messages",
            method="POST",
            headers={"Authorization": f"Bot {BOT_TOKEN}",
                     "Content-Type": "application/json",
                     # Cloudflare 403s urllib's default UA
                     "User-Agent": "lectern-smoke/1.0"},
            data=json.dumps({"content": msg[:1900]}).encode())
        urllib.request.urlopen(req, timeout=15)
    except Exception as e:  # noqa: BLE001
        print(f"discord send failed: {e}", file=sys.stderr)


def ensure_smoke_repo() -> None:
    if (SMOKE_REPO / ".git").exists():
        return
    SMOKE_REPO.mkdir(parents=True, exist_ok=True)
    (SMOKE_REPO / "app.py").write_text('print("smoke ok")\n')
    for cmd in (["git", "init", "-q", "-b", "main"], ["git", "add", "app.py"],
                ["git", "-c", "user.email=smoke@lec", "-c", "user.name=smoke",
                 "commit", "-qm", "initial"]):
        subprocess.run(cmd, cwd=SMOKE_REPO, check=True)


def ensure_smoke_project() -> int:
    for p in api("GET", "/projects"):
        if p["name"] == "lec-smoke":
            return p["id"]
    local = next(t for t in api("GET", "/targets") if t["kind"] == "local")
    return api("POST", "/projects", {
        "name": "lec-smoke", "target_id": local["id"],
        "repo_path": str(SMOKE_REPO), "verify_cmd": "python3 app.py"})["id"]


def main() -> int:
    lines, failed = [], False

    # 1. deep target probes (real auth round-trip)
    for t in api("GET", "/targets"):
        if t["kind"] == "mock":
            continue
        try:
            r = api("POST", f"/targets/{t['id']}/check?deep=true")
            info = json.loads(r["info_json"])
            auth = info.get("claude_auth", "n/a")
            ok = r["status"] == "online" and auth in ("ok", "n/a")
            failed |= not ok
            lines.append(f"{'✅' if ok else '❌'} target `{t['name']}` "
                         f"{r['status']}, auth {auth}")
        except Exception as e:  # noqa: BLE001
            failed = True
            lines.append(f"❌ target `{t['name']}` probe error: {e}")

    # 2. real dispatch
    ensure_smoke_repo()
    pid = ensure_smoke_project()
    stamp = time.strftime("%Y-%m-%d")
    task = api("POST", "/tasks", {
        "project_id": pid, "title": f"smoke {stamp}",
        "prompt": 'In app.py change the printed string to exactly "smoke ok" '
                  "(keep it if already correct) and add nothing else.",
        "permission_mode": "acceptEdits", "model": "haiku"})
    api("POST", f"/tasks/{task['id']}/dispatch", {})
    status = "timeout"
    for _ in range(120):   # up to 10 min
        time.sleep(5)
        status = api("GET", f"/tasks/{task['id']}")["status"]
        if status in ("review", "failed"):
            break
    detail = api("GET", f"/tasks/{task['id']}")
    verify = (detail.get("attempt") or {}).get("verify") or {}
    ok = status == "review" and verify.get("rc") == 0
    failed |= not ok
    lines.append(f"{'✅' if ok else '❌'} smoke dispatch #{task['id']}: {status}, "
                 f"verify rc={verify.get('rc')}, "
                 f"cost=${(detail['attempt']['result'].get('cost_usd') or 0):.3f}"
                 if detail.get("attempt") else f"❌ smoke dispatch: {status}")

    # 3. tidy up
    if status == "review":
        api("POST", f"/tasks/{task['id']}/complete")
        api("POST", f"/tasks/{task['id']}/cleanup")
        api("POST", "/admin/janitor", {})

    header = "🔴 **lectern smoke FAILED**" if failed else "🟢 **lectern smoke passed**"
    discord(header + "\n" + "\n".join(lines))
    print(header)
    print("\n".join(lines))
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
