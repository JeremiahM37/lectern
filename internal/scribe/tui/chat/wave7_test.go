package chat

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

// A continuation of an interrupted turn extends the same row: it opens no
// second, empty assistant row under its own header.
func TestContinuationExtendsTheInterruptedRow(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddUser("refactor it", 0, at(c), false)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "refactor it", At: at(c)})
	text(v, "I will start with", c)
	// The process stops here: no turn_done. Resume, then the continuation runs.
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "", At: at(c)})
	text(v, " the parser.", c)
	v.Apply(agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{}, At: at(c)})

	assistants := 0
	for _, r := range v.msgs {
		if r.Role == roleAssistant {
			assistants++
		}
	}
	if assistants != 1 {
		t.Fatalf("continuation made %d assistant rows, want 1", assistants)
	}
	out := plain(v)
	if n := strings.Count(out, "Scribe"); n != 1 {
		t.Fatalf("header shown %d times:\n%s", n, out)
	}
	if !strings.Contains(out, "I will start with") || !strings.Contains(out, "the parser") {
		t.Fatalf("continuation text missing:\n%s", out)
	}
}

// An unfinished row with nothing in it draws no bare header.
func TestEmptyUnfinishedRowHasNoHeader(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddUser("hi", 0, at(c), false)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "", At: at(c)})
	v.closeLive()
	if n := strings.Count(plain(v), "Scribe"); n != 0 {
		t.Fatalf("empty unfinished row shows a header:\n%s", plain(v))
	}
}
