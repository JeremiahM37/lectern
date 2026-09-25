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
	"github.com/charmbracelet/x/ansi"
)

func key(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }
func sampleDashboard() *dashboard {
	m := newDashboard(New("http://unused", ""), nil)
	m.rows = []row{{"id": float64(1), "name": "Alpha UI", "project_name": "Website", "target_name": "Laptop", "status": "running", "pane_tail": "hello"}, {"id": float64(2), "name": "API repair", "project_name": "Backend", "target_name": "Server", "status": "waiting", "pane_tail": "needs input"}}
	m.filter()
	return m
}

func TestSessionActionsExposeConversationPromotionForUnassignedNativeCandidate(t *testing.T) {
	m := newDashboard(New("http://unused", ""), nil)
	m.section = 0
	m.rows = []row{{"id": float64(42), "name": "Shell", "agent": "shell", "status": "running", "workdir": "/tmp/work", "tmux_session": "lec-shell"}}
	m.filter()
	found := false
	for _, action := range m.rowActions() {
		if action.Label == "Promote conversation" && action.Operation == "promote-conversation" {
			found = true
		}
	}
	if !found {
		t.Fatal("unassigned live shell has no Promote conversation action")
	}
}

func TestPromotionPreviewPreservesProofAndSessionBinding(t *testing.T) {
	m := newDashboard(New("http://unused", ""), nil)
	m.section = 0
	m.rows = []row{{"id": float64(42), "name": "first", "agent": "shell", "status": "running"}}
	m.filter()
	m.promotionPreviewForm([]byte(`{"session_id":42,"identity":{"agent":"claude","cid":"cid-1","pid":17,"proc_start":"start-1","workdir":"/srv/exact","tmux_session":"lec-42","tracking_identity":"track-1","future_proof":"retain-me"},"existing_projects":[{"id":7,"name":"Exact","repo_path":"/srv/exact"}]}`))
	if m.form == nil {
		t.Fatal("valid promotion preview did not open form")
	}
	m.rows = []row{{"id": float64(99), "name": "changed selection", "agent": "shell", "status": "running"}}
	m.filter()
	if cmd := m.form.submit(map[string]any{"project_id": "7"}); cmd != nil {
		t.Fatal("confirmation should be deferred until y")
	}
	if m.pending == nil || m.pending.Path != "/sessions/42/promote" {
		t.Fatalf("promotion changed session binding: %#v", m.pending)
	}
	proof, ok := m.pending.Body.(map[string]any)["expected_identity"].(json.RawMessage)
	if !ok || !strings.Contains(string(proof), `"future_proof":"retain-me"`) {
		t.Fatalf("identity proof was not preserved: %#v", m.pending.Body)
	}
}

func TestReadableInteractiveDetailsUseLabelsAndRetainUnknownFields(t *testing.T) {
	got := readable(map[string]any{
		"status":         "running",
		"provider_extra": map[string]any{"region": "west", "count": float64(2)},
		"labels":         []any{"urgent", "ui"},
		"nested":         []map[string]string{{"name": "one"}},
		"message":        "first line\nsecond line\x1b]52;c;bad\a",
		"empty":          map[string]any{},
	})
	if strings.ContainsAny(got, "{}\"\x1b") {
		t.Fatalf("interactive details still look like raw JSON: %q", got)
	}
	for _, want := range []string{"Status: running", "Provider Extra", "Region: west", "Count: 2", "• urgent", "• ui", "Name: one", "Message: first line", "second line", "Empty: (none)"} {
		if !strings.Contains(got, want) {
			t.Errorf("readable output missing %q:\n%s", want, got)
		}
	}
}

func TestReadableJSONFieldsHideEmptyAndExpandNonemptyValues(t *testing.T) {
	got := readable(map[string]any{
		"env_json":      `{}`,
		"settings_json": `{"region":"west","retries":2}`,
	})
	if strings.Contains(got, "Env Json") || strings.ContainsAny(got, "{}\"") {
		t.Fatalf("JSON storage encoding leaked into interactive details: %q", got)
	}
	for _, want := range []string{"Settings", "Region: west", "Retries: 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("readable JSON field missing %q:\n%s", want, got)
		}
	}
}

