package console

import (
	"strings"
	"testing"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

func TestTargetPickerFiltersAsKeysArrive(t *testing.T) {
	input := textinput.New()
	q := targetPicker{all: []TargetOption{{ID: 1, Name: "AIServer", Kind: "local"}, {ID: 2, Name: "MediaServer", Kind: "ssh"}}, query: input}
	q.query.Focus()
	model, _ := q.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	filtered := model.(targetPicker).matches()
	if len(filtered) != 1 || filtered[0].ID != 2 {
		t.Fatalf("typed filter selected %v, want MediaServer", filtered)
	}
	model, _ = model.(targetPicker).Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := model.(targetPicker).selected; got != 2 {
		t.Fatalf("enter selected target %d, want 2", got)
	}
}

func TestBlankShellFormOffersSearchableMachines(t *testing.T) {
	m := sampleDashboard()
	m.targets = []row{{"id": float64(1), "name": "AIServer", "kind": "local"}, {"id": float64(2), "name": "MediaServer", "kind": "ssh"}}
	m.newShellForm()
	if m.form == nil || len(m.form.fields) != 1 || !m.form.fields[0].Searchable {
		t.Fatal("blank shell form did not offer a searchable machine field")
	}
	if got := m.form.fields[0].Value; got != "1" {
		t.Fatalf("blank shell form defaulted to %q, want first machine", got)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.busy {
		t.Fatal("Enter should submit the one-field blank shell form")
	}
}

func TestBlankShellResultReturnsToLiveSessionsForAttachment(t *testing.T) {
	m := sampleDashboard()
	m.section = 3
	m.query.SetValue("old filter")
	m.ended, m.archived, m.attention = true, true, true
	m.Update(resultMsg{label: "Create blank shell", data: []byte(`{"id":42}`)})
	if m.section != 0 || m.query.Value() != "" || m.ended || m.archived || m.attention || !m.attachAfterRefresh || m.focusSessionID != "42" {
		t.Fatalf("blank shell result did not reset dashboard for attachment: section=%d query=%q ended=%v archived=%v attention=%v focus=%q attach=%v", m.section, m.query.Value(), m.ended, m.archived, m.attention, m.focusSessionID, m.attachAfterRefresh)
	}
}

func TestBlankShell404ExplainsStaleRunningServer(t *testing.T) {
	m := sampleDashboard()
	m.Update(resultMsg{label: "Create blank shell", err: &HTTPError{Status: 404, Detail: "404 page not found"}})
	if !strings.Contains(m.notice, "restart or update Lectern") {
		t.Fatalf("unhelpful stale-server notice: %q", m.notice)
	}
}
