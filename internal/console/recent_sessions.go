package console

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// The Restore view (C) lists what GET /sessions/restorable offers — closed,
// archived, exited and restart-interrupted sessions — and Enter runs each
// row's own action through POST /sessions/ID/reopen. U reopens the newest one
// without opening the list: undo for the session just closed.

const restoreLabel = "Restore session"

func (m *dashboard) loadRecentSessions() tea.Cmd {
	if m.busy || sections[m.section] != "sessions" {
		return nil
	}
	m.busy = true
	c := m.client
	return func() tea.Msg {
		b, err := c.JSON("GET", "/sessions/restorable?limit=100", nil)
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
		ended, ok = r["updated_at"].(float64)
	}
	if !ok || ended <= 0 {
		return "recently"
	}
	age := time.Since(time.Unix(int64(ended), 0))
	if age < time.Minute {
		return "just now"
	}
	return shortAge(age) + " ago"
}

// sessionAge is how long since a live session last did anything ("40s",
// "12m", "3h 5m", "2d 4h"), or "" when the server sent no timestamp.
func sessionAge(r row) string {
	at, ok := r["last_activity_at"].(float64)
	if !ok || at <= 0 {
		if at, ok = r["created_at"].(float64); !ok || at <= 0 {
			return ""
		}
	}
	age := time.Since(time.Unix(int64(at), 0))
	if age < time.Minute {
		return "now"
	}
	return shortAge(age)
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
		project = "No project"
	}
	agent := str(r["agent"])
	if agent == "" {
		agent = "unknown agent"
	}
	parts := []string{project, agent}
	if reason := str(r["reason_label"]); reason != "" {
		parts = append(parts, reason)
	}
	return oneLine(strings.Join(append(parts, recentClosedAge(r)), " · "))
}

func recentAction(r row) string {
	if label := str(r["action_label"]); label != "" {
		return label
	}
	return "Choose history"
}

// recentVisible applies the Restore view's own filter: every word must appear
// in the name, project, agent, folder, reason or last message.
func (m *dashboard) recentVisible() []row {
	terms := strings.Fields(strings.ToLower(m.recentQuery))
	if len(terms) == 0 {
		return m.recentRows
	}
	out := []row{}
	for _, r := range m.recentRows {
		haystack := strings.ToLower(strings.Join([]string{name(r), str(r["project_name"]), str(r["agent"]),
			str(r["model"]), str(r["workdir"]), str(r["group_path"]), str(r["reason_label"]), str(r["preview"])}, " "))
		match := true
		for _, term := range terms {
			if !strings.Contains(haystack, term) {
				match = false
				break
			}
		}
		if match {
			out = append(out, r)
		}
	}
	return out
}

func (m *dashboard) recentView(height int) string {
	head := " Restore · ↑↓ choose · Enter restore · h history · / search · Esc back"
	if m.recentSearching || m.recentQuery != "" {
		head = " Restore · search: " + m.recentQuery
		if m.recentSearching {
			head += "▏ (Enter done)"
		}
	}
	lines := []string{head, ""}
	visible := m.recentVisible()
	if len(visible) == 0 {
		if m.recentQuery != "" {
			lines = append(lines, " No closed session matches.")
		} else {
			lines = append(lines, " Nothing to restore.")
		}
		return strings.Join(lines, "\n")
	}
	visibleRows := max(1, (height-2)/3)
	start := 0
	if m.recentSelected >= visibleRows {
		start = m.recentSelected - visibleRows + 1
	}
	end := min(len(visible), start+visibleRows)
	if start > 0 {
		lines = append(lines, " …")
	}
	for i := start; i < end; i++ {
		r := visible[i]
		label := "  " + oneLine(name(r))
		if i == m.recentSelected {
			label = "› " + oneLine(name(r))
			label = chosen.Render(label)
		}
		operation := recentAction(r)
		if preview := str(r["preview"]); preview != "" {
			operation += " — “" + oneLine(preview) + "”"
		}
		lines = append(lines, clip(label, m.width-2), clip("  "+recentLabel(r), m.width-2), clip("  "+operation, m.width-2))
	}
	if end < len(visible) {
		lines = append(lines, " …")
	}
	return strings.Join(lines, "\n")
}

func (m *dashboard) recentSelectedRow() row {
	visible := m.recentVisible()
	if m.recentSelected < 0 || m.recentSelected >= len(visible) {
		return nil
	}
	return visible[m.recentSelected]
}

func (m *dashboard) resumeRecentSelected() tea.Cmd {
	r := m.recentSelectedRow()
	if r == nil {
		return nil
	}
	if str(r["action"]) == "history" {
		return m.recentHistory(r)
	}
	m.recentPending = r
	return m.request(restoreLabel, "POST", "/sessions/"+id(r)+"/reopen", map[string]any{}, false)
}

// undoLastClose reopens the newest restorable session: U right after closing
// one brings it back without opening the list.
func (m *dashboard) undoLastClose() tea.Cmd {
	if m.busy || sections[m.section] != "sessions" {
		return nil
	}
	m.busy = true
	c, key := m.client, m.key()
	return func() tea.Msg {
		b, err := c.JSON("GET", "/sessions/restorable?limit=1", nil)
		var rows []row
		if err == nil {
			err = json.Unmarshal(b, &rows)
		}
		if err != nil {
			return resultMsg{label: restoreLabel, err: err, key: key}
		}
		if len(rows) == 0 {
			return resultMsg{label: restoreLabel, err: fmt.Errorf("nothing to restore"), key: key}
		}
		if str(rows[0]["action"]) == "history" {
			return undoMsg{row: rows[0], history: true}
		}
		data, err := c.JSON("POST", "/sessions/"+id(rows[0])+"/reopen", map[string]any{})
		return undoMsg{row: rows[0], result: resultMsg{label: restoreLabel, data: data, err: err, key: key}}
	}
}

type undoMsg struct {
	row     row
	history bool
	result  resultMsg
}

// restoredSession reads the session a reopen returned, and its message.
func restoredSession(data []byte) (string, string) {
	var out struct {
		Session row    `json:"session"`
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &out) != nil {
		return "", ""
	}
	return id(out.Session), out.Message
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
	m.historyResume = true
	return m.savedConversations()
}
