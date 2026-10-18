package tui

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

func TestClaudeContextRowsOffByDefault(t *testing.T) {
	rows := (&settings{}).claudeContextRows(config.ContextManagement{})
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want preserve thinking and clear tool uses", len(rows))
	}
	want := map[string]string{
		"context_management.clear_thinking":  "Preserve thinking",
		"context_management.clear_tool_uses": "Clear old tool uses",
	}
	for _, r := range rows {
		if want[r.key] != r.label {
			t.Errorf("row %q has key %q", r.label, r.key)
		}
		if r.value != "off" || r.on || !r.toggle {
			t.Errorf("row %q value=%q on=%v toggle=%v, want an off switch", r.label, r.value, r.on, r.toggle)
		}
	}
}

func TestClaudeContextRowsShowSetValues(t *testing.T) {
	on := true
	rows := (&settings{}).claudeContextRows(config.ContextManagement{ClearThinking: &on, ClearToolUses: &on})
	for _, r := range rows {
		if r.value != "on" || !r.on {
			t.Errorf("row %q value=%q on=%v, want on", r.label, r.value, r.on)
		}
	}
}