func TestFormatDetailKeepsGenericAPIFieldsReadable(t *testing.T) {
	got := formatDetail("API result", []byte(`{"status":"ready","limits":{"max":3},"provider_flag":true}`))
	if strings.ContainsAny(got, "{}\"") {
		t.Fatalf("generic detail regressed to JSON: %q", got)
	}
	for _, want := range []string{"Status: ready", "Limits", "Max: 3", "Provider Flag: true"} {
		if !strings.Contains(got, want) {
			t.Errorf("detail missing %q:\n%s", want, got)
		}
	}
}

func TestReadableDashboardPreviewAtWideAndNarrowTerminalWidths(t *testing.T) {
	m := sampleDashboard()
	m.section = 4
	m.rows = []row{{"id": float64(1), "name": "Fixture target", "provider_extra": map[string]any{"region": "west", "mode": "fixture"}}}
	m.filter()
	m.selected = 0
	for _, width := range []int{120, 52} {
		m.Update(tea.WindowSizeMsg{Width: width, Height: 30})
		view := m.View()
		if strings.Contains(view, "provider_extra") || strings.ContainsAny(view, "{}\"") {
			t.Fatalf("width %d rendered raw JSON:\n%s", width, view)
		}
		if width >= 100 && !strings.Contains(view, "Provider Extra") {
			t.Fatalf("width %d omitted readable preview:\n%s", width, view)
		}
	}
}
func TestDashboardSearchGroupingAndSelectionSurviveRefresh(t *testing.T) {
	m := sampleDashboard()
	m.selected = 1
	chosen := id(m.current())
	m.rows = append(m.rows, row{"id": float64(3), "name": "A new arrival", "project_name": "Backend"})
	m.filter()
	if id(m.current()) != chosen {
		t.Fatal("refresh jumped selection")
	}
	m.query.SetValue("@ apir")
	m.filter()
	if len(m.visible) != 1 || id(m.current()) != "2" {
		t.Fatalf("fuzzy waiting search: %v", m.visible)
	}
	m.query.SetValue("")
	m.attention = true
	m.filter()
	if len(m.visible) != 1 {
		t.Fatal("attention filter")
	}
	m.attention = false
	m.grouping = 1
	m.filter()
	if m.group(m.current()) == "Backend" {
		t.Fatal("target grouping not applied")
	}
}

