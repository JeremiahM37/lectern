package agentevents

import "github.com/JeremiahM37/lectern/v2/internal/shellq"

// Codex 0.155.1's hooks support, as probed for this pass (see
// docs/agent-events.md section 2 and the final report for how):
//
//   - `codex --help`/`codex exec --help` both list `--dangerously-bypass-hook-trust`
//     ("Run enabled hooks without requiring persisted hook trust... Intended
//     only for automation that already vets hook sources"), and the shipped
//     binary embeds full JSON Schemas for hook input/output on SessionStart,
//     UserPromptSubmit, PreToolUse, PostToolUse, PreCompact, PostCompact,
//     SessionEnd, SubagentStart, SubagentStop, PermissionRequest, Stop and
//     Interrupt — byte-for-byte the same field names, enums and
//     hookSpecificOutput/permissionDecision shapes Claude Code uses. A
//     PermissionRequest hook's answer is exactly the contract's
//     `{"hookSpecificOutput":{"hookEventName":"PermissionRequest","decision":
//     {"behavior":"allow"|"deny","message":"..."}}}`.
//   - Handlers are COMMAND-based, not Claude's `type: "http"`
//     (codex_hooks::engine::command_runner invokes them via `sh -lc`); there
//     is no confirmed native HTTP hook handler for codex.
//   - What was NOT nailed down within this pass's budget: the exact on-disk
//     hooks.json registration schema (event name -> handler-list shape) and
//     its project-level discovery path (a plugin's `plugin.json` can point
//     `"hooks": "./hooks.json"`, and a `.tmp/plugins/.../hooks/hooks.json`
//     string suggests a project-local file, but this was not confirmed
//     end-to-end against a live turn — doing so needs a real ChatGPT-auth'd
//     turn, which this offline pass could not run). Wiring hooks.json for
//     PreToolUse/PostToolUse/Stop/etc. is left to a follow-up worker; the
//     HTTP ingest side (IngestEvent) already accepts every one of these
//     event names today and needs no change when that lands.
//
// What IS wired for this pass is codex's older, independently-confirmed
// `notify` mechanism: `-c notify=["python3","<script>"]` runs <script> with
// the turn's JSON as its last argv element on agent-turn-complete
// (`--strict-config` accepted `-c 'notify=["/bin/true"]'` outright, and the
// binary's own strings list the exact notify payload field names:
// thread-id, turn-id, cwd, client, input-messages, last-assistant-message).
// That alone is enough to cover docs/agent-events.md's
// AgentTurnComplete -> idle mapping without depending on the unconfirmed
// hooks.json shape, at the cost of the richer per-tool-call signals hooks.json
// would give: session state still falls back to screen-scraping for a codex
// session between turns, exactly as it does for any agent with no hooks at
// all (poll.go's applyPane).

// CodexNotifyScript is the notify program lectern installs on a codex
// session's target. It is a fixed script with no per-session templating —
// everything it needs (LECTERN_HOOK_TOKEN, LECTERN_HOOK_URL) arrives as
// process environment, exactly like Claude's statusline wrapper — so it is
// written once per session under ~/.lectern/hooks/<tmux>-codex-notify.py and
// referenced from the launch command's `-c notify=[...]` override.
//
// Degrades silently on any error: a notify script must never make codex
// think a turn failed, and lectern being briefly unreachable is not the
// agent's problem. If curl is missing, subprocess.run raises and is caught.
const CodexNotifyScript = `#!/usr/bin/env python3
"""lectern codex notify handler — see internal/agentevents/codex_settings.go.

Invoked by codex as ` + "`notify <json>`" + ` on agent-turn-complete.
"""
import json
import os
import subprocess
import sys

TOKEN = os.environ.get("LECTERN_HOOK_TOKEN", "")
URL = os.environ.get("LECTERN_HOOK_URL", "").rstrip("/")


def main() -> int:
    if not TOKEN or not URL:
        return 0
    try:
        payload = json.loads(sys.argv[-1]) if len(sys.argv) > 1 else {}
    except Exception:
        payload = {}
    body = json.dumps({"hook_event_name": "AgentTurnComplete", "codex_notify": payload}).encode()
    try:
        subprocess.run(
            ["curl", "-s", "-m", "5", "-X", "POST",
             "-H", "Authorization: Bearer " + TOKEN,
             "-H", "Content-Type: application/json",
             "--data-binary", "@-", URL + "/AgentTurnComplete"],
            input=body, timeout=6,
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
    except Exception:
        pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
`

// CodexNotifyInstallCommand returns a shell command that writes
// CodexNotifyScript under ~/.lectern/hooks/<tmux>-codex-notify.py (mode
// 0700) and prints its absolute path, for use as the second element of a
// `-c notify=[...]` config override.
func CodexNotifyInstallCommand(tmuxName string) string {
	path := "$HOME/.lectern/hooks/" + tmuxName + "-codex-notify.py"
	return "mkdir -p \"$HOME/.lectern/hooks\" && cat > " + shellq.Quote(path) +
		" <<'ADKCODEXNOTIFY'\n" + CodexNotifyScript + "ADKCODEXNOTIFY\n" +
		"chmod 700 " + shellq.Quote(path) + " && printf '%s' " + shellq.Quote(path)
}
