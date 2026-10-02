package sessions

import "github.com/JeremiahM37/lectern/v2/internal/shellq"

// DemoAgent is the stand-in agent behind "Try a demo agent" in the web app's
// first-run screen (docs/design/simple-ui.md): something a person with no
// agent CLI installed can start, talk to and review, to see how Lectern works.
// It uses no AI and needs only a POSIX shell. It answers each message and
// appends it to demo-notes.md, so the session goes from working to idle and
// leaves a change to look at in Review.
const DemoAgent = "demo"

// In ask mode (its first argument is "ask") the demo asks for approval once,
// before its first write, through the same PermissionRequest hook a real
// agent uses, so a new user sees the approval card without installing
// anything. With no answer (no curl, or nobody decided in time) it asks in
// the terminal instead, as Claude Code does.
const demoScript = `printf '\033[1mDemo agent\033[0m: a stand-in that needs nothing installed and uses no AI.\n'; ` +
	`printf 'Type a message and press Enter. It answers and writes your message to demo-notes.md,\n'; ` +
	`printf 'so you can try chat, status, approvals and reviewing a change. Install Claude Code or Codex for a real agent.\n\n'; ` +
	`ask="$1"; ` +
	`while printf '> ' && IFS= read -r line; do ` +
	`[ -z "$line" ] && continue; ` +
	`printf 'Working on it...\n'; sleep 2; ` +
	`if [ "$ask" = ask ]; then ask=; ` +
	`printf 'Asking for approval to write demo-notes.md (answer it in Lectern)...\n'; answer=; ` +
	`if command -v curl >/dev/null 2>&1 && [ -n "$LECTERN_HOOK_URL" ]; then ` +
	`answer=$(curl -s -X POST -H "Authorization: Bearer $LECTERN_HOOK_TOKEN" -H 'Content-Type: application/json' ` +
	`-d '{"hook_event_name":"PermissionRequest","tool_name":"Write","tool_input":{"file_path":"demo-notes.md","description":"The demo agent wants to add your message to demo-notes.md"}}' ` +
	`"$LECTERN_HOOK_URL/PermissionRequest"); fi; ` +
	`case "$answer" in ` +
	`*'"allow"'*) printf 'Approved.\n' ;; ` +
	`*'"deny"'*) printf 'Denied, so I did not write anything. Ask me again to try once more.\n\n'; continue ;; ` +
	`*) printf 'Write to demo-notes.md? [y/N] '; IFS= read -r yn; case "$yn" in [yY]*) ;; *) printf 'Skipped.\n\n'; continue ;; esac ;; ` +
	`esac; fi; ` +
	`printf -- '- %s\n' "$line" >> demo-notes.md; ` +
	`printf 'Done: I added that to demo-notes.md. Open Review & merge to see the change.\n\n'; ` +
	`done`

// demoAsks is the demo's argument for a session that asks before acting.
const demoAsks = "ask"

// DemoSpec is the demo agent's definition. It is not in Builtins: agent
// pickers, the agent registry and Settings never list it. WithDemo adds it
// only where a session is launched.
func DemoSpec() Spec {
	// Spec args are shell words as written (like a configured agent's), so
	// the script goes in quoted.
	return Spec{Name: DemoAgent, Command: "sh", Args: []string{"-c", shellq.Quote(demoScript), "lectern-demo"}}
}

// WithDemo is specs plus the demo agent, unless the operator defined their own
// agent called "demo", which then wins.
func WithDemo(specs []Spec) []Spec {
	if _, ok := Find(specs, DemoAgent); ok {
		return specs
	}
	return append(append([]Spec{}, specs...), DemoSpec())
}
