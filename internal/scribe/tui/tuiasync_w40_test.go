package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// A checkpoint undo runs behind a busy modal: the backend is not touched until
// the command runs, keys are held while it runs, and the wait modal closes once
// the result has been applied.
func TestCheckpointUndoIsBusyUntilTheResultLands(t *testing.T) {
	m, rb := newGitModel(t, 80, 24)
	rb.cps = []CheckpointInfo{{Turn: 3, Prompt: "docs", Files: []string{"d.md"}, Created: time.Now()}}
	send(m, "ctrl+z")
	cmd := send(m, "y")
	w, ok := m.topModal().(*busyWait)
	if !ok || !w.Busy() {
		t.Fatalf("undo should leave a busy wait on top, top is %T", m.topModal())
	}
	if rb.undone != 0 {
		t.Fatalf("the backend must not run on the event loop, undone=%d", rb.undone)
	}
	send(m, "esc") // keys wait while the work runs
	if m.topModal() != w {
		t.Fatalf("a key must not close a busy modal")
	}
	gitPump(m, cmd)
	if rb.undone != 1 {
		t.Fatalf("expected one undo after the command ran, got %d", rb.undone)
	}
	if m.topModal() != nil {
		t.Fatalf("the wait modal should close once the result is applied, top is %T", m.topModal())
	}
}

// runBusy applies its result on the event loop and closes the wait modal even
// when the result is an error, so a failed operation never leaves the UI stuck.
func TestRunBusyClosesAfterAFailedResult(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	cmd := m.runBusy("Testing…", func() func() tea.Cmd {
		return func() tea.Cmd {
			m.setFlash("err", "boom")
			return nil
		}
	})
	if _, ok := m.topModal().(*busyWait); !ok {
		t.Fatalf("runBusy should push the wait modal, top is %T", m.topModal())
	}
	runCmd(m, cmd)
	if m.topModal() != nil {
		t.Fatalf("the wait modal should close after the result, top is %T", m.topModal())
	}
	if m.flash != "boom" {
		t.Fatalf("the result should be applied, flash=%q", m.flash)
	}
}

// The autocomplete popup moves on the compact profile: a burst of wheel ticks
// never moves more than three rows at once, and a slow tick moves one row.
func TestSlashPopupWheelUsesCompactProfile(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	typeText(m, "/")
	if m.cur().in.popupKind() != "slash" || len(m.cur().in.slashMatches()) < 10 {
		t.Fatalf("typing / should open the slash popup with many rows")
	}
	popupWheelState = listWheel{}
	t0 := time.Now()
	wheelDown := tea.MouseMsg{Button: tea.MouseButtonWheelDown, Action: tea.MouseActionPress}
	prev := m.cur().in.acSel
	for i := 0; i < 8; i++ {
		if !m.wheelPopupAt(wheelDown, t0.Add(time.Duration(i)*5*time.Millisecond)) {
			t.Fatalf("a wheel tick over an open popup should be handled")
		}
		if step := m.cur().in.acSel - prev; step < 1 || step > 3 {
			t.Fatalf("tick %d moved %d rows, want 1 to 3 on the compact profile", i, step)
		}
		prev = m.cur().in.acSel
	}
	popupWheelState = listWheel{}
	before := m.cur().in.acSel
	m.wheelPopupAt(wheelDown, t0.Add(time.Second))
	if got := m.cur().in.acSel - before; got != 1 {
		t.Fatalf("a slow tick should move one row, moved %d", got)
	}
}

// The Ctrl+R history list takes the wheel too, and a wheel with no popup open
// falls through to the transcript.
func TestHistorySearchWheelMovesSelection(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	in := m.cur().in
	for _, h := range []string{"alpha one", "alpha two", "alpha three", "alpha four"} {
		in.pushHistory(h)
	}
	wheelUp := tea.MouseMsg{Button: tea.MouseButtonWheelUp, Action: tea.MouseActionPress}
	if m.wheelPopup(wheelUp) {
		t.Fatalf("no popup is open, so the wheel must not be taken")
	}
	in.openSearch()
	if len(in.search.matches) < 4 {
		t.Fatalf("search should list the history, got %d", len(in.search.matches))
	}
	popupWheelState = listWheel{}
	in.search.sel = 3
	if !m.wheelPopupAt(wheelUp, time.Now()) {
		t.Fatalf("the wheel should move the open history list")
	}
	if in.search.sel != 2 {
		t.Fatalf("one slow up tick should move the selection to 2, got %d", in.search.sel)
	}
}
