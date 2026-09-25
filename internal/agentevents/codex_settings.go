package agentevents

import (
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// Codex 0.156.1's hooks support, CONFIRMED end-to-end against the real
// binary (see docs/agent-events.md section 2 for the full trace). A prior
// pass left the on-disk registration schema and discovery path unconfirmed;
// this pass ran `codex exec --dangerously-bypass-hook-trust` twice against a
// scratch CODEX_HOME (auth.json copied in, nothing else) with a hand-written
// hooks.json and traced every hook invocation end to end:
//
//   - Discovery: `$CODEX_HOME/hooks.json` (default `~/.codex/hooks.json`) is
//     loaded automatically — no config.toml entry, plugin manifest or CLI
//     flag is needed. Confirmed by placing ONLY auth.json + hooks.json in a
//     fresh CODEX_HOME (no config.toml at all) and seeing every hook fire.
//     There is no per-project `.codex/hooks.json` and no CLI flag analogous
//     to Claude's `--settings <path>` to point at an alternate file — the
//     real, pre-existing `~/.codex/hooks.json` on this machine (installed by
//     an unrelated third-party tool, "aoe") is proof the file is genuinely
//     global to CODEX_HOME, not per session.
//   - Schema (confirmed by writing it and watching codex read it):
//     `{"hooks": {"<EventName>": [{"matcher"?: "...", "hooks": [{"type":
//     "command", "command": "<sh -lc string>"}]}]}}` — an event maps to a
//     LIST of hook groups (so multiple tools/installs can coexist under the
//     same event without clobbering each other, which is exactly how the
//     pre-existing "aoe" hooks.json and lectern's own entries now sit side
//     by side), each group optionally scoped by `matcher` (omitted = match
//     every tool, verified: PreToolUse/PostToolUse fired for a plain shell
//     exec with no matcher present).
//   - Handlers are COMMAND-based (`sh -lc "<command>"`), not Claude's
//     `type: "http"` — confirmed no other handler `type` fires; a bare
//     shell command receives the hook JSON on stdin and its stdout, if
//     non-empty valid JSON, is read back as the hook's response.
//   - Events fired and payload shapes, captured verbatim from a real run
//     (`SessionStart` -> `UserPromptSubmit` -> `PreToolUse` -> `PostToolUse`
//     -> `Stop`, for a prompt that ran one shell command): every event
//     carries `session_id`, `transcript_path`, `cwd`, `hook_event_name`,
//     `model`, `permission_mode` ("bypassPermissions" in the default exec
//     sandbox). `UserPromptSubmit` adds `turn_id`/`prompt`. `PreToolUse`/
//     `PostToolUse` add `tool_name` ("Bash" for a shell command — the same
//     name Claude uses), `tool_input`, `tool_use_id`, and `PostToolUse` adds
//     `tool_response`. `Stop` adds `stop_hook_active`/`last_assistant_message`.
//     Byte-for-byte the field names docs/agent-events.md already assumed
//     from Claude parity — `agentevents.IngestEvent`/`MapEventState` needed
//     NO changes to accept these.
//   - `additionalContext` round-trip CONFIRMED for real: a `SessionStart`
//     hook that printed
//     `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":"The magic phrase is XYZZY137."}}`
//     on stdout caused the agent's own reply to state "XYZZY137" back — i.e.
//     the cross-agent awareness briefing (section 5) reaches a codex session
//     exactly the way it reaches Claude, once this hook is installed.
//   - `PermissionRequest` payload/response shape matches the contract
//     (confirmed via the binary's embedded JSON Schema — see the prior
//     pass's note, unchanged), but it could NOT be made to fire from
//     `codex exec`: exec forces `approval: never` regardless of
//     `-c approval_policy=...` (there is no interactive surface to ask, so
//     it always runs full-auto). It is documented, schema-confirmed, and
//     wired below for the interactive/app-server path, which does support
//     `on-request` approval — see docs/agent-events.md section 3's codex
//     note for exactly what is and is not proven here.
//   - Trust: an on-disk hooks.json's commands are hashed and the hash is
//     cached in config.toml's `[hooks.state]` (`trusted_hash = "sha256:..."`,
//     keyed by `<hooks.json path>:<event_snake_case>:<group idx>:<hook
//     idx>`) after an interactive trust prompt. `--dangerously-bypass-hook-trust`
//     (confirmed to exist for exactly this) skips that prompt; it is passed
//     on every builtin-codex launch below rather than reverse-engineering
//     the hash algorithm, since lectern wrote the hooks it is bypassing
//     trust for — the flag's own description ("automation that already
//     vets hook sources") is precisely this case.
//
// Also still wired, unchanged: codex's older `notify` mechanism
// (`-c notify=["python3","<script>"]`, agent-turn-complete -> idle). It is
// kept as a second, independent idle signal now that hooks.json's `Stop`
// event covers the same transition with richer data (last_assistant_message)
// — harmless redundancy, and it costs nothing to leave running.

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
//
// The target path is built with shellq.HomePath, not a bare
// `shellq.Quote("$HOME/"+...)`: Quote's safeWord charset excludes `$`, so
// quoting a string that already contains "$HOME" text single-quotes the
// whole thing and disables the expansion — confirmed against a real shell,
// see HomePath's own doc for the reproduction. That was the actual rendered
// form here before this fix.
func CodexNotifyInstallCommand(tmuxName string) string {
	path := shellq.HomePath("/.lectern/hooks/" + tmuxName + "-codex-notify.py")
	return "mkdir -p " + shellq.HomePath("/.lectern/hooks") + " && cat > " + path +
		" <<'ADKCODEXNOTIFY'\n" + CodexNotifyScript + "ADKCODEXNOTIFY\n" +
		"chmod 700 " + path + " && printf '%s' " + path
}

// CodexHookTrustBypassArg is the launch flag added for every builtin codex
// session once CodexHooksInstallCommand has run: see this file's package doc
// for why bypassing persisted hook trust, rather than reverse-engineering
// codex's hash cache, is the deliberate choice here.
const CodexHookTrustBypassArg = "--dangerously-bypass-hook-trust"

// codexHookMarker is embedded in every command lectern writes into
// hooks.json, and is what CodexHooksInstallCommand's merge step uses to find
// (and replace or remove) lectern's own entries on a later call without
// touching any other tool's groups under the same event — see the real
// pre-existing `~/.codex/hooks.json` on this machine (an unrelated tool,
// "aoe") for proof multiple installers must coexist under one event's array.
const codexHookMarker = "lectern-codex-hook.py"

// codexHookEvents lists every event lectern ever registers, mapped to that
// hook invocation's own timeout (seconds) — mirrors
// agentevents.ClaudeSettingsInstallCommand's per-event timeouts: fast on the
// PreToolUse/PostToolUse hot path, a little more slack for the once-per-turn
// events, and PermissionRequest given headroom above the 120s default
// LECTERN_APPROVAL_HOLD so lectern's own hold — not this hook's client
// timeout — is what decides an unanswered approval.
var codexHookEvents = []struct {
	Name    string
	Timeout int
}{
	{"SessionStart", 8},
	{"UserPromptSubmit", 8},
	{"PreToolUse", 3},
	{"PostToolUse", 3},
	{"Stop", 8},
	{"SessionEnd", 8},
	{"PreCompact", 8},
	{"PermissionRequest", 130},
}

// CodexHookScript is the fixed, non-templated program every managed hooks.json
// entry below invokes. Like CodexNotifyScript, it carries no per-session
// state of its own — LECTERN_HOOK_TOKEN/LECTERN_HOOK_URL arrive as process
// environment (confirmed reachable: a hook command's subprocess inherits the
// full codex process environment, no Claude-style allowedEnvVars allowlist
// needed) — so ONE copy of this file, written once per target, serves every
// codex session on it; only the environment differs per invocation.
//
// argv[1] is the hook event name, argv[2] its timeout in seconds. The hook
// body arrives on stdin and is forwarded byte-for-byte as the POST body
// (never re-encoded, so lectern's ingest sees exactly what codex sent).
// lectern's JSON response is printed back to stdout verbatim — this is what
// lets `additionalContext` (awareness) and PermissionRequest's `decision`
// reach codex, confirmed for real (see this file's package doc). On any
// failure (no curl, lectern unreachable, timeout) it prints "{}" and always
// exits 0: a hook that errors or times out must never be mistaken by codex
// for a request to block the agent's turn or tool call.
const CodexHookScript = `#!/usr/bin/env python3
"""lectern codex hook handler (` + codexHookMarker + `) — see
internal/agentevents/codex_settings.go.

Invoked by codex per hooks.json, as "<this file> <event> <timeout>", with the
hook JSON on stdin.
"""
import os
import subprocess
import sys

TOKEN = os.environ.get("LECTERN_HOOK_TOKEN", "")
URL = os.environ.get("LECTERN_HOOK_URL", "").rstrip("/")


def main() -> int:
    event = sys.argv[1] if len(sys.argv) > 1 else ""
    try:
        timeout = int(sys.argv[2]) if len(sys.argv) > 2 else 8
    except ValueError:
        timeout = 8
    if not TOKEN or not URL or not event:
        print("{}")
        return 0
    body = sys.stdin.buffer.read()
    try:
        result = subprocess.run(
            ["curl", "-s", "-m", str(timeout), "-X", "POST",
             "-H", "Authorization: Bearer " + TOKEN,
             "-H", "Content-Type: application/json",
             "--data-binary", "@-", URL + "/" + event],
            input=body, timeout=timeout + 2, capture_output=True,
        )
        out = result.stdout.decode("utf-8", "replace").strip()
        print(out if out else "{}")
    except Exception:
        print("{}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
`

// codexHooksInstallPy takes one positional arg, "1"/"0" for whether
// PermissionRequest should be registered (permission mode "ask"), and merges
// lectern's hook groups into $CODEX_HOME/hooks.json (default ~/.codex/hooks.json
// — see this file's package doc for why that path, not a per-session file,
// is what codex actually discovers). The merge preserves every other tool's
// entries and every other top-level key in the document; it only ever adds,
// replaces or removes groups it can prove (via codexHookMarker, above) it
// installed itself, so running this on every launch is idempotent and safe
// to interleave with the pre-existing "aoe"-style hooks.json this machine
// already carries.
//
// Written through a temp file and rename for the same reason claudeTrust and
// claudeSettingsInstallPy are: never leave a half-written JSON file for codex
// to trip over.
const codexHooksInstallPy = `import json, os, shlex, sys, tempfile

ask = sys.argv[1] == "1"
script_path = sys.argv[2]

home = os.path.expanduser("~")
hooks_dir = os.path.join(home, ".lectern", "hooks")
codex_home = os.environ.get("CODEX_HOME") or os.path.join(home, ".codex")
os.makedirs(codex_home, mode=0o700, exist_ok=True)
hooks_path = os.path.join(codex_home, "hooks.json")

doc = {}
try:
    with open(hooks_path) as f:
        doc = json.load(f)
    if not isinstance(doc, dict):
        doc = {}
except Exception:
    doc = {}
events = doc.get("hooks")
if not isinstance(events, dict):
    events = {}
doc["hooks"] = events


def is_ours(group):
    for h in group.get("hooks", []) if isinstance(group, dict) else []:
        if isinstance(h, dict) and h.get("type") == "command" and script_path in (h.get("command") or ""):
            return True
    return False


for event, timeout in DESIRED:
    existing = events.get(event, [])
    if not isinstance(existing, list):
        existing = []
    kept = [g for g in existing if not is_ours(g)]
    if event == "PermissionRequest" and not ask:
        if kept:
            events[event] = kept
        else:
            events.pop(event, None)
        continue
    command = "python3 " + shlex.quote(script_path) + " " + event + " " + str(timeout)
    group = {"hooks": [{"type": "command", "command": command}]}
    events[event] = kept + [group]

fd, tmp = tempfile.mkstemp(dir=hooks_dir)
with os.fdopen(fd, "w") as f:
    json.dump(doc, f, indent=2)
os.replace(tmp, hooks_path)

print(hooks_path)
`

// codexHooksDesiredPy renders codexHookEvents as the Python list-of-tuples
// literal codexHooksInstallPy's `for event, timeout in DESIRED` loop expects.
// Event names are a fixed internal set (no user input), so %q is sufficient
// quoting for a Python string literal too (both use the same escaping for
// this printable ASCII set).
func codexHooksDesiredPy() string {
	s := "["
	for i, e := range codexHookEvents {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("(%q, %d)", e.Name, e.Timeout)
	}
	return s + "]"
}

// CodexHooksInstallCommand returns a shell command that, run on a session's
// target, writes CodexHookScript once to a fixed, shared path
// (~/.lectern/hooks/lectern-codex-hook.py — not templated per session, see
// CodexHookScript's doc) and additively merges lectern's hook groups into
// the real $CODEX_HOME/hooks.json (default ~/.codex/hooks.json), then prints
// that file's path. askPermission registers PermissionRequest; a later call
// with askPermission false removes it again, mirroring
// ClaudeSettingsInstallCommand's "ask" gate. Every other tool's entries and
// every other top-level key in hooks.json are preserved untouched — see this
// file's package doc for why that matters on a machine that already has one.
func CodexHooksInstallCommand(askPermission bool) string {
	ask := "0"
	if askPermission {
		ask = "1"
	}
	// scriptPath is a shell WORD (via shellq.HomePath, not a bare Quote of a
	// "$HOME/..." Go string — see HomePath's doc for why that combination
	// silently breaks) that the shell expands to an absolute path before it
	// ever reaches python's argv, so codexHooksInstallPy's script_path is
	// always a real filesystem path, never unexpanded shell syntax.
	scriptPath := shellq.HomePath("/.lectern/hooks/" + codexHookMarker)
	writeScript := "mkdir -p " + shellq.HomePath("/.lectern/hooks") + " && cat > " + scriptPath +
		" <<'ADKCODEXHOOK'\n" + CodexHookScript + "ADKCODEXHOOK\n" +
		"chmod 700 " + scriptPath
	py := strings.Replace(codexHooksInstallPy, "DESIRED", codexHooksDesiredPy(), 1)
	install := "python3 - " + ask + " " + scriptPath +
		" <<'ADKCODEXHOOKINSTALL'\n" + py + "\nADKCODEXHOOKINSTALL"
	return writeScript + " && " + install
}
