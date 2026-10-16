package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A draft sent over the bus lands in an empty input; a draft already typed is
// stashed, not overwritten.
func TestDraftBusRestoresAndStashes(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	tb := m.cur()
	m.restoreDraft(tb, "from a command")
	if got := tb.in.value(); got != "from a command" {
		t.Fatalf("empty input did not take the draft: %q", got)
	}
	tb.in.setValue("half typed")
	m.restoreDraft(tb, "from remote")
	if got := tb.in.value(); got != "from remote" {
		t.Fatalf("draft not restored: %q", got)
	}
	if n := len(tb.in.stashes); n == 0 || tb.in.stashes[n-1] != "half typed" {
		t.Fatalf("typed draft was not stashed: %v", tb.in.stashes)
	}
	// The bus command reaches the tab through Update.
	m.Update(draftCmd(tb.id, "bus")())
	if got := tb.in.value(); got != "bus" {
		t.Fatalf("bus message did not restore: %q", got)
	}
}

// A failure the backend reported opens the dismissable error alert; a typo
// or usage hint does not (it stays in the flash line and the chat).
func TestCommandErrorOpensAlert(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.Update(flashMsg{kind: "err", text: "No theme named nope", chat: true})
	if m.topModal() != nil {
		t.Fatalf("a typo opened an alert: %T", m.topModal())
	}
	m.Update(cmdErr(errors.New("could not write the session file: disk full"))())
	a, ok := m.topModal().(*Alert)
	if !ok {
		t.Fatalf("command error did not open an alert: %T", m.topModal())
	}
	if got := plain(strings.Join(modalFrame(a, m.th, 80, 24), "\n")); !strings.Contains(got, "disk full") {
		t.Fatalf("alert does not show the error: %q", got)
	}
	m.closeModal(a)

	m.Update(flashMsg{kind: "err", text: "Usage: /theme [name]", chat: true})
	if m.topModal() != nil {
		t.Fatalf("usage hint opened an alert: %T", m.topModal())
	}
}

// While browsing a checkpoint the empty input says how to rewind.
func TestPlaceholderNamesCheckpointWhileBrowsing(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	tb := m.cur()
	tb.cps = []CheckpointInfo{{Turn: 3}, {Turn: 7}}
	tb.browse = 1
	ph, _ := inputPlaceholder(tb, m.th)
	if !strings.Contains(ph, "rewind to checkpoint #7") {
		t.Fatalf("placeholder while browsing = %q", ph)
	}
	tb.browse = -1
	if ph, _ := inputPlaceholder(tb, m.th); strings.Contains(ph, "rewind") {
		t.Fatalf("live placeholder still says rewind: %q", ph)
	}
}

// The wheel moves a list one row per slow tick and speeds up on a burst, up to
// the list cap of four rows.
func TestListWheelAccelerates(t *testing.T) {
	var w listWheel
	now := time.Unix(1000, 0)
	if n := w.step(now); n != 1 {
		t.Fatalf("first tick moved %d rows, want 1", n)
	}
	now = now.Add(time.Second)
	if n := w.step(now); n != 1 {
		t.Fatalf("slow tick moved %d rows, want 1", n)
	}
	var last int
	for i := 0; i < 20; i++ {
		now = now.Add(5 * time.Millisecond)
		last = w.step(now)
	}
	if last != listWheelMax {
		t.Fatalf("burst moved %d rows, want the cap %d", last, listWheelMax)
	}
}

// A wheel over a picker moves its cursor, not the chat behind it.
func TestWheelMovesPickerCursor(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.openThemePicker()
	p := m.topModal().(*Picker)
	before := p.cursor
	m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress})
	if p.cursor <= before {
		t.Fatalf("wheel down did not move the picker: %d -> %d", before, p.cursor)
	}
}
