package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// A finished edit row shows its own first output line, cut to 40, in place of
// the verb and path (spec 01 §3.4). The +N -M counts live in the diff box.
func TestEditRowShowsFirstLineCutTo40(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "edit", At: at(c)})
	tool(v, "e1", "edit_file", map[string]any{"path": "main.go"}, c)
	result(v, "e1", "edit_file", tools.Result{
		Output:  "Edited main.go: replaced the first occurrence of a long phrase.",
		Summary: "+1 -1",
	}, time.Millisecond, c)
	v.Apply(agent.Event{Kind: agent.EvTurnDone, At: at(c), Stats: &agent.TurnStats{}})
	out := plain(v)
	if !strings.Contains(out, "Edited main.go: replaced the first oc...") {
		t.Fatalf("edit row should show the first output line cut to 40:\n%s", out)
	}
	if strings.Contains(out, "replaced the first occurrence") {
		t.Fatalf("the row should not show the full line:\n%s", out)
	}
}

// A failed edit keeps the 60-character clip that spec 01 §3.4 and §3.6 give
// error suffixes.
func TestEditErrorSuffixStaysAt60(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "edit", At: at(c)})
	tool(v, "e1", "edit_file", map[string]any{"path": "main.go"}, c)
	result(v, "e1", "edit_file", tools.Result{Output: strings.Repeat("x", 80), IsError: true}, time.Millisecond, c)
	v.Apply(agent.Event{Kind: agent.EvTurnDone, At: at(c), Stats: &agent.TurnStats{}})
	out := plain(v)
	if !strings.Contains(out, "→ "+strings.Repeat("x", 57)+"...") || strings.Contains(out, strings.Repeat("x", 58)) {
		t.Fatalf("error suffix should be cut at 60:\n%s", out)
	}
}
