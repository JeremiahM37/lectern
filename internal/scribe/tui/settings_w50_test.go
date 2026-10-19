package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// The four config-only Anthropic and OpenAI options each get a row with the
// right key, the default shown, and a hint that says it is off by default.
func TestW50FeatureRowsShowDefaultsAndKeys(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &settings{}
	rows := s.claudeFeatureRows(config.Performance{})
	want := map[string]string{
		"Code execution":    "performance.anthropic_code_execution",
		"Computer use":      "performance.anthropic_computer_use",
		"Server compaction": "performance.anthropic_server_compaction",
	}
	if len(rows) != len(want) {
		t.Fatalf("want %d feature rows, got %d", len(want), len(rows))
	}
	for _, r := range rows {
		if want[r.label] != r.key {
			t.Fatalf("row %q has key %q, want %q", r.label, r.key, want[r.label])
		}
		if r.value != "off" {
			t.Fatalf("row %q shows %q by default, want off", r.label, r.value)
		}
		if !strings.Contains(r.note, "off by default") || !strings.Contains(r.note, "not live-tested") {
			t.Fatalf("row %q hint %q must say off by default and not live-tested", r.label, r.note)
		}
	}

	on := config.Performance{
		AnthropicCodeExecution:    boolPtr(true),
		AnthropicComputerUse:      boolPtr(true),
		AnthropicServerCompaction: boolPtr(true),
	}
	for _, r := range s.claudeFeatureRows(on) {
		if r.value != "on" {
			t.Fatalf("row %q shows %q when set, want on", r.label, r.value)
		}
	}
}

// The reasoning summary row shows off by default and cycles through the
// four values, with off stored as the empty string.
func TestW50ReasoningSummaryRowShowsChoices(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := &settings{}
	r := s.openAIReasoningSummaryRow(config.Performance{})
	if r.key != "performance.openai_reasoning_summary" || r.value != "off" {
		t.Fatalf("default row key=%q value=%q, want its key and off", r.key, r.value)
	}
	for _, v := range []string{"auto", "concise", "detailed"} {
		r = s.openAIReasoningSummaryRow(config.Performance{OpenAIReasoningSummary: v})
		if r.value != v {
			t.Fatalf("row shows %q, want %q", r.value, v)
		}
	}
	if !strings.Contains(r.note, "off by default") || !strings.Contains(r.note, "not live-tested") {
		t.Fatalf("hint %q must say off by default and not live-tested", r.note)
	}
}

func boolPtr(b bool) *bool { return &b }
