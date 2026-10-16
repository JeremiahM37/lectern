package session

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// fakeHist is a bare message list for checkpoint tests.
type fakeHist struct{ msgs []llm.Message }

func (h *fakeHist) Messages() []llm.Message     { return h.msgs }
func (h *fakeHist) SetMessages(m []llm.Message) { h.msgs = append([]llm.Message(nil), m...) }
func (h *fakeHist) add(n int) {
	for i := 0; i < n; i++ {
		h.msgs = append(h.msgs, llm.Message{})
	}
}

// Three turns: 1 chats, 2 edits a file, 3 chats. Sending after turn 1 must
// drop turns 2 and 3 from the conversation and revert the file, and nothing
// from turn 1 goes.
func TestTruncateAfterDropsLaterTurns(t *testing.T) {
	e := newCP(t)
	h := &fakeHist{}
	e.cp.SetHistory(h)
	abs := filepath.Join(e.root, "f.txt")
	e.write("f.txt", []byte("original"), 0o644)

	e.cp.Begin(1, "question")
	h.add(2)
	if err := e.cp.End(); err != nil {
		t.Fatal(err)
	}
	e.cp.Begin(2, "edit f")
	h.add(2)
	e.cp.BeforeWrite(abs)
	e.write("f.txt", []byte("edited"), 0o644)
	if err := e.cp.End(); err != nil {
		t.Fatal(err)
	}
	e.cp.Begin(3, "more chat")
	h.add(2)
	if err := e.cp.End(); err != nil {
		t.Fatal(err)
	}

	dropped, err := e.cp.TruncateAfter(1, false)
	if err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if dropped != 1 {
		t.Fatalf("dropped %d later checkpoints with files, want 1", dropped)
	}
	if len(h.msgs) != 2 {
		t.Fatalf("conversation has %d messages, want 2 (turn 1 only)", len(h.msgs))
	}
	if got := string(e.read("f.txt")); got != "original" {
		t.Fatalf("file after truncate = %q, want the turn-1 state", got)
	}
	if _, err := e.cp.Redo(false); !errors.Is(err, ErrNothingToRedo) {
		t.Fatalf("redo after a branch should be empty, got %v", err)
	}
}

// Truncating to the last turn changes nothing.
func TestTruncateAfterLastTurnIsNoop(t *testing.T) {
	e := newCP(t)
	h := &fakeHist{}
	e.cp.SetHistory(h)
	e.cp.Begin(1, "only")
	h.add(2)
	if err := e.cp.End(); err != nil {
		t.Fatal(err)
	}
	dropped, err := e.cp.TruncateAfter(1, false)
	if err != nil || dropped != 0 || len(h.msgs) != 2 {
		t.Fatalf("noop: dropped %d err %v msgs %d", dropped, err, len(h.msgs))
	}
}

// A turn still running blocks the cut, as it blocks undo.
func TestTruncateAfterRefusesWhileTurnRuns(t *testing.T) {
	e := newCP(t)
	h := &fakeHist{}
	e.cp.SetHistory(h)
	e.cp.Begin(1, "a")
	h.add(1)
	_ = e.cp.End()
	e.cp.Begin(2, "b")
	h.add(1)
	if _, err := e.cp.TruncateAfter(1, false); !errors.Is(err, ErrTurnRunning) {
		t.Fatalf("want ErrTurnRunning, got %v", err)
	}
}
