package main

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/console"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
)

// demoCommand is `lectern demo`: the web app's "Try a demo agent", in the
// terminal. It starts the stand-in agent (internal/sessions/demo.go) in a
// throwaway folder — or comes back to the one already running — and attaches,
// so someone with no agent CLI installed can see sessions, approvals and
// review work end to end.
func demoCommand(cfg *config.Config, args []string, base, token string, local bool, out io.Writer) error {
	fresh := false
	for _, a := range args {
		switch a {
		case "--new":
			fresh = true
		default:
			return fmt.Errorf("lectern demo: unsupported argument %q (supported: --new)", a)
		}
	}
	c := console.New(base, token)
	sess, reused, err := startDemoSession(c, fresh)
	if err != nil {
		return err
	}
	if reused {
		fmt.Fprintf(out, "Back to the demo agent (session #%d).\n", sess.ID)
	} else {
		fmt.Fprintf(out, "Started the demo agent (session #%d). It needs nothing installed and uses no AI.\n", sess.ID)
	}
	fmt.Fprintln(out, "Type a message and press Enter. Before it writes a file it asks you: press Ctrl+] y to allow it.")
	fmt.Fprintln(out, "Ctrl+] d leaves it running; lectern demo brings you back. For a real agent, install Claude Code or Codex and run lectern claude or lectern codex.")
	if !interactiveTerminal() {
		fmt.Fprintf(out, "Attach with: lectern attach session %d\n", sess.ID)
		return nil
	}
	return attachAgentSession(cfg, base, token, local, sess.ID)
}

// startDemoSession reuses a running demo session unless fresh is set, and
// otherwise starts one exactly as the web app's demo button does.
func startDemoSession(c *console.Client, fresh bool) (*quickSessionView, bool, error) {
	if !fresh {
		data, err := c.JSON("GET", "/sessions", nil)
		if err != nil {
			return nil, false, err
		}
		var rows []quickSessionView
		if err := json.Unmarshal(data, &rows); err != nil {
			return nil, false, err
		}
		for i := range rows {
			if rows[i].Agent == sessions.DemoAgent {
				return &rows[i], true, nil
			}
		}
	}
	data, err := c.JSON("POST", "/sessions", map[string]any{"agent": sessions.DemoAgent, "scratch": true, "name": "demo"})
	if err != nil {
		return nil, false, fmt.Errorf("start the demo agent: %w", err)
	}
	var created quickSessionView
	if err := json.Unmarshal(data, &created); err != nil {
		return nil, false, err
	}
	return &created, false, nil
}

