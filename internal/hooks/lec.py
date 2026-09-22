#!/usr/bin/env python3
"""lectern agent-side kit — lets a running agent file follow-up task cards.
Usage (from inside the worktree):
    python3 .lectern/lec.py add-task "title" "detailed prompt" [--dispatch]
    python3 .lectern/lec.py add-note "durable fact future agents should know"
Auth comes from .lectern/env (per-attempt token). Stdlib only.
"""
import json
import os
import sys
import urllib.request


def load_env() -> dict:
    env = {}
    path = os.path.join(os.path.dirname(os.path.abspath(__file__)), "env")
    try:
        for line in open(path):
            if "=" in line:
                k, v = line.strip().split("=", 1)
                env[k] = v
    except OSError:
        pass
    return env


def main() -> int:
    args = [a for a in sys.argv[1:]]
    dispatch = "--dispatch" in args
    args = [a for a in args if a != "--dispatch"]
    if len(args) < 2 or args[0] not in ("add-task", "add-note"):
        print(__doc__, file=sys.stderr)
        return 2
    env = load_env()
    url, token = env.get("ADK_URL", "").rstrip("/"), env.get("ADK_TOKEN", "")
    if not url or not token:
        print("lec: missing .lectern/env", file=sys.stderr)
        return 2
    if args[0] == "add-note":
        path, payload = "/api/hook/notes", {"token": token, "note": args[1]}
    else:
        path = "/api/hook/tasks"
        payload = {"token": token, "title": args[1],
                   "prompt": args[2] if len(args) > 2 else args[1],
                   "dispatch": dispatch}
    req = urllib.request.Request(
        url + path, method="POST",
        headers={"Content-Type": "application/json"},
        data=json.dumps(payload).encode())
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            out = json.load(resp)
    except Exception as e:  # noqa: BLE001
        print(f"lec: request failed: {e}", file=sys.stderr)
        return 1
    if args[0] == "add-note":
        print(f"lec: noted (#{out['note_id']})")
    else:
        print(f"lec: filed task #{out['task_id']}"
              f" ({'dispatched' if dispatch else 'backlog'})")
    return 0


if __name__ == "__main__":
    sys.exit(main())
