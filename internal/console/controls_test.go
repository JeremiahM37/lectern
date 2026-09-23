package console

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func controlsDashboard(opts DashboardOptions) *dashboard {
	opts.ControlOnly = true
	return newDashboardOpts(New("http://unused", ""), opts)
}

func runningSessionRow(idValue float64, nameText string) row {
	return row{"id": idValue, "name": nameText, "status": "running", "project_name": "Website", "target_name": "Laptop"}
}

func TestControlsModeHidesNativeTerminalActions(t *testing.T) {
	m := controlsDashboard(DashboardOptions{})
	m.section = 0
	m.rows = []row{runningSessionRow(1, "Alpha")}
	m.filter()
	operations := map[string]bool{}
	for _, action := range m.actions() {
		if nativeTerminalAction(action.Operation) {
			t.Fatalf("controls menu offers native terminal action %q", action.Label)
		}
		operations[action.Operation] = true
	}
	for _, want := range []string{"upload", "send", "rename", "group", "review", "history", "handoff"} {
		if !operations[want] {
			t.Errorf("controls menu lost the ordinary %q action", want)
		}
	}
	if operations["blank-shell"] {
		t.Error("controls menu still offers a blank shell")
	}
}

func TestControlsModeDoesNotOfferAttachForTasks(t *testing.T) {
	m := controlsDashboard(DashboardOptions{})
	m.section = 1
	m.rows = []row{{"id": float64(3), "name": "Task", "status": "running", "attempt": map[string]any{"id": float64(4)}}}
	m.filter()
	for _, action := range m.actions() {
		if nativeTerminalAction(action.Operation) {
			t.Fatalf("controls task menu offers %q", action.Label)
		}
	}
}

func TestControlsModeDisablesNativeAttachKeys(t *testing.T) {
	m := controlsDashboard(DashboardOptions{})
	m.attach = func(string, string) error {
		t.Fatal("controls mode opened a native terminal")
		return nil
	}
	m.rows = []row{runningSessionRow(1, "Alpha")}
	m.filter()
	for _, pressed := range []tea.KeyMsg{{Type: tea.KeyEnter}, key("a"), key("s"), key("S")} {
		m.notice = ""
		_, cmd := m.Update(pressed)
		if cmd != nil {
			cmd()
		}
		if !strings.Contains(m.notice, "disabled in the controls popup") {
			t.Errorf("%v did not explain the disabled action: %q", pressed, m.notice)
		}
	}
}

// The popup must not open actions on the first row while the list is still
// loading; it waits until the requested row is really there.
func TestControlsFocusSelectsRequestedRowBeforeOpeningMenu(t *testing.T) {
	m := controlsDashboard(DashboardOptions{Popup: true, FocusKind: "session", FocusID: "7"})
	if m.menu {
		t.Fatal("menu opened before the row loaded")
	}
	if m.pendingFocus == nil {
		t.Fatal("focus was dropped")
	}
	m.Update(rowsMsg{section: "sessions", generation: m.generation, rows: []row{runningSessionRow(1, "Other"), runningSessionRow(7, "Wanted")}})
	if m.pendingFocus != nil {
		t.Fatal("focus was not resolved")
	}
	if current := m.current(); current == nil || id(current) != "7" {
		t.Fatalf("selected the wrong row: %#v", current)
	}
	if !m.menu {
		t.Fatal("menu did not open on the requested row")
	}
}

func TestControlsFocusReportsAMissingRow(t *testing.T) {
	m := controlsDashboard(DashboardOptions{Popup: true, FocusKind: "session", FocusID: "7"})
	m.Update(rowsMsg{section: "sessions", generation: m.generation, rows: []row{runningSessionRow(1, "Other")}})
	if m.menu {
		t.Fatal("opened actions for an unrelated row")
	}
	if !strings.Contains(m.notice, "No session") {
		t.Fatalf("missing row was not reported: %q", m.notice)
	}
}

func TestControlsAttemptFocusResolvesOwningTask(t *testing.T) {
	m := controlsDashboard(DashboardOptions{FocusKind: "attempt", FocusID: "9"})
	if m.section != 1 {
		t.Fatalf("attempt controls should open Tasks, got section %d", m.section)
	}
	rows := []row{
		{"id": float64(3), "name": "Other task", "status": "running", "attempt": map[string]any{"id": float64(4)}},
		{"id": float64(5), "name": "Owning task", "status": "running", "attempt": map[string]any{"id": float64(9)}},
	}
	m.Update(rowsMsg{section: "tasks", generation: m.generation, rows: rows})
	if current := m.current(); current == nil || id(current) != "5" {
		t.Fatalf("attempt did not resolve to its owning task: %#v", current)
	}
	if !m.menu {
		t.Fatal("menu did not open for the owning task")
	}
}

func TestControlsAttemptFocusReportsAnUnattachedAttempt(t *testing.T) {
	m := controlsDashboard(DashboardOptions{FocusKind: "attempt", FocusID: "9"})
	m.Update(rowsMsg{section: "tasks", generation: m.generation, rows: []row{{"id": float64(3), "name": "Other", "status": "running", "attempt": map[string]any{"id": float64(4)}}}})
	if m.menu {
		t.Fatal("opened actions for an unrelated task")
	}
	if !strings.Contains(m.notice, "No task") {
		t.Fatalf("unattached attempt was not reported: %q", m.notice)
	}
}

func quitFrom(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestControlsPopupEscLeavesTheMenu(t *testing.T) {
	m := controlsDashboard(DashboardOptions{Popup: true})
	m.rows = []row{runningSessionRow(1, "Alpha")}
	m.filter()
	m.menu = true
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); !quitFrom(cmd) {
		t.Fatal("Esc from the menu did not close the popup")
	}
}

func TestControlsPopupEscFromListKeepsDetails(t *testing.T) {
	m := controlsDashboard(DashboardOptions{Popup: true})
	m.rows = []row{runningSessionRow(1, "Alpha")}
	m.filter()
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); !quitFrom(cmd) {
		t.Fatal("Esc from the list did not close the popup")
	}
	m.detailKey = "sessions/1"
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); quitFrom(cmd) {
		t.Fatal("Esc from a detail closed the popup instead of returning to controls")
	}
	if m.detailKey != "" {
		t.Fatalf("detail was not cleared: %q", m.detailKey)
	}
}

func TestControlsPopupFormAndReviewReturnToTheMenu(t *testing.T) {
	m := controlsDashboard(DashboardOptions{Popup: true})
	m.rows = []row{runningSessionRow(1, "Alpha")}
	m.filter()
	m.uploadForm()
	if m.form == nil {
		t.Fatal("upload form did not open")
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyEsc})
	if m.form != nil {
		t.Fatal("Esc did not close the form")
	}
	if !m.menu {
		t.Fatal("Esc from the form did not return to the controls menu")
	}
	m.menu = false
	m.review = &codeReview{}
	m.updateReview(tea.KeyMsg{Type: tea.KeyEsc})
	if m.review != nil || !m.menu {
		t.Fatalf("Esc from review did not return to the menu: review=%v menu=%v", m.review, m.menu)
	}
}
