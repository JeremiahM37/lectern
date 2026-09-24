package console

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

func TestRecentSessionsLoadsAndRendersHumanLabels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions/recent" || r.URL.Query().Get("limit") != "30" {
			t.Fatalf("recent request: %s", r.URL.String())
		}
		json.NewEncoder(w).Encode([]row{{
			"id": float64(9), "name": "Closed UI", "project_name": "Lectern",
			"agent": "codex", "ended_at": float64(time.Now().Add(-2 * time.Hour).Unix()),
			"can_resume_recent": true,
		}})
	}))
	defer srv.Close()

	m := sampleDashboard()
	m.client = New(srv.URL, "")
	cmd := m.loadRecentSessions()
	model, msgCmd := m.Update(cmd())
	if msgCmd != nil {
		// The load command returns its message directly; this guards against
		// accidentally scheduling a second request while rendering the panel.
		t.Fatalf("recent load unexpectedly returned a command")
	}
	m = model.(*dashboard)
	view := m.View()
	if !strings.Contains(view, "Closed UI") || !strings.Contains(view, "Lectern") || !strings.Contains(view, "codex") || !strings.Contains(view, "closed 2h") {
		t.Fatalf("recent view omitted human labels:\n%s", view)
	}
	if strings.Contains(view, "can_resume_recent") || strings.Contains(view, "ended_at") {
		t.Fatalf("recent view leaked API field names:\n%s", view)
	}
}

func TestRecentSessionsFallbackUsesNativeHistoryPicker(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		switch r.URL.Path {
		case "/api/sessions/9/conversations":
			json.NewEncoder(w).Encode(map[string]any{
				"conversations":  []map[string]any{{"id": "cid", "title": "Saved", "modified": float64(1)}},
				"fork_supported": false, "resume_supported": true,
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	m := sampleDashboard()
	m.client = New(srv.URL, "")
	m.recentRows = []row{{"id": float64(9), "name": "Closed UI", "ended_at": float64(time.Now().Unix()), "can_resume_recent": false}}
	m.recentOpen = true
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.recentOpen || !m.busy {
		t.Fatalf("fallback did not close panel and load history: open=%v busy=%v cmd=%v rows=%d section=%d", m.recentOpen, m.busy, cmd != nil, len(m.recentRows), m.section)
	}
	if cmd == nil {
		t.Fatal("fallback did not schedule history request")
	}
	m.Update(cmd())
	if gotPath != "/api/sessions/9/conversations" || m.form == nil {
		t.Fatalf("fallback did not open native history picker: path=%q form=%v", gotPath, m.form != nil)
	}
}

func TestRecentSessionsViewportKeepsLastRowsReachable(t *testing.T) {
	m := sampleDashboard()
	for i := 0; i < 30; i++ {
		m.recentRows = append(m.recentRows, row{"id": float64(i + 1), "name": "Closed " + fmt.Sprint(i+1), "agent": "codex"})
	}
	m.recentSelected = 29
	m.width, m.height = 60, 15
	view := m.recentView(7)
	if !strings.Contains(view, "Closed 30") || strings.Contains(view, "Closed 1\n") {
		t.Fatalf("recent viewport did not keep selected tail visible:\n%s", view)
	}
}

func TestRecentSessionsShortcutIsAvailableOnlyInSessions(t *testing.T) {
	m := sampleDashboard()
	if _, cmd := m.Update(key("C")); cmd == nil {
		t.Fatal("C did not load recently closed sessions")
	}
	m.section = 1
	if _, cmd := m.Update(key("C")); cmd != nil {
		t.Fatal("C should be a sessions-only shortcut")
	}
}
