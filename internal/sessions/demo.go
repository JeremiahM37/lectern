package sessions

import "github.com/JeremiahM37/lectern/v2/internal/shellq"

// DemoAgent is the stand-in agent behind "Try a demo agent" in the web app's
// first-run screen (docs/design/simple-ui.md): something a person with no
// agent CLI installed can start, talk to and review, to see how Lectern works.
// It uses no AI and needs only a POSIX shell. It answers each message and
// appends it to demo-notes.md, so the session goes from working to idle and
// leaves a change to look at in Review.
const DemoAgent = "demo"

const demoScript = `printf '\033[1mDemo agent\033[0m: a stand-in that needs nothing installed and uses no AI.\n'; ` +
	`printf 'Type a message and press Enter. It answers and writes your message to demo-notes.md,\n'; ` +
	`printf 'so you can try chat, status and reviewing a change. Install Claude Code or Codex for a real agent.\n\n'; ` +
	`while printf '> ' && IFS= read -r line; do ` +
	`[ -z "$line" ] && continue; ` +
	`printf 'Working on it...\n'; sleep 2; ` +
	`printf -- '- %s\n' "$line" >> demo-notes.md; ` +
	`printf 'Done: I added that to demo-notes.md. Open Review to see the change.\n\n'; ` +
	`done`

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
