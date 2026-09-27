#!/usr/bin/env python3
"""A task.finished hook.

Lectern writes the event to stdin as JSON: {"event", "plugin", "time",
"project_id", "data"}, where data is the task. Print one JSON object:
{"ok": bool, "message": str, "notify": str}. "notify" is sent to your
notification channels because this plugin declares the notify capability.
"""
import json
import os
import sys
import time

event = json.load(sys.stdin)
task = event.get("data") or {}

state = os.environ.get("XDG_STATE_HOME") or os.path.join(os.environ.get("HOME", "/tmp"), ".local", "state")
log_dir = os.path.join(state, "lectern-hello")
os.makedirs(log_dir, exist_ok=True)
with open(os.path.join(log_dir, "events.log"), "a", encoding="utf-8") as log:
    log.write("%s task %s %s: %s\n" % (time.strftime("%Y-%m-%d %H:%M:%S"), task.get("id"), task.get("status"), task.get("title", "")))

print(json.dumps({
    "ok": True,
    "message": "logged task %s" % task.get("id"),
    "notify": "Task %s finished: %s" % (task.get("id"), task.get("title", "")),
}))
