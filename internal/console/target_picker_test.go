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

func TestBlankShellFormOffersOneSearchableLocationField(t *testing.T) {
	m := sampleDashboard()
	m.targets = []row{{"id": float64(1), "name": "AIServer", "kind": "local"}, {"id": float64(2), "name": "MediaServer", "kind": "ssh"}}
	m.projects = []row{{"id": float64(7), "name": "Site", "repo_path": "/srv/site", "target_name": "AIServer"}}
	m.newShellForm()
	if m.form == nil || len(m.form.fields) != 1 || m.form.fields[0].Key != "location" || !m.form.fields[0].Searchable {
		t.Fatal("blank shell form should offer one searchable location field")
	}
	// Projects lead the list, shown with their folder and target.
	if got := m.form.fields[0].Value; got != "project:7" {
		t.Fatalf("blank shell form defaulted to %q, want the first project", got)
	}
	labels := map[string]string{}
	for _, c := range m.form.fields[0].Options {
		labels[c.Value] = c.Label
	}
	if !strings.Contains(labels["project:7"], "/srv/site") || !strings.Contains(labels["project:7"], "AIServer") {
		t.Fatalf("project option hid its folder or target: %q", labels["project:7"])
	}
	if !strings.Contains(labels["machine:2"], "MediaServer") {
		t.Fatalf("machine option lost its label: %q", labels["machine:2"])
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || !m.busy {
		t.Fatal("Enter should submit the one-field blank shell form")
	}
}

func TestBlankShellLocationMapsToTheRightSelector(t *testing.T) {
	// A project sends project_id so the server derives target and folder; a
	// machine keeps target_id. There is never a caller-supplied path.
	if body, ok := shellBody("project:9"); !ok || body["project_id"] != int64(9) {
		t.Fatalf("project location mapped to %v (ok=%v)", body, ok)
	}
	if body, ok := shellBody("machine:3"); !ok || body["target_id"] != int64(3) {
		t.Fatalf("machine location mapped to %v (ok=%v)", body, ok)
	}
	if _, ok := shellBody("nonsense"); ok {
		t.Fatal("an unparsable location was accepted")
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
