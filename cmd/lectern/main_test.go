package main

import "testing"

// TestAgentQuickVerbsNeverShadowExistingSubcommands proves the one-command
// agent launchers (`lectern claude`, `lectern codex`) can never silently take
// over a name main.go's own switch or localCommand's subcommands already
// claim. Existing subcommands must always win.
func TestAgentQuickVerbsNeverShadowExistingSubcommands(t *testing.T) {
	for name := range agentQuickVerbs {
		if clientVerbs[name] {
			t.Errorf("agent-quick verb %q duplicates a clientVerbs entry", name)
		}
		if reservedVerbs[name] {
			t.Errorf("agent-quick verb %q collides with a reserved top-level subcommand", name)
		}
	}
}

// TestDispatchTable exercises the same boolean main.go actually branches on
// (clientVerbs[arg] || agentQuickVerbs[arg]) for every name the CLI knows
// about, so a future edit to either set is caught here instead of only at
// runtime.
func TestDispatchTable(t *testing.T) {
	cases := []struct {
		arg              string
		wantClientDialed bool // routed through clientCommand/localCommand's shared dispatch
	}{
		{"console", true}, {"tui", true}, {"shell", true}, {"api", true},
		{"agent", true}, {"upload", true}, {"files", true}, {"download", true},
		{"post", true}, {"live", true}, {"expose", true}, {"skill", true},
		{"promote", true}, {"controls", true}, {"help", true}, {"--help", true}, {"-h", true},
		{"claude", true}, {"codex", true},
		{"local", false}, {"up", false}, {"doctor", false}, {"serve", false},
		{"attach", false}, {"mcp", false}, {"version", false}, {"--version", false}, {"-v", false},
		{"nonexistent-command", false},
	}
	for _, c := range cases {
		got := clientVerbs[c.arg] || agentQuickVerbs[c.arg]
		if got != c.wantClientDialed {
			t.Errorf("dispatch(%q) routed-through-clientVerbs = %v, want %v", c.arg, got, c.wantClientDialed)
		}
	}
}

// TestLocalClientCommandsIncludesAgentQuickVerbs: `lectern local claude` and
// `lectern local codex` must work the same way every other client command
// does under `lectern local`, per local_cli.go's localClientCommands.
func TestLocalClientCommandsIncludesAgentQuickVerbs(t *testing.T) {
	for name := range agentQuickVerbs {
		if !localClientCommands[name] {
			t.Errorf("localClientCommands is missing agent-quick verb %q", name)
		}
	}
}
