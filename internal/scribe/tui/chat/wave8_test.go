package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// retry feeds one retry notice the way the agent loop emits it.
func retry(v *View, attempt int, c *clock) {
	v.Apply(agent.Event{Kind: agent.EvRetry, Category: llm.CatStream, Attempt: attempt, MaxAttempts: 3,
		Delay: 2 * time.Second, Text: "stream broke", At: at(c)})
}

// hasSpinner reports whether a braille spinner frame is drawn anywhere.
func hasSpinner(s string) bool {
	for _, r := range s {
		if r >= 0x2800 && r <= 0x28FF {
			return true
		}
	}
	return false
}

// A retry notice is live while its attempt is pending, and folds into the
// turn once a later attempt streams text.
func TestRetryNoticeCollapsesWhenAttemptSucceeds(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "", At: at(c)})
	retry(v, 1, c)
	if out := plain(v); !strings.Contains(out, "Retry 1/3 — Stream Error") {
		t.Fatalf("pending retry should show while the attempt waits:\n%s", out)
	}
	text(v, "The answer.", c)
	v.Apply(agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{Duration: 3 * time.Second}, At: at(c)})
	out := plain(v)
	if strings.Contains(out, "Retry") {
		t.Fatalf("retry notice left in the finished transcript:\n%s", out)
	}
	if !strings.Contains(out, "The answer.") || !strings.Contains(out, "1 retry") {
		t.Fatalf("finished turn should keep the answer and a quiet retry note:\n%s", out)
	}
	checkShape(t, v)
}

// A turn that ends without any progress after a retry also drops the notice.
func TestRetryNoticeGoneWhenTurnEnds(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "", At: at(c)})
	retry(v, 1, c)
	v.Apply(agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{}, At: at(c)})
	if out := plain(v); strings.Contains(out, "Retry") {
		t.Fatalf("retry notice survived turn end:\n%s", out)
	}
}

// Two retries in a row: only the newest notice is up; the earlier one is
// counted, and both are gone once the turn finishes.
func TestOnlyNewestRetryStaysUp(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "", At: at(c)})
	retry(v, 1, c)
	retry(v, 2, c)
	out := plain(v)
	if strings.Contains(out, "Retry 1/3") || !strings.Contains(out, "Retry 2/3") {
		t.Fatalf("want only the newest retry while waiting:\n%s", out)
	}
	text(v, "ok", c)
	v.Apply(agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{Duration: time.Second}, At: at(c)})
	out = plain(v)
	if strings.Contains(out, "Retry") || !strings.Contains(out, "2 retries") {
		t.Fatalf("finished turn should count both retries and show no notice:\n%s", out)
	}
}

// A turn that ends after tool results with no final text reads as done: no
// empty-answer line, no spinner, and a Done footer.
func TestTurnEndingOnToolResultsReadsDone(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "fix it", At: at(c)})
	tool(v, "t1", "edit_file", map[string]any{"path": "a.go"}, c)
	result(v, "t1", "edit_file", tools.Result{Output: "edited a.go"}, time.Second, c)
	v.Apply(agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{}, At: at(c)})
	out := plain(v)
	if strings.Contains(out, "Empty response") {
		t.Fatalf("tool-only turn shows an empty answer:\n%s", out)
	}
	if hasSpinner(out) || strings.Contains(out, "Thinking") {
		t.Fatalf("finished tool-only turn still animates:\n%s", out)
	}
	if !strings.Contains(out, "✓ Done") {
		t.Fatalf("tool-only turn has no Done summary:\n%s", out)
	}
	checkShape(t, v)
}

// A cancelled subagent draws the ⊘ glyph, not the pending ○, and the
// dispatch header counts it as cancelled rather than as finished.
func TestCancelledSubagentGlyphAndCount(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "d1", "dispatch", map[string]any{"tasks": []any{
		map[string]any{"task": "map the store", "role": "explore"},
		map[string]any{"task": "write the test", "role": "code"},
	}}, c)
	v.Apply(agent.Event{Kind: agent.EvSubagent, CallID: "d1", At: at(c), Agent: &agent.AgentInfo{
		ID: "a1", Task: "map the store", Role: "explore", Status: "cancelled"}})
	v.Apply(agent.Event{Kind: agent.EvSubagent, CallID: "d1", At: at(c), Agent: &agent.AgentInfo{
		ID: "a2", Task: "write the test", Role: "code", Status: "done"}})
	out := plain(v)
	if !strings.Contains(out, "⊘ a1 explore") {
		t.Fatalf("cancelled subagent should draw ⊘:\n%s", out)
	}
	if strings.Contains(out, "○ a1") {
		t.Fatalf("cancelled subagent still draws the pending glyph:\n%s", out)
	}
	result(v, "d1", "dispatch", tools.Result{Output: "ok"}, time.Second, c)
	if out := plain(v); !strings.Contains(out, "1/2 agents · 1 cancelled") {
		t.Fatalf("dispatch summary should count the cancelled agent:\n%s", out)
	}
}
