package sessions

import "testing"

// Only Claude Code and forks that read CLAUDE_CODE_DISABLE_MOUSE get it,
// whether they are named for it or only run its binary.
func TestOnlyClaudeCodeIsToldToLeaveTheMouse(t *testing.T) {
	openclaude, _ := FindCatalogPreset("openclaude")
	for spec, want := range map[*Spec]bool{
		{Name: "claude", Command: "claude"}:                   true,
		&openclaude.Spec:                                      true,
		{Name: "work-claude", Command: "/opt/bin/claude --x"}: true,
		{Name: "codex", Command: "codex"}:                     false,
		{Name: "gemini", Command: "gemini"}:                   false,
		{Name: "claudeish", Command: "claudeish"}:             false,
	} {
		if got := honoursDisableMouse(*spec); got != want {
			t.Errorf("%s (%s): got %v, want %v", spec.Name, spec.Command, got, want)
		}
	}
}
