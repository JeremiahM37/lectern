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
		if r.URL.Path != "/api/sessions/restorable" || r.URL.Query().Get("limit") != "100" {
			t.Fatalf("recent request: %s", r.URL.String())
		}
		json.NewEncoder(w).Encode([]row{{
			"id": float64(9), "name": "Closed UI", "project_name": "Lectern",
			"agent": "codex", "ended_at": float64(time.Now().Add(-2 * time.Hour).Unix()),
			"reason_label": "Exited on its own", "action": "resume", "action_label": "Resume",
			"preview": "the parser fix is merged",
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
	if !strings.Contains(view, "Closed UI") || !strings.Contains(view, "Lectern") || !strings.Contains(view, "codex") || !strings.Contains(view, "2h") ||
		!strings.Contains(view, "Exited on its own") || !strings.Contains(view, "Resume — “the parser fix is merged”") {
		t.Fatalf("recent view omitted human labels:\n%s", view)
	}
	if strings.Contains(view, "action_label") || strings.Contains(view, "ended_at") {
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
	m.recentRows = []row{{"id": float64(9), "name": "Closed UI", "ended_at": float64(time.Now().Unix()), "action": "history", "action_label": "Choose history"}}
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

func TestRestoreSearchFiltersByLastMessage(t *testing.T) {
	m := sampleDashboard()
	m.recentOpen = true
	m.recentRows = []row{
		{"id": float64(1), "name": "One", "preview": "fix the parser"},
		{"id": float64(2), "name": "Two", "preview": "write docs"},
	}
	m.Update(key("/"))
	for _, r := range "docs" {
		m.Update(key(string(r)))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.recentSearching || len(m.recentVisible()) != 1 || name(m.recentSelectedRow()) != "Two" {
		t.Fatalf("search did not filter: searching=%v visible=%v", m.recentSearching, m.recentVisible())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !m.recentOpen || len(m.recentVisible()) != 2 {
		t.Fatal("Esc should clear the search before closing the view")
	}
}

func TestRestoreEnterReopensAndAttaches(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{"id": float64(12)}, "message": "Resumed its saved conversation."})
	}))
	defer srv.Close()
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	m.recentOpen = true
	m.recentRows = []row{{"id": float64(9), "name": "Closed", "action": "resume", "action_label": "Resume"}}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(cmd())
	if got != "POST /api/sessions/9/reopen" || m.recentOpen || m.focusSessionID != "12" || !m.attachAfterRefresh || m.notice != "Resumed its saved conversation." {
		t.Fatalf("reopen: got=%q open=%v focus=%q attach=%v notice=%q", got, m.recentOpen, m.focusSessionID, m.attachAfterRefresh, m.notice)
	}
}

func TestUndoReopensTheNewestClosedSessionWithoutAttaching(t *testing.T) {
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		if r.Method == "GET" {
			json.NewEncoder(w).Encode([]row{{"id": float64(7), "name": "Just closed", "action": "resume"}})
			return
		}
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{"id": float64(8)}, "message": "Resumed its saved conversation."})
	}))
	defer srv.Close()
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	_, cmd := m.Update(key("U"))
	if cmd == nil {
		t.Fatal("U did not schedule an undo")
	}
	m.Update(cmd())
	if strings.Join(calls, ",") != "GET /api/sessions/restorable?limit=1,POST /api/sessions/7/reopen" {
		t.Fatalf("undo requests: %v", calls)
	}
	if m.focusSessionID != "8" || m.attachAfterRefresh || m.notice != "Resumed its saved conversation." {
		t.Fatalf("undo result: focus=%q attach=%v notice=%q", m.focusSessionID, m.attachAfterRefresh, m.notice)
	}
}

func TestAnExitedAgentShowsAndRevivesWithR(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Method + " " + r.URL.Path
		w.WriteHeader(201)
		json.NewEncoder(w).Encode(map[string]any{"id": float64(5), "name": "Alpha UI"})
	}))
	defer srv.Close()
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	m.width, m.height = 120, 30
	m.rows[0]["agent_exited_at"] = float64(1)
	m.filter()
	for i, r := range m.visible {
		if id(r) == "1" {
			m.selected = i
		}
	}
	if !strings.Contains(m.View(), "Stopped") {
		t.Fatalf("an exited agent must not read as its screen status:\n%s", m.View())
	}
	m.Update(key("R"))
	if m.pending == nil || m.pending.Label != reviveLabel {
		t.Fatal("R should ask before reviving")
	}
	_, cmd := m.Update(key("y"))
	m.Update(cmd())
	if got != "POST /api/sessions/1/revive" || m.focusSessionID != "5" || !m.attachAfterRefresh {
		t.Fatalf("revive: got=%q focus=%q attach=%v", got, m.focusSessionID, m.attachAfterRefresh)
	}
	m.selected = 1 - m.selected
	m.Update(key("R"))
	if m.pending != nil || !strings.Contains(m.notice, "still running") {
		t.Fatalf("R on a running agent: pending=%v notice=%q", m.pending, m.notice)
	}
}

func TestRelaunchNoticeShowsOncePerRun(t *testing.T) {
	m := sampleDashboard()
	m.Update(refsMsg{relaunched: []row{{"id": float64(3), "name": "came back"}}})
	if !strings.Contains(m.notice, "Relaunched 1 session(s) after a restart: came back") {
		t.Fatalf("notice: %q", m.notice)
	}
	m.notice = ""
	m.Update(refsMsg{relaunched: []row{{"id": float64(3), "name": "came back"}}})
	if m.notice != "" {
		t.Fatal("the relaunch notice repeated")
	}
}
