package console

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func (m *dashboard) loadRecentSessions() tea.Cmd {
	if m.busy || sections[m.section] != "sessions" {
		return nil
	}
	m.busy = true
	c := m.client
	return func() tea.Msg {
		b, err := c.JSON("GET", "/sessions/recent?limit=30", nil)
		var rows []row
		if err == nil {
			err = json.Unmarshal(b, &rows)
		}
		return recentMsg{rows: rows, err: err}
	}
}

func recentClosedAge(r row) string {
	ended, ok := r["ended_at"].(float64)
	if !ok || ended <= 0 {
		return "closed recently"
	}
	age := time.Since(time.Unix(int64(ended), 0))
	if age < time.Minute {
		return "closed just now"
	}
	return fmt.Sprintf("closed %s ago", shortAge(age))
}

func shortAge(age time.Duration) string {
	if age < time.Hour {
		return fmt.Sprintf("%dm", int(age/time.Minute))
	}
	if age < 24*time.Hour {
		return fmt.Sprintf("%dh %dm", int(age/time.Hour), int(age/time.Minute)%60)
	}
	return fmt.Sprintf("%dd %dh", int(age/(24*time.Hour)), int(age/time.Hour)%24)
}

func recentLabel(r row) string {
	project := str(r["project_name"])
	if project == "" {
		project = "Unassigned"
	}
	agent := str(r["agent"])
	if agent == "" {
		agent = "unknown agent"
	}
	return oneLine(strings.Join([]string{project, agent, recentClosedAge(r)}, " · "))
}

func (m *dashboard) recentView(height int) string {
	lines := []string{" Recently closed · ↑↓ choose · Enter resume · h choose history · Esc back", ""}
	if len(m.recentRows) == 0 {
		lines = append(lines, " No recently closed sessions.")
		return strings.Join(lines, "\n")
	}
	visibleRows := max(1, (height-2)/3)
	start := 0
	if m.recentSelected >= visibleRows {
		start = m.recentSelected - visibleRows + 1
	}
	end := min(len(m.recentRows), start+visibleRows)
	if start > 0 {
		lines = append(lines, " …")
	}
	for i := start; i < end; i++ {
		r := m.recentRows[i]
		label := "  " + oneLine(name(r))
		if i == m.recentSelected {
			label = "› " + oneLine(name(r))
			label = chosen.Render(label)
		}
		lines = append(lines, clip(label, m.width-2), clip("  "+recentLabel(r), m.width-2))
		operation := "Choose history"
		if r["released"] == true && r["can_restore"] == true {
			operation = "Restore tracking"
		} else if r["can_resume_recent"] == true {
			operation = "Resume"
		}
		lines = append(lines, clip("  "+operation, m.width-2))
	}
	if end < len(m.recentRows) {
		lines = append(lines, " …")
	}
	return strings.Join(lines, "\n")
}

func (m *dashboard) recentSelectedRow() row {
	if m.recentSelected < 0 || m.recentSelected >= len(m.recentRows) {
		return nil
	}
	return m.recentRows[m.recentSelected]
}

func (m *dashboard) resumeRecentSelected() tea.Cmd {
	r := m.recentSelectedRow()
	if r == nil {
		return nil
	}
	if r["released"] == true && r["can_restore"] == true {
		m.recentPending = r
		return m.request("Restore tracking", "POST", "/sessions/"+id(r)+"/restore", map[string]any{}, false)
	}
	if r["can_resume_recent"] != true {
		return m.recentHistory(r)
	}
	m.recentPending = r
	return m.request("Resume recently closed", "POST", "/sessions/"+id(r)+"/resume-recent", map[string]any{"name": name(r)}, false)
}

func (m *dashboard) recentHistorySelected() tea.Cmd {
	if r := m.recentSelectedRow(); r != nil {
		return m.recentHistory(r)
	}
	return nil
}

// Put the server record into the in-memory list so the existing native history
// picker can use its normal current-row flow. This does not change the server
// record or attempt to attach to its old terminal.
func (m *dashboard) recentHistory(r row) tea.Cmd {
	if sections[m.section] != "sessions" || r == nil {
		return nil
	}
	m.recentOpen = false
	m.archived = false
	m.searching = false
	m.query.Blur()
	m.query.SetValue("")
	m.ended = true
	found := false
	for _, existing := range m.rows {
		if id(existing) == id(r) {
			found = true
			break
		}
	}
	if !found {
		m.rows = append(m.rows, r)
	}
	m.filter()
	for i, visible := range m.visible {
		if id(visible) == id(r) {
			m.selected = i
			m.ensureSelection()
			m.updatePreview()
			break
		}
	}
	return m.savedConversations()
}
