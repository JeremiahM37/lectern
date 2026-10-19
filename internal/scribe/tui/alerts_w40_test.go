package tui

import (
	"strings"
	"testing"
	"time"
)

// An editor that exits with an error is a failure the user must see, so it
// opens an alert and not only the flash line (spec 01 §7.3).
func TestEditorNonZeroExitOpensAlert(t *testing.T) {
	skipWithoutPTY(t)
	t.Setenv("SCRIBE_EDITOR", "false")
	m, _ := newTestModel(t, 120, 40)
	defer m.closePanes()

	m.runCommand("/editor")
	if m.ps().editor == nil || m.ps().editor.pane == nil {
		t.Fatal("editor did not start")
	}
	deadline := time.Now().Add(5 * time.Second)
	for m.ps().editor != nil && time.Now().Before(deadline) {
		m.onPaneMsg(paneMsg{p: m.ps().editor.pane})
		time.Sleep(20 * time.Millisecond)
	}
	if m.ps().editor != nil {
		t.Fatal("editor pane stayed open after the program exited")
	}
	if m.flashKind != "err" || !strings.HasPrefix(m.flash, "Editor exited with status") {
		t.Fatalf("flash = %q (%s), want the editor failure", m.flash, m.flashKind)
	}
	if n := errorAlerts(m); n != 1 {
		t.Fatalf("error alerts = %d, want 1", n)
	}
}
