package drivers

import "testing"

func TestSelect(t *testing.T) {
	cases := []struct {
		name           string
		agent          string
		builtin        bool
		permissionMode string
		want           string
	}{
		{"claude default", "claude", true, "acceptEdits", KindClaudeExec},
		{"claude steerable", "claude", true, "steerable", KindClaudeSteer},
		{"claude gated stays exec (hook-based, unchanged)", "claude", true, "default", KindClaudeExec},
		{"codex bypass", "codex", true, "bypassPermissions", KindCodexExec},
		{"codex gated selects app-server", "codex", true, "default", KindCodexAppServer},
		{"codex steerable is not a thing; falls through to exec", "codex", true, "steerable", KindCodexExec},
		{"gemini", "gemini", true, "acceptEdits", KindGemini},
		{"custom CLI is always generic", "mycli", false, "steerable", KindGeneric},
		{"unknown builtin name", "cursor", true, "acceptEdits", KindGeneric},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Select(c.agent, c.builtin, c.permissionMode); got != c.want {
				t.Errorf("Select(%q, %v, %q) = %q, want %q", c.agent, c.builtin, c.permissionMode, got, c.want)
			}
		})
	}
}
