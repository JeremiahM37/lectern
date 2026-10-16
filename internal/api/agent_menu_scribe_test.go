package api

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
)

// TestDefaultMenuDoesNotShrinkToScribe: Scribe is always installed (it is
// this binary). With none of the external agents installed the menu must
// still show every built-in, with Scribe last, not Scribe alone.
func TestDefaultMenuDoesNotShrinkToScribe(t *testing.T) {
	var specs []sessions.Spec
	for _, b := range sessions.Builtins() {
		if b.Name != sessions.ScribeAgent {
			b.Command = "lectern-test-not-installed-" + b.Name
		}
		specs = append(specs, b)
	}
	got := strings.Join(defaultAgentMenu(specs), ",")
	if got != "claude,codex,gemini,scribe" {
		t.Fatalf("menu = %s, want claude,codex,gemini,scribe", got)
	}
}
