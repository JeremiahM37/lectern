package console

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func groupedDashboard() *dashboard {
	m := sampleDashboard()
	m.rows = []row{
		{"id": float64(1), "name": "Repair", "group_path": "Work/Backend", "status": "waiting"},
		{"id": float64(2), "name": "Review", "group_path": "Work/Backend", "status": "running"},
		{"id": float64(3), "name": "Website", "group_path": "Work/Frontend", "status": "idle"},
		{"id": float64(4), "name": "Named ungrouped", "group_path": "Ungrouped"},
		{"id": float64(5), "name": "Actually ungrouped"},
	}
	m.grouping = 3
	m.filter()
	return m
}
func selectDisplay(t *testing.T, m *dashboard, selection string) {
	t.Helper()
	for i, r := range m.visible {
		if displayID(r) == selection {
			m.selected = i
			m.ensureSelection()
			m.updatePreview()
			return
		}
	}
	t.Fatalf("missing %q in %v", selection, m.visible)
}
func TestDashboardGroupCollapseSearchAndRefresh(t *testing.T) {
	m := groupedDashboard()
	selectDisplay(t, m, "group:Work")
	if r := m.selectedGroup(); r["count"] != 3 || r["attention"] != 1 {
		t.Fatal(r)
	}
	if m.current() != nil {
		t.Fatal("group exposed as actionable session")
	}
	for _, action := range m.actions() {
		if action.Path != "" || action.Method != "" {
			t.Fatal("group action sent to session API", action)
		}
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.collapsed["Work"] || strings.Contains(m.listView(50), "Repair") {
		t.Fatal("not collapsed")
	}
	m.rows = append(m.rows, row{"id": float64(6), "name": "New arrival", "group_path": "Work/Backend", "status": "failed"})
	m.filter()
	if m.selectionID() != "group:Work" || m.selectedGroup()["count"] != 4 || m.selectedGroup()["attention"] != 2 {
		t.Fatal("refresh lost group state/counts")
	}
	m.query.SetValue("Repair")
	m.filter()
	if len(m.visible) != 1 || id(m.current()) != "1" {
		t.Fatal("search hid collapsed child")
	}
	m.query.SetValue("")
	m.filter()
	selectDisplay(t, m, "group:Work")
	if !m.collapsed["Work"] {
		t.Fatal("search discarded collapse preference")
	}
	m.Update(key("]"))
	selectDisplay(t, m, "1")
	m.Update(key("["))
	if m.selectionID() != "group:Work/Backend" || !m.collapsed["Work/Backend"] {
		t.Fatal("did not collapse session's group")
	}
	m.Update(key("["))
	if m.selectionID() != "group:Work" || !m.collapsed["Work"] {
		t.Fatal("did not collapse parent")
	}
}
func TestDashboardUngroupedIdentityAndTreeMouseGeometry(t *testing.T) {
	m := groupedDashboard()
	m.setGroupCollapsed("\x00", true)
	selectDisplay(t, m, "4") // A group literally named Ungrouped remains open.
	for _, height := range []int{12, 18, 35} {
		m.height = height
		for selected := range m.visible {
			m.selected = selected
			m.ensureSelection()
			y := m.rowsHeight(m.offset, selected)
			if m.rowAt(y) != selected || y+m.rowHeight(selected) > max(3, height-9) {
				t.Fatal("selected row not visible/clickable", height, selected)
			}
		}
	}
	if m.rowAt(-1) != -1 || m.rowAt(m.height) != -1 {
		t.Fatal("footer click selects session")
	}
	m.height = 35
	m.offset = 0
	selectDisplay(t, m, "group:Work")
	y := m.rowsHeight(m.offset, m.selected) + 4
	m.Update(tea.MouseMsg{Y: y, X: 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if !m.collapsed["Work"] {
		t.Fatal("group click did not fold")
	}
}

func TestDashboardBlankSpaceBelowTreeDoesNotSelectClippedSession(t *testing.T) {
	m := groupedDashboard()
	m.height = 12 // Four body lines: header + session + header, then no room.
	m.offset = 0
	m.collapsed = nil
	m.filter()
	// Start at Work: Work/Backend's first two-line session cannot fit in the
	// one remaining line after its sibling/header arrangement below.
	selectDisplay(t, m, "group:Work/Backend")
	m.offset = m.selected
	m.height = 12
	if m.rowAt(3) != -1 {
		t.Fatal("blank space selected a session omitted by listView")
	}
}
