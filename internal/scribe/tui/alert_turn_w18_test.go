package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
)

func turnError(text string) agent.Event {
	return agent.Event{Kind: agent.EvError, Text: text}
}

func topAlert(t *testing.T, m *Model) *Alert {
	t.Helper()
	a, ok := m.topModal().(*Alert)
	if !ok {
		t.Fatalf("top modal is %T, want an *Alert", m.topModal())
	}
	return a
}

func TestTurnErrorOnTheVisibleTabOpensADismissableAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.onEvent(eventMsg{tab: m.cur().id, ev: turnError("rate limited by the provider")})

	a := topAlert(t, m)
	if a.level != "error" || a.title != "Turn failed" {
		t.Fatalf("alert = level %q title %q, want an error titled Turn failed", a.level, a.title)
	}
	if !strings.Contains(a.message, "rate limited by the provider") || !strings.Contains(a.message, "/errors") {
		t.Fatalf("alert body = %q, want the error and a pointer to /errors", a.message)
	}
	// The error log entry is still written; the alert adds to it.
	if len(m.errLog) != 1 || m.errLog[0].Source != srcTurn {
		t.Fatalf("errLog = %+v, want one turn entry", m.errLog)
	}
	// Enter dismisses it, and the chat behind it is still there.
	m.updateModal(tea.KeyMsg{Type: tea.KeyEnter})
	if m.topModal() != nil {
		t.Fatal("Enter did not dismiss the error alert")
	}
}

func TestTurnErrorAlertDismissesWithEsc(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.onEvent(eventMsg{tab: m.cur().id, ev: turnError("overloaded")})
	m.updateModal(tea.KeyMsg{Type: tea.KeyEsc})
	if m.topModal() != nil {
		t.Fatal("Esc did not dismiss the error alert")
	}
}

func TestBackgroundTabErrorIsLoggedButDoesNotOpenAnAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	visible := m.active
	bg := m.addTab(NewFakeBackend(), "background")
	m.active = visible // addTab switches to the new tab; the user stays on the first
	m.onEvent(eventMsg{tab: bg.id, ev: turnError("auth failed")})

	if m.topModal() != nil {
		t.Fatalf("a background tab's error opened a dialog over the visible tab: %T", m.topModal())
	}
	if len(m.errLog) != 1 || !bg.errFlag {
		t.Fatalf("background error not logged and flagged: log=%+v flag=%v", m.errLog, bg.errFlag)
	}
}

func TestRepeatedTurnErrorStacksOneAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.onEvent(eventMsg{tab: m.cur().id, ev: turnError("stream broke")})
	m.onEvent(eventMsg{tab: m.cur().id, ev: turnError("stream broke")})

	n := 0
	for _, md := range m.modals {
		if _, ok := md.(*Alert); ok {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("alerts on the stack = %d, want 1 for one repeated error", n)
	}
	if len(m.errLog) != 1 {
		t.Fatalf("errLog = %d entries, want the repeat folded into one row", len(m.errLog))
	}
}

func TestUserStopIsNotAnErrorAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.onEvent(eventMsg{tab: m.cur().id, ev: turnError(context.Canceled.Error())})
	if m.topModal() != nil {
		t.Fatal("stopping a turn opened an error alert")
	}
}

func TestEmptyTurnErrorOpensNoAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.showTurnError(m.cur(), "   ")
	if m.topModal() != nil {
		t.Fatal("an empty error opened an alert with no message")
	}
}

// A failed turn reports its error as it happens and again in its final
// stats; the log shows it once, not as a repeat.
func TestTurnErrorIsLoggedOnceWithTheTurnsStats(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.onEvent(eventMsg{tab: m.cur().id, ev: turnError("connection refused")})
	m.onEvent(eventMsg{tab: m.cur().id, ev: agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{Error: "connection refused"}}})
	if len(m.errLog) != 1 || m.errLog[0].Count != 1 {
		t.Fatalf("errLog = %+v, want one entry seen once", m.errLog)
	}
}