func TestDashboardSearchKeepsWordsInsideFields(t *testing.T) {
	m := newDashboard(New("http://unused", ""), nil)
	m.rows = []row{
		{"id": float64(1), "name": "Alpha", "project_name": "Backend", "target_name": "Laptop", "status": "idle"},
		{"id": float64(2), "name": "Delta", "project_name": "Alpha", "target_name": "Laptop", "status": "idle"},
		{"id": float64(3), "name": "Build", "project_name": "Nightly", "target_name": "Shift", "status": "idle"},
		{"id": float64(4), "name": "安全🧪", "project_name": "Lab", "target_name": "Laptop", "status": "idle"},
	}
	m.query.SetValue("Alpha")
	m.filter()
	if len(m.visible) != 2 {
		t.Fatalf("single-field search should match both Alpha fields: %#v", m.visible)
	}
	m.query.SetValue("night shift")
	m.filter()
	if len(m.visible) != 1 || id(m.visible[0]) != "3" {
		t.Fatalf("multiword search should span fields by word: %#v", m.visible)
	}
	m.query.SetValue("taal")
	m.filter()
	if len(m.visible) != 0 {
		t.Fatalf("search must not bridge fields: %#v", m.visible)
	}
	m.query.SetValue("安全")
	m.filter()
	if len(m.visible) != 1 || id(m.visible[0]) != "4" {
		t.Fatalf("unicode field search: %#v", m.visible)
	}
}
func TestDashboardIgnoresStaleResponsesAndKeepsRowsOnFailure(t *testing.T) {
	m := sampleDashboard()
	cmd := m.refresh()
	_ = cmd
	m.switchSection(1)
	m.Update(rowsMsg{section: "sessions", generation: 1, rows: []row{{"id": float64(9)}}})
	if len(m.rows) != 0 {
		t.Fatal("stale tab response applied")
	}
	m.Update(rowsMsg{section: "tasks", generation: m.generation, rows: []row{{"id": float64(4), "title": "Keep me"}}})
	m.Update(rowsMsg{section: "tasks", generation: m.generation, err: fmt.Errorf("offline")})
	if len(m.rows) != 1 || !strings.Contains(m.View(), "OFFLINE") {
		t.Fatal("offline state lost last snapshot")
	}
	m.Update(resultMsg{preview: true, key: "sessions/9", label: "Wrong", data: []byte(`{"text":"stale"}`)})
	if strings.Contains(m.preview.View(), "stale") {
		t.Fatal("stale preview overwrote selection")
	}
}
func TestDashboardFitsUnicodeAndUntrustedOutput(t *testing.T) {
	m := sampleDashboard()
	m.rows[0]["name"] = "安全な名前🧪 repeated repeated repeated"
	m.rows[0]["pane_tail"] = "\x1b]52;c;c2VjcmV0\x07\x1b[2Jline\nlong 🧪 日本語 preview"
	m.filter()
	for _, size := range [][2]int{{35, 12}, {60, 18}, {80, 24}, {120, 35}, {180, 45}} {
		m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for _, mode := range []string{"list", "help", "menu"} {
			m.help = mode == "help"
			m.menu = mode == "menu"
			v := m.View()
			if strings.Contains(v, "\x1b]52") || strings.Contains(v, "\x1b[2J") {
				t.Fatal("remote control sequence escaped")
			}
			lines := strings.Split(v, "\n")
			if len(lines) > size[1] {
				t.Fatalf("%s at %v: %d lines", mode, size, len(lines))
			}
			for _, line := range lines {
				if ansi.StringWidth(line) > size[0] {
					t.Fatalf("%s at %v: width %d: %q", mode, size, ansi.StringWidth(line), line)
				}
			}
		}
	}
}
func TestDashboardFormsUseNamesPreserveDraftsAndSubmitRealHTTP(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/sessions" || r.Method != "POST" {
			t.Errorf("wrong route %s %s", r.Method, r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{"id":4}`)
	}))
	defer srv.Close()
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	m.projects = []row{{"id": float64(7), "name": "Named project"}}
	m.targets = []row{{"id": float64(8), "name": "Named target"}}
	m.newForm()
	for i := range m.form.fields {
		f := &m.form.fields[i]
		switch f.Key {
		case "name":
			f.Value = "Test name"
		case "project_id":
			f.Value = "7"
		case "prime":
			f.Value = "first line\nsecond line"
		}
	}
	m.form.index = 1
	m.focusField()
	if !strings.Contains(m.formView(), "Named project") {
		t.Fatal("project name absent")
	}
	m.Update(rowsMsg{section: "sessions", generation: m.generation, rows: m.rows})
	if m.form.fields[0].Value != "Test name" {
		t.Fatal("refresh replaced draft")
	}
	cmd := m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlS})
	msg := cmd()
	m.Update(msg)
	if got["name"] != "Test name" || got["project_id"] != float64(7) || got["target_id"] != nil || got["prime"] != "first line\nsecond line" || got["yolo"] != false {
		t.Fatalf("body: %#v", got)
	}
	if m.form != nil {
		t.Fatal("successful form stayed open")
	}
}

func TestDashboardProjectPickerFiltersAndPreservesSelection(t *testing.T) {
	m := sampleDashboard()
	m.projects = make([]row, 100)
	for i := range m.projects {
		m.projects[i] = row{"id": float64(i + 1), "name": fmt.Sprintf("Project %02d", i+1)}
	}
	m.section = 0 // sessions
	m.newForm()
	projectIndex := -1
	for i, f := range m.form.fields {
		if f.Key == "project_id" {
			projectIndex = i
			if !f.Searchable {
				t.Fatal("project field is not searchable")
			}
		}
	}
	if projectIndex < 0 {
		t.Fatal("project field missing")
	}
	m.form.index = projectIndex
	m.focusField()

	for _, r := range []rune("Project 0") {
		m.updateForm(key(string(r)))
	}
	if got := len(filteredChoices(m.form.fields[projectIndex])); got != 9 {
		t.Fatalf("filtered project count = %d, want 9", got)
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyDown})
	if m.form.fields[projectIndex].Value != "2" {
		t.Fatalf("filtered down selected %q, want 2", m.form.fields[projectIndex].Value)
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlU})

	for _, r := range []rune("Project 99") {
		m.updateForm(key(string(r)))
	}
	if got := len(filteredChoices(m.form.fields[projectIndex])); got != 1 {
		t.Fatalf("filtered project count = %d, want 1", got)
	}
	if !strings.Contains(m.formView(), "1 matches") || !strings.Contains(m.formView(), "Project 99") {
		t.Fatalf("picker view omitted filter state: %s", m.formView())
	}
	if cmd := m.updateForm(tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatal("entering a project field should only move focus")
	}
	if m.form.fields[projectIndex].Value != "99" {
		t.Fatalf("selected project = %q, want 99", m.form.fields[projectIndex].Value)
	}
	if m.form.index != projectIndex+2 {
		t.Fatalf("enter advanced to field %d, want %d", m.form.index, projectIndex+2)
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.form.index != projectIndex || m.form.fields[projectIndex].Value != "99" {
		t.Fatalf("shift-tab lost project selection: index=%d value=%q", m.form.index, m.form.fields[projectIndex].Value)
	}

	// A miss must not submit or replace the still-valid project value.
	for _, r := range []rune("does-not-exist") {
		m.updateForm(key(string(r)))
	}
	if len(filteredChoices(m.form.fields[projectIndex])) != 0 {
		t.Fatal("expected no matching projects")
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
	if m.form.index != projectIndex || m.form.fields[projectIndex].Value != "99" {
		t.Fatalf("no-result enter changed form state: index=%d value=%q", m.form.index, m.form.fields[projectIndex].Value)
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyTab})
	if m.form.index != projectIndex || m.form.fields[projectIndex].Value != "99" {
		t.Fatalf("no-result tab changed form state: index=%d value=%q", m.form.index, m.form.fields[projectIndex].Value)
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.form.index != projectIndex || m.form.fields[projectIndex].Value != "99" {
		t.Fatalf("no-result submit changed form state: index=%d value=%q", m.form.index, m.form.fields[projectIndex].Value)
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlU})
	if m.form.fields[projectIndex].OptionFilter != "" || m.form.fields[projectIndex].Value != "99" {
		t.Fatalf("ctrl-u did not clear filter safely: filter=%q value=%q", m.form.fields[projectIndex].OptionFilter, m.form.fields[projectIndex].Value)
	}
	body, err := formBody([]field{m.form.fields[projectIndex]})
	if err != nil || body["project_id"] != int64(99) {
		t.Fatalf("form body project = %#v, err=%v", body["project_id"], err)
	}
}

func TestDashboardNonSearchableOptionKeepsExistingChoiceOnEnter(t *testing.T) {
	m := sampleDashboard()
	m.openForm("Choice", []field{{Key: "mode", Label: "Mode", Value: "second", Options: []choice{{"First", "first"}, {"Second", "second"}}}, {Key: "name", Label: "Name"}}, func(map[string]any) tea.Cmd { return nil })
	m.focusField()
	m.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
	if m.form.fields[0].Value != "second" || m.form.index != 1 {
		t.Fatalf("enter changed non-searchable option: value=%q index=%d", m.form.fields[0].Value, m.form.index)
	}
}

func TestDashboardProjectFilterCommitsOnTab(t *testing.T) {
	m := sampleDashboard()
	m.projects = []row{{"id": float64(1), "name": "Alpha"}, {"id": float64(2), "name": "Beta"}}
	m.newForm()
	projectIndex := 2
	m.form.index = projectIndex
	m.focusField()
	for _, r := range []rune("Beta") {
		m.updateForm(key(string(r)))
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyTab})
	if m.form.fields[projectIndex].Value != "2" || m.form.index != projectIndex+2 {
		t.Fatalf("tab did not commit filtered project: value=%q index=%d", m.form.fields[projectIndex].Value, m.form.index)
	}
}

func TestDashboardProjectSelectionDerivesTargetAndSkipsOverrides(t *testing.T) {
	m := sampleDashboard()
	delete(m.rows[0], "project_id")
	m.rows[0]["target_id"] = float64(1)
	m.projects = []row{
		{"id": float64(1), "name": "Project A", "target_id": float64(1)},
		{"id": float64(2), "name": "Project B", "target_id": float64(2)},
	}
	m.targets = []row{{"id": float64(1), "name": "Target A"}, {"id": float64(2), "name": "Target B"}}
	m.newForm()
	projectIndex, targetIndex, workdirIndex := -1, -1, -1
	for i, f := range m.form.fields {
		switch f.Key {
		case "project_id":
			projectIndex = i
		case "target_id":
			targetIndex = i
		case "workdir":
			workdirIndex = i
		}
	}
	m.form.fields[0].Value = "derive target"
	m.form.fields[targetIndex].Value = "2"
	m.form.fields[workdirIndex].Value = "/scratch/draft"
	m.form.index = projectIndex
	m.focusField()
	for _, r := range []rune("Project B") {
		m.updateForm(key(string(r)))
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
	if m.form.fields[projectIndex].Value != "2" {
		t.Fatalf("selected project = %q, want 2", m.form.fields[projectIndex].Value)
	}
	if fieldVisible(m.form.fields, targetIndex) || fieldVisible(m.form.fields, workdirIndex) {
		t.Fatal("target and directory remained editable with a project selected")
	}
	if m.form.fields[targetIndex].Value != "" || m.form.fields[workdirIndex].Value != "" {
		t.Fatalf("stale project overrides remain: target=%q workdir=%q", m.form.fields[targetIndex].Value, m.form.fields[workdirIndex].Value)
	}
	if m.form.index != targetIndex+1 { // project -> agent, skipping target
		t.Fatalf("focus landed on field %d, want agent field %d", m.form.index, targetIndex+1)
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.form.index != projectIndex {
		t.Fatalf("shift-tab did not return to previous visible project field: %d", m.form.index)
	}
	m.form.fields[projectIndex].Value = ""
	m.syncProjectTarget()
	if m.form.fields[targetIndex].Value != "2" || m.form.fields[workdirIndex].Value != "/scratch/draft" {
		t.Fatalf("clearing project did not restore manual scratch draft: target=%q workdir=%q", m.form.fields[targetIndex].Value, m.form.fields[workdirIndex].Value)
	}
	m.form.index = projectIndex
	m.focusField()
	m.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
	if m.form.index != targetIndex || !fieldVisible(m.form.fields, targetIndex) {
		t.Fatalf("scratch session did not expose target field: index=%d visible=%v", m.form.index, fieldVisible(m.form.fields, targetIndex))
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyLeft})

	body, err := formBody(m.form.fields)
	if err != nil {
		t.Fatal(err)
	}
	if body["project_id"] != nil || body["target_id"] == nil {
		t.Fatalf("scratch body did not retain target choice: %#v", body)
	}
}

func TestDashboardFailedMutationKeepsDraft(t *testing.T) {
	m := sampleDashboard()
	m.renameForm()
	m.form.editor.SetValue("keep my draft")
	m.saveField()
	m.busy = true
	m.Update(resultMsg{err: fmt.Errorf("connection lost")})
	if m.form == nil || m.form.fields[0].Value != "keep my draft" || m.busy {
		t.Fatal("failed request discarded draft")
	}
}

func TestDashboardMCPSettingsEditorUsesRevisionAndKeepsDraftOnConflict(t *testing.T) {
	requests, reads := 0, 0
	var retryBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/projects/7/mcp" {
			t.Fatalf("wrong MCP path: %s", r.URL.Path)
		}
		if r.Method == "GET" {
			reads++
			if reads == 1 {
				fmt.Fprint(w, `{"mcp":{"fixture":{"command":"fixture"}},"revision":"rev-1","strict_mcp":false}`)
			} else {
				fmt.Fprint(w, `{"mcp":{"fixture":{"command":"other-writer"}},"revision":"rev-2","strict_mcp":false}`)
			}
			return
		}
		requests++
		if requests == 1 {
			w.WriteHeader(http.StatusConflict)
			fmt.Fprint(w, `{"detail":"MCP settings changed elsewhere"}`)
			return
		}
		if err := json.NewDecoder(r.Body).Decode(&retryBody); err != nil {
			t.Fatal(err)
		}
		fmt.Fprint(w, `{"mcp":{},"revision":"rev-2","strict_mcp":false}`)
	}))
	defer srv.Close()
	m := newDashboard(New(srv.URL, ""), nil)
	m.section = 3
	m.rows = []row{{"id": float64(7), "name": "Fixture"}}
	m.filter()
	msg := m.mcpSettingsForm()()
	m.Update(msg)
	if m.form == nil || m.form.fields[0].Key != "mcp" || !strings.Contains(m.form.fields[0].Value, "fixture") {
		t.Fatalf("MCP form did not load: %#v", m.form)
	}
	m.form.editor.SetValue(`{"fixture":{"command":"changed"}}`)
	cmd := m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlS})
	m.Update(cmd())
	if m.form == nil || !strings.Contains(m.form.fields[0].Value, "changed") || !strings.Contains(m.notice, "changed elsewhere") {
		t.Fatalf("conflict discarded MCP draft: form=%#v notice=%q", m.form, m.notice)
	}
	// A second Ctrl-s retries exactly the preserved draft with the refreshed
	// revision; the fixture server accepts it to prove no re-fetch overwrote it.
	m.form.editor.SetValue(`{"fixture":{"command":"changed"}}`)
	cmd = m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlS})
	m.Update(cmd())
	if m.form != nil || requests != 2 || retryBody["revision"] != "rev-2" {
		t.Fatalf("MCP retry did not submit preserved draft: form=%v requests=%d", m.form != nil, requests)
	}
}
func TestDashboardTaskCreateDispatchDoesNotDuplicateAfterPartialFailure(t *testing.T) {
	creates, dispatches := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tasks":
			creates++
			fmt.Fprint(w, `{"id":12}`)
		case "/api/tasks/12/dispatch":
			dispatches++
			w.WriteHeader(409)
			fmt.Fprint(w, `{"detail":"target unavailable"}`)
		default:
			t.Error(r.URL.Path)
		}
	}))
	defer srv.Close()
	m := newDashboard(New(srv.URL, ""), nil)
	m.section = 1
	m.openForm("New task", []field{{Key: "title", Label: "Title", Value: "One task"}}, nil)
	cmd := m.createAndDispatch(map[string]any{"title": "One task", "project_id": 7})
	m.Update(cmd())
	if creates != 1 || dispatches != 1 || m.form != nil || !strings.Contains(m.notice, "Created task 12; dispatch failed") {
		t.Fatalf("partial failure: %d %d %s", creates, dispatches, m.notice)
	}
}
func TestDashboardDestructiveActionsRequireConfirmationAndRetainIdentity(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Method != "DELETE" || r.URL.Path != "/api/sessions/2" {
			t.Error(r.URL.Path)
		}
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	m.current()["origin"] = "discovered"
	actions := m.actions()
	a := actions[len(actions)-1]
	if !strings.Contains(a.Label, "leave running") {
		t.Fatal("adopted session warning wrong")
	}
	if cmd := m.choose(a); cmd != nil || m.pending == nil {
		t.Fatal("deletion bypassed confirmation")
	}
	m.Update(key("n"))
	if requests != 0 || m.pending != nil {
		t.Fatal("cancel sent request")
	}
	m.choose(a)
	m.selected = 1
	_, cmd := m.Update(key("y"))
	m.Update(cmd())
	if requests != 1 {
		t.Fatal("confirmation did not run once")
	}
}

func TestEndedAndArchivedWorktreesRemainRemovableWithConfirmation(t *testing.T) {
	for _, archived := range []bool{false, true} {
		m := sampleDashboard()
		r := m.current()
		r["ended_at"] = float64(1)
		if archived {
			r["archived_at"] = float64(1)
		}
		r["workspace"] = map[string]any{"state": "failed", "path": "/owned/failed"}
		found := false
		for _, action := range m.actions() {
			if action.Method == "DELETE" && strings.HasSuffix(action.Path, "/worktree") {
				found = true
				if cmd := m.choose(action); cmd != nil || m.pending == nil {
					t.Fatal("worktree removal bypassed confirmation")
				}
			}
		}
		if !found {
			t.Fatalf("worktree cleanup missing for archived=%v", archived)
		}
	}
}
func TestDashboardDiffHistoryAndEmptyResourceViews(t *testing.T) {
	got := formatDetail("Diff", []byte(`{"files":[{"path":"main.go","patch":"@@ -1 +1 @@\n-old\n+new"}]}`))
	if !strings.Contains(got, "\n-old\n+new") {
		t.Fatal("diff rendered as escaped JSON")
	}
	m := newDashboard(New("http://unused", ""), nil)
	m.updated = time.Now()
	m.Update(resultMsg{preview: true, key: m.key(), label: "Usage", data: []byte(`{"total":42}`)})
	if !strings.Contains(m.preview.View(), "42") {
		t.Fatal("global resource hidden on empty list")
	}
}

func TestDashboardProcessesBatchedKeysButDoesNotExecutePastes(t *testing.T) {
	m := sampleDashboard()
	m.Update(key("/repair"))
	if !m.searching || m.query.Value() != "repair" || len(m.visible) != 1 {
		t.Fatal("batched search keys lost")
	}
	m.searching = false
	m.query.Blur()
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2n"), Paste: true})
	if m.section != 0 || m.form != nil {
		t.Fatal("paste executed commands")
	}
}

func TestDashboardPreviewDoesNotPadPastTerminalWidth(t *testing.T) {
	m := sampleDashboard()
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 35})
	if strings.Contains(strings.Join(strings.Split(m.View(), "\n")[4:30], "\n"), "…") {
		t.Fatal("short list and preview unnecessarily truncated")
	}
}

func TestDashboardNativeEditorsCanClearOptionalFields(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	m.projects = []row{{"id": float64(8), "name": "Project"}}
	m.current()["project_id"] = float64(8)
	m.editCommonForm()
	for i := range m.form.fields {
		if m.form.fields[i].Key == "project_id" {
			m.form.fields[i].Value = ""
		}
	}
	m.form.index = 1
	m.focusField()
	cmd := m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlS})
	m.Update(cmd())
	if v, ok := got["project_id"]; !ok || v != nil {
		t.Fatalf("unassign must send explicit null: %v", got)
	}
	m.notificationForm([]byte(`{"discord_webhook":"https://old.example","ntfy_server":"","ntfy_topic":""}`))
	m.form.editor.SetValue("")
	cmd = m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlS})
	m.Update(cmd())
	if got["discord_webhook"] != "" {
		t.Fatal("could not clear webhook")
	}
}

func TestDashboardNamedGroupsAndWorkspaceSearch(t *testing.T) {
	m := sampleDashboard()
	m.rows[0]["group_path"] = "Work/Client"
	m.rows[0]["workspace"] = map[string]any{"branch": "feature/reader"}
	m.grouping = 3
	m.query.SetValue("Work/Client")
	m.filter()
	if len(m.visible) != 1 || m.group(m.current()) != "Work/Client" {
		t.Fatal(m.visible)
	}
	m.query.SetValue("feature/reader")
	m.filter()
	if len(m.visible) != 1 {
		t.Fatal("branch search missing")
	}
}

func TestConfirmationWrapsConversationIdentity(t *testing.T) {
	m := sampleDashboard()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	cid := "11111111-1111-4111-8111-111111111111"
	m.pending = &dashboardAction{Label: "Resume conversation", Warning: "The previous terminal must be stopped. Continue this same saved history in its original workspace? Conversation: " + cid}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, cid) || !strings.Contains(view, "y Confirm") {
		t.Fatalf("confirmation clipped essential details:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > 80 {
			t.Fatal("confirmation wrapped outside screen")
		}
	}
}

func TestSetupPhaseGuardsAttachmentAndKeepsFailuresInAttention(t *testing.T) {
	m := sampleDashboard()
	for _, phase := range []string{"creating", "failed"} {
		m.rows = []row{{"id": float64(9), "name": "Workspace", "status": "dead", "setup_state": phase, "setup_error": "checkout error"}}
		m.filter()
		if cmd := m.attachSelected(false); cmd != nil {
			t.Fatalf("attachment allowed during %s", phase)
		}
		if !strings.Contains(strings.ToLower(m.notice), "setup") && !strings.Contains(m.notice, "setting up") {
			t.Fatalf("missing setup explanation: %s", m.notice)
		}
	}
	m.attention = true
	m.filter()
	if len(m.visible) != 1 {
		t.Fatal("failed setup vanished from attention view")
	}
}

func TestDashboardSessionMouseAttachesOnlyVisibleRows(t *testing.T) {
	m := sampleDashboard()
	m.attach = func(string, string) error { return nil }
	m.width = 120
	m.height = 35
	click := tea.MouseMsg{X: 1, Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	_, cmd := m.Update(click)
	if cmd == nil {
		t.Fatal("session click did not request native attachment")
	}
	click.Y = m.height - 1
	if _, cmd = m.Update(click); cmd != nil {
		t.Fatal("footer click attached a hidden row")
	}
	click.Y = 4
	m.recentOpen = true
	if _, cmd = m.Update(click); cmd != nil {
		t.Fatal("recent overlay attached underlying row")
	}
	m.recentOpen = false
	m.width = 80
	m.previewFocus = true
	if _, cmd = m.Update(click); cmd != nil {
		t.Fatal("narrow preview click attached hidden row")
	}
	m.previewFocus = false
	click.Action = tea.MouseActionRelease
	if _, cmd = m.Update(click); cmd != nil {
		t.Fatal("mouse release attached twice")
	}
}

func TestDashboardBackgroundTabsKeepDefaultAttachAndBatchMode(t *testing.T) {
	m := sampleDashboard()
	m.attach = func(string, string) error { return nil }
	opened := 0
	m.openTerminal = func(kind, id string, batch bool) error {
		if kind != "session" || id == "" {
			t.Fatal("wrong target")
		}
		opened++
		return nil
	}
	_, cmd := m.Update(key("o"))
	if cmd == nil {
		t.Fatal("no background open")
	}
	m.Update(cmd())
	if opened != 1 || m.busy {
		t.Fatal("open did not complete")
	}
	m.openBatch = func(ids []string) error { opened += len(ids); return nil }
	m.Update(key("b"))
	if !m.batchOpen {
		t.Fatal("batch toggle")
	}
	m.Update(key(" "))
	if len(m.batchSelected) != 1 {
		t.Fatal("selection not marked")
	}
	if opened != 1 {
		t.Fatal("selecting opened a terminal early")
	}
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("batch Enter ignored")
	}
	m.Update(cmd())
	if opened != 2 {
		t.Fatal("batch Enter attached in place")
	}
	m.Update(key("b"))
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || opened != 2 {
		t.Fatal("normal Enter changed")
	}
	m.controlOnly = true
	if _, cmd = m.Update(key("o")); cmd != nil {
		t.Fatal("controls popup opened terminal")
	}
}

func TestBatchSelectionAndRightClickAreIndependent(t *testing.T) {
	m := sampleDashboard()
	m.width = 120
	m.height = 35
	opened := 0
	m.openTerminal = func(kind, id string, batch bool) error { opened++; return nil }
	m.Update(key("b"))
	click := tea.MouseMsg{X: 2, Y: 4, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}
	if _, cmd := m.Update(click); cmd != nil {
		t.Fatal("selection launched early")
	}
	if len(m.batchSelected) != 1 || !strings.Contains(m.View(), "[x]") {
		t.Fatal("missing visible selection")
	}
	click.Button = tea.MouseButtonRight
	_, cmd := m.Update(click)
	if cmd == nil {
		t.Fatal("right click ignored")
	}
	m.Update(cmd())
	if opened != 1 || len(m.batchSelected) != 1 {
		t.Fatal("right click modified selection")
	}
	click.Action = tea.MouseActionRelease
	if _, cmd = m.Update(click); cmd != nil {
		t.Fatal("release opened twice")
	}
}
