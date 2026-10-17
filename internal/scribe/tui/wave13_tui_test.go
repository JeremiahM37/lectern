package tui

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// A servers view that is restarting a server holds its keys until the restart
// reports back; Esc waits too.
func TestServersViewHoldsKeysWhileRestarting(t *testing.T) {
	b := serversFixture()
	m := modalModel(t, 140, 36, b)
	m.runCommand("/servers")
	send(m, "down") // postgres
	cmd := send(m, "r")
	if cmd == nil {
		t.Fatal("r should start a restart")
	}
	v, ok := m.topModal().(*serversView)
	if !ok {
		t.Fatalf("top modal = %T, want the servers view", m.topModal())
	}
	if !v.Busy() {
		t.Fatal("the view should be busy while the restart runs")
	}
	send(m, "esc")
	if m.topModal() != v {
		t.Fatal("Esc must wait while the restart runs")
	}
	m.updateModal(cmd())
	if v.Busy() {
		t.Fatal("the view should be free once the restart reports back")
	}
	if v.note == "" {
		t.Fatal("the restart result should show in the view")
	}
}

// Diagnose holds keys until its checks finish.
func TestDiagnoseHoldsKeysWhileChecking(t *testing.T) {
	p := &diagnosePopup{th: theme.Default(), loading: true}
	if !p.Busy() {
		t.Fatal("a loading diagnose popup must be busy")
	}
	p.Update(diagnoseDone{checks: []DiagnoseCheck{{Area: "Git", Name: "repo", State: "ok"}}})
	if p.Busy() {
		t.Fatal("a finished diagnose popup must be free")
	}
}
