package tui

import (
	"strings"
	"testing"
)

// The welcome step shows lazygit and one line for the language servers, and
// the i key lists install commands for what is missing without running them.
func TestPrereqLazygitAndLanguageServers(t *testing.T) {
	fakeMachine(t, "git", "gopls")
	ps := checkPrereqs()
	var lazy bool
	for _, p := range ps {
		if p.Name == "lazygit" {
			lazy = true
			if p.Required {
				t.Fatal("lazygit must be optional")
			}
		}
	}
	if !lazy {
		t.Fatal("lazygit row missing")
	}
	lines := plain(strings.Join(prereqLines(mustTheme(t, "scribe"), ps, "linux", 80), "\n"))
	if !strings.Contains(lines, "Language servers  gopls") {
		t.Fatalf("found language server not listed: %q", lines)
	}
	hints := strings.Join(missingHints(ps, "linux"), "\n")
	if !strings.Contains(hints, "lazygit:") || strings.Contains(hints, "gopls:") {
		t.Fatalf("install hints wrong: %q", hints)
	}
}
