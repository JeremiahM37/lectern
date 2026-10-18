package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// errorAlerts counts the error alerts on the modal stack.
func errorAlerts(m *Model) int {
	n := 0
	for _, md := range m.modals {
		if a, ok := md.(*Alert); ok && a.level == "error" {
			n++
		}
	}
	return n
}

func TestMarkedCommandFailureOpensAnAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.update(flashMsg{kind: "err", text: "disk full", chat: true, alert: true})

	a := topAlert(t, m)
	if a.level != "error" || a.title != "Command failed" || a.message != "disk full" {
		t.Fatalf("alert = level %q title %q message %q", a.level, a.title, a.message)
	}
	// The chat entry is still written for a command error.
	if got := lastChat(m); !strings.HasPrefix(got, "Command error: disk full") {
		t.Fatalf("chat row = %q, want the Command error: wrapper", got)
	}
}

func TestGitFailureOpensAnAlert(t *testing.T) {
	cases := []string{
		"Commit failed: nothing to commit",
		"Stash failed: no local changes",
		"Stash pop failed: conflict in main.go",
		"Pull failed: exit 1",
		"Push failed: rejected",
		"Force push failed: exit 128",
		"Reset failed: exit 1",
	}
	for _, text := range cases {
		t.Run(text, func(t *testing.T) {
			m, _ := newTestModel(t, 120, 30)
			m.update(flashMsg{kind: "err", text: text})
			if a := topAlert(t, m); a.message != text {
				t.Fatalf("alert message = %q, want %q", a.message, text)
			}
		})
	}
}

func TestRepeatedFailureStacksOneAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.update(flashMsg{kind: "err", text: "Push failed: rejected"})
	m.update(flashMsg{kind: "err", text: "Push failed: rejected"})
	if n := errorAlerts(m); n != 1 {
		t.Fatalf("error alerts = %d, want 1 for one repeated failure", n)
	}
}

func TestUsageHintsStayInTheFlashLine(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	// The usage text is also marked, so this checks the usage guard itself,
	// not just the absence of the flag.
	m.update(flashMsg{kind: "err", text: "Usage: /commit --staged <message>", chat: true, alert: true})
	m.update(flashMsg{kind: "err", text: "Usage: /undo [n], where n is a positive number of turns"})
	if m.topModal() != nil {
		t.Fatalf("a usage hint opened a dialog: %T", m.topModal())
	}
	if m.flash == "" {
		t.Fatal("a usage hint did not reach the flash line")
	}
}

func TestInvalidChoiceCmdHintOpensNoAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	// cmdHint builds a flashMsg with no alert flag, as invalidChoice text does.
	msg := cmdHint(`/speed: "warp" is not a choice. Valid: fast, slow.`)()
	m.update(msg)
	if m.topModal() != nil {
		t.Fatalf("an invalid choice opened a dialog: %T", m.topModal())
	}
	if m.flash == "" {
		t.Fatal("an invalid choice did not reach the flash line")
	}
}

func TestInfoAndOkFlashesOpenNoAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.update(flashMsg{kind: "info", text: "Press Ctrl+C once more to quit"})
	m.update(flashMsg{kind: "ok", text: "Committed"})
	// An info line that reads like a failure is still info.
	m.update(flashMsg{kind: "info", text: "Commit failed: nothing to commit", alert: true})
	if m.topModal() != nil {
		t.Fatalf("an info or ok flash opened a dialog: %T", m.topModal())
	}
}

func TestUnlistedErrorFlashStaysFlashOnly(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.update(flashMsg{kind: "err", text: "Could not save the theme style: read-only file"})
	if m.topModal() != nil {
		t.Fatalf("an unlisted error flash opened a dialog: %T", m.topModal())
	}
	if m.flash != "Could not save the theme style: read-only file" {
		t.Fatalf("flash = %q, want the error on the flash line", m.flash)
	}
}

func TestDifferentFailuresEachOpenTheirOwnAlert(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.onEvent(eventMsg{tab: m.cur().id, ev: turnError("overloaded")})
	m.update(flashMsg{kind: "err", text: "Push failed: rejected"})
	if n := errorAlerts(m); n != 2 {
		t.Fatalf("error alerts = %d, want the turn alert and the push alert", n)
	}
	// Dismissing the top one leaves the other, so nothing is lost behind it.
	m.updateModal(tea.KeyMsg{Type: tea.KeyEnter})
	if n := errorAlerts(m); n != 1 {
		t.Fatalf("after one dismiss, error alerts = %d, want 1", n)
	}
}
