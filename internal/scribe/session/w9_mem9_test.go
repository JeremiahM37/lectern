package session

import (
	"fmt"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// fakeHistory stands in for the agent's message list.
type fakeHistory struct{ msgs []llm.Message }

func (h *fakeHistory) Messages() []llm.Message     { return append([]llm.Message(nil), h.msgs...) }
func (h *fakeHistory) SetMessages(m []llm.Message) { h.msgs = append([]llm.Message(nil), m...) }

// addTurn appends a prompt and an answer, as the agent does for one turn.
func (h *fakeHistory) addTurn(n int) {
	h.msgs = append(h.msgs, llm.UserText(fmt.Sprintf("prompt %d", n)), llm.AssistantText(fmt.Sprintf("answer %d", n)))
}

func TestUndoTruncatesMessagesAndRedoPutsThemBack(t *testing.T) {
	e := newCP(t)
	h := &fakeHistory{}
	e.cp.SetHistory(h)
	for n := 1; n <= 3; n++ {
		// The agent appends the turn's messages after Begin, so they go in the mutate step.
		e.turn(n, []string{"f.txt"}, func() {
			h.addTurn(n)
			e.write("f.txt", []byte(fmt.Sprintf("v%d", n)), 0o644)
		})
	}
	if len(h.msgs) != 6 {
		t.Fatalf("history = %d messages, want 6", len(h.msgs))
	}
	if _, err := e.cp.Undo(false); err != nil {
		t.Fatalf("undo: %v", err)
	}
	if len(h.msgs) != 4 || h.msgs[3].Text() != "answer 2" {
		t.Fatalf("after undo history has %d messages, last %q; want turn 3 gone", len(h.msgs), h.msgs[len(h.msgs)-1].Text())
	}
	if _, err := e.cp.Redo(false); err != nil {
		t.Fatalf("redo: %v", err)
	}
	if len(h.msgs) != 6 || h.msgs[5].Text() != "answer 3" || h.msgs[4].Text() != "prompt 3" {
		t.Fatalf("after redo history has %d messages; want turn 3 back in order", len(h.msgs))
	}
}

func TestRedoRefusesWhenConversationMovedOn(t *testing.T) {
	e := newCP(t)
	h := &fakeHistory{}
	e.cp.SetHistory(h)
	e.turn(1, []string{"g.txt"}, func() { h.addTurn(1); e.write("g.txt", []byte("one"), 0o644) })
	e.turn(2, []string{"g.txt"}, func() { h.addTurn(2); e.write("g.txt", []byte("two"), 0o644) })
	if _, err := e.cp.Undo(false); err != nil {
		t.Fatal(err)
	}
	h.addTurn(9) // the person kept talking after the undo
	if _, err := e.cp.Redo(false); err != ErrHistoryMoved {
		t.Fatalf("redo after the conversation moved on = %v, want ErrHistoryMoved", err)
	}
	if _, err := e.cp.Redo(true); err != nil {
		t.Fatalf("forced redo: %v", err)
	}
}

func TestCheckpointWithoutAnchorLeavesConversationAlone(t *testing.T) {
	e := newCP(t)
	h := &fakeHistory{}
	h.addTurn(1)
	h.addTurn(2)
	// Bound after the turns were recorded: no anchors, so undo changes files only.
	e.turn(1, []string{"k.txt"}, func() { e.write("k.txt", []byte("x"), 0o644) })
	e.cp.SetHistory(h)
	before := len(h.msgs)
	if _, err := e.cp.Undo(false); err != nil {
		t.Fatal(err)
	}
	if len(h.msgs) != before {
		t.Fatalf("undo without an anchor changed the history: %d -> %d", before, len(h.msgs))
	}
}
