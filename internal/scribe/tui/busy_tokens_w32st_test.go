package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// The busy line's token count is the rough estimate of everything the model
// is producing this turn, at 3.5 characters a token (spec 01 §2.13). Streamed
// reasoning is produced output too, so it must move the count as text does.
func TestBusyLineCountsStreamedReasoning(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	tb := m.cur()
	now := time.Now()
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvTurnStart, Prompt: "explain", At: now}})

	// 3,500 characters of reasoning, streamed in small deltas as Ollama sends
	// them: 1,000 tokens at 3.5 characters each.
	chunk := strings.Repeat("r", 100)
	for i := 0; i < 35; i++ {
		m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvReasoning, Text: chunk, At: now}})
	}
	line := ansi.Strip(m.busyLine(tb))
	if !strings.Contains(line, "~1.0k tokens") {
		t.Fatalf("busy line does not count the streamed reasoning, got %q", line)
	}

	// Text adds to the same count.
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvText, Text: strings.Repeat("t", 35), At: now}})
	line = ansi.Strip(m.busyLine(tb))
	if !strings.Contains(line, "~1.0k tokens") {
		t.Fatalf("text did not add to the reasoning count, got %q", line)
	}

	// A new turn starts the count again.
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{}, At: now}})
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvTurnStart, Prompt: "again", At: now}})
	if line := ansi.Strip(m.busyLine(tb)); strings.Contains(line, "tokens") {
		t.Fatalf("a new turn still shows the old count: %q", line)
	}
}
