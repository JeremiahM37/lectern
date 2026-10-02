package console

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

func barText(m *dashboard) string { return ansi.Strip(renderKeyBar(m.keyBar(), 200)) }

func pendingApproval(sessionID float64) row {
	return row{"id": float64(31), "session_id": sessionID, "session_name": "API repair", "tool_name": "Bash",
		"status": "pending", "input": map[string]any{"command": "echo hi > NOTES.md"}}
}

// recordingServer answers every request with body and remembers what came in.
type recorded struct {
	mu       sync.Mutex
	requests []string
	bodies   []map[string]any
}

func recordingServer(t *testing.T, status int, body string) (*httptest.Server, *recorded) {
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		rec.mu.Lock()
		rec.requests = append(rec.requests, r.Method+" "+r.URL.Path)
		rec.bodies = append(rec.bodies, got)
		rec.mu.Unlock()
		w.WriteHeader(status)
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv, rec
}

func run(m *dashboard, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			run(m, c)
		}
		return
	}
	m.Update(msg)
}

func TestKeyBarShowsOnlyWhatWorksHere(t *testing.T) {
	m := sampleDashboard()
	selectID(m, "2") // ready
	if bar := barText(m); !strings.Contains(bar, "Enter attach") || !strings.Contains(bar, "x end") || !strings.Contains(bar, "r restore") || strings.Contains(bar, "allow") {
		t.Fatalf("ready session bar: %q", bar)
	}
	m.approvals = []row{pendingApproval(2)}
	if bar := barText(m); !strings.Contains(bar, "y allow once") || !strings.Contains(bar, "a allow for session") {
		t.Fatalf("needs-you bar: %q", bar)
	}
	m.rows[1]["agent_exited_at"] = float64(1)
	m.approvals = nil
	if bar := barText(m); !strings.Contains(bar, "r start agent again") {
		t.Fatalf("exited agent bar: %q", bar)
	}
	m.switchSection(1)
	m.rows = []row{pendingApproval(2)}
	m.filter()
	if bar := barText(m); !strings.Contains(bar, "y allow once") || !strings.Contains(bar, "n deny") || strings.Contains(bar, "n new") {
		t.Fatalf("approvals bar: %q", bar)
	}
	m.openHelp()
	if bar := barText(m); !strings.Contains(bar, "Esc close") || strings.Contains(bar, "y allow") {
		t.Fatalf("help bar: %q", bar)
	}
}

func TestKeyBarKeepsQuitAndHelpAtEveryWidth(t *testing.T) {
	for _, width := range []int{35, 45, 60, 80, 100, 160} {
		m := sampleDashboard()
		m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
		view := ansi.Strip(m.View())
		for _, want := range []string{"q quit", "? keys"} {
			if !strings.Contains(view, want) {
				t.Fatalf("width %d lost %q:\n%s", width, want, view)
			}
		}
		for _, line := range strings.Split(view, "\n") {
			if ansi.StringWidth(line) > width {
				t.Fatalf("width %d overflow: %q", width, line)
			}
		}
	}
}

func selectID(m *dashboard, want string) {
	for i, r := range m.visible {
		if id(r) == want {
			m.selected = i
		}
	}
	m.updatePreview()
}

// The audit's dead end: searching the menu for "end" ran Send message,
// because Enter ran the first hit and "send" contains "end".
func TestPaletteRanksEndSessionFirstAndRunsTheHighlightedItem(t *testing.T) {
	m := sampleDashboard()
	m.Update(key(":"))
	for _, r := range "end" {
		m.Update(key(string(r)))
	}
	list := m.paletteActions()
	if len(list) == 0 || list[0].Label != "End session" {
		t.Fatalf("end ranked %+v first", list)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	second := list[1]
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.palette {
		t.Fatal("Enter left the palette open")
	}
	if second.Warning != "" && (m.pending == nil || m.pending.Label != second.Label) {
		t.Fatalf("Enter did not run the highlighted item %q", second.Label)
	}
	if m.pending != nil && m.pending.Label == "End session" {
		t.Fatal("Enter ran the first match instead of the highlighted one")
	}
}

func TestContextMenuIsShortAndShowsKeys(t *testing.T) {
	m := sampleDashboard()
	for _, kind := range []string{"session", "approval", "project", "task"} {
		switch kind {
		case "approval":
			m.switchSection(1)
			m.rows = []row{pendingApproval(2)}
		case "project":
			m.switchSection(2)
			m.rows = []row{{"id": float64(4), "name": "Site"}}
		case "task":
			m.switchSection(3)
			m.rows = []row{{"id": float64(5), "title": "Fix", "attempt": map[string]any{"id": float64(6)}}}
		}
		m.filter()
		list := m.contextActions()
		if len(list) > menuLimit || list[len(list)-1].Operation != "palette" {
			t.Fatalf("%s menu: %d items %+v", kind, len(list), list)
		}
	}
	m = sampleDashboard()
	list := m.contextActions()
	var end *dashboardAction
	for i := range list {
		if list[i].Label == "End session" {
			end = &list[i]
		}
	}
	if end == nil || end.Key != "x" || list[0].Label != "Attach" {
		t.Fatalf("session menu lacks Attach first or End with its key: %+v", list)
	}
	m.Update(key("m"))
	if view := ansi.Strip(m.View()); !strings.Contains(view, "End session") || !strings.Contains(view, "All commands…") {
		t.Fatalf("menu view:\n%s", view)
	}
	// An item's own key runs it from the menu.
	m.Update(key("x"))
	if m.menu || m.pending == nil || m.pending.Confirm != "end" {
		t.Fatal("x in the menu did not ask to end the session")
	}
}

// Esc goes back and never quits; q goes back in sub-views and quits only at
// the top level.
func TestEscAndQStepBackOneLevel(t *testing.T) {
	m := sampleDashboard()
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc}); quitFrom(cmd) {
		t.Fatal("Esc quit from the top level")
	}
	for name, open := range map[string]func(){
		"help":    func() { m.openHelp() },
		"menu":    func() { m.openMenu() },
		"review":  func() { m.review = &codeReview{} },
		"restore": func() { m.recentOpen = true },
	} {
		open()
		_, cmd := m.Update(key("q"))
		if quitFrom(cmd) {
			t.Fatalf("q in %s quit the dashboard", name)
		}
		if m.help || m.menu || m.review != nil || m.recentOpen {
			t.Fatalf("q did not leave %s", name)
		}
	}
	m.openPalette("")
	m.Update(key("q"))
	if !m.palette || m.paletteQuery != "q" {
		t.Fatal("q in the palette is text")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if _, cmd := m.Update(key("q")); !quitFrom(cmd) {
		t.Fatal("q at the top level did not quit")
	}
}

func TestPaneNumbersAndTab(t *testing.T) {
	m := sampleDashboard()
	for k, want := range map[string]string{"2": "approvals", "3": "projects", "4": "tasks", "5": "targets", "6": "approvals", "1": "sessions"} {
		m.Update(key(k))
		if sections[m.section] != want {
			t.Fatalf("%s opened %s, want %s", k, sections[m.section], want)
		}
	}
	m.switchSection(0)
	for _, want := range []string{"approvals", "projects", "tasks", "sessions"} {
		m.Update(tea.KeyMsg{Type: tea.KeyTab})
		if sections[m.section] != want {
			t.Fatalf("Tab reached %s, want %s", sections[m.section], want)
		}
	}
	m.approvals = []row{pendingApproval(2)}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "2 Approvals (1)") || !strings.Contains(view, "needs you") {
		t.Fatalf("approval count or banner missing:\n%s", view)
	}
}

// Allow once is one key with no second question; allow for this session
// widens what the agent may do, so it asks first; n denies.
func TestApprovalKeysDecideInline(t *testing.T) {
	srv, rec := recordingServer(t, 200, `{"id":31,"status":"approved"}`)
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	m.approvals = []row{pendingApproval(2)}
	selectID(m, "2")
	if !strings.Contains(ansi.Strip(m.View()), "needs you") {
		t.Fatal("the session does not say it needs you")
	}
	_, cmd := m.Update(key("y"))
	if m.pending != nil || cmd == nil {
		t.Fatal("allow once asked for confirmation")
	}
	run(m, cmd)
	if len(rec.requests) == 0 || rec.requests[0] != "POST /api/approvals/31/decision" || rec.bodies[0]["decision"] != "approved" || rec.bodies[0]["for_session"] != nil {
		t.Fatalf("allow once sent %v %v", rec.requests, rec.bodies)
	}
	if !strings.Contains(m.notice, "Allowed once") {
		t.Fatalf("notice: %q", m.notice)
	}
	m.busy = false
	m.Update(key("a"))
	if m.pending == nil || m.pending.Confirm != "allow for this session" {
		t.Fatal("allow for this session did not ask first")
	}
	_, cmd = m.Update(key("y"))
	run(m, cmd)
	last := rec.bodies[len(rec.bodies)-1]
	if last["for_session"] != true || last["decision"] != "approved" {
		t.Fatalf("allow for session sent %v", last)
	}
	// n on Sessions still starts a new session; on Approvals it denies.
	m.busy = false
	m.switchSection(1)
	m.rows = []row{pendingApproval(2)}
	m.filter()
	_, cmd = m.Update(key("n"))
	run(m, cmd)
	last = rec.bodies[len(rec.bodies)-1]
	if last["decision"] != "denied" {
		t.Fatalf("n did not deny: %v", last)
	}
	m.busy = false
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if sections[m.section] != "sessions" || m.focusSessionID != "2" {
		t.Fatalf("Enter did not open the asking session: section=%s focus=%q", sections[m.section], m.focusSessionID)
	}
	_ = cmd
}

func TestEndKeyAsksAndRestoreKeyBringsBack(t *testing.T) {
	srv, rec := recordingServer(t, 200, `{}`)
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	selectID(m, "1")
	m.Update(key("x"))
	if m.pending == nil || m.pending.Method != "DELETE" || m.pending.Path != "/sessions/1" {
		t.Fatalf("x did not ask to end: %+v", m.pending)
	}
	if view := ansi.Strip(m.View()); !strings.Contains(view, "r (Restore) brings it back") || !strings.Contains(view, "y end") {
		t.Fatalf("confirmation:\n%s", view)
	}
	m.Update(key("n"))
	if m.pending != nil || len(rec.requests) != 0 {
		t.Fatal("n did not cancel")
	}
	m.Update(key("d"))
	_, cmd := m.Update(key("y"))
	run(m, cmd)
	if len(rec.requests) == 0 || rec.requests[0] != "DELETE /api/sessions/1" || !strings.Contains(m.notice, "r brings it back") {
		t.Fatalf("end: %v notice=%q", rec.requests, m.notice)
	}
	// r on an agent that exited starts it again; elsewhere it opens Restore.
	m = sampleDashboard()
	m.rows[0]["agent_exited_at"] = float64(1)
	m.filter()
	selectID(m, "1")
	m.Update(key("r"))
	if m.pending == nil || m.pending.Label != reviveLabel {
		t.Fatal("r did not offer to start the exited agent")
	}
	m.pending = nil
	selectID(m, "2")
	if _, cmd := m.Update(key("r")); cmd == nil || !m.busy {
		t.Fatal("r on a running session did not load the Restore list")
	}
}

func TestHelpScrollsAndFiltersInsteadOfClosingOnAnyKey(t *testing.T) {
	m := sampleDashboard()
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	m.Update(key("?"))
	first := ansi.Strip(m.View())
	m.Update(tea.KeyMsg{Type: tea.KeyPgDown})
	if !m.help || ansi.Strip(m.View()) == first {
		t.Fatal("help did not scroll")
	}
	m.Update(key("/"))
	for _, r := range "restore" {
		m.Update(key(string(r)))
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "restore") || strings.Contains(view, "rename") {
		t.Fatalf("help filter:\n%s", view)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(key("x"))
	if !m.help || m.pending != nil {
		t.Fatal("a stray key closed help or acted behind it")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.help {
		t.Fatal("Esc did not close help")
	}
}

func TestNewSessionOffersOnlyInstalledAgents(t *testing.T) {
	m := sampleDashboard()
	m.targets = []row{{"id": float64(1), "name": "Here", "kind": "local"}}
	m.agents = []row{{"name": "codex", "builtin": true}, {"name": "claude", "builtin": true}, {"name": "gemini", "builtin": true}}
	m.agentMenuOrder = []string{"codex", "claude", "gemini"}
	m.agentState = map[string]map[string]string{"1": {"codex": "missing", "claude": "available", "gemini": "missing"}}
	m.newForm()
	agent := m.form.fields[1]
	if agent.Key != "agent" || agent.Value != "claude" || len(agent.Options) != 1 {
		t.Fatalf("agent field: %+v", agent)
	}
	if !strings.Contains(m.formView(), "Not installed here: codex, gemini") {
		t.Fatalf("form does not say what is missing:\n%s", m.formView())
	}
	m.form = nil
	m.agentState["1"] = map[string]string{"codex": "missing", "claude": "missing", "gemini": "missing"}
	m.newForm()
	if m.form == nil || !strings.Contains(m.formView(), "No agent CLI was found") || !strings.Contains(m.formView(), "not found on this machine") {
		t.Fatalf("a machine with no agents was not explained: %q", m.notice)
	}
}

func TestNewSessionChecksTheMachineOnceThenOpens(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/api/settings":
			fmt.Fprint(w, `{"session_permission_mode":"bypass"}`)
		case "/api/targets/1/agents":
			fmt.Fprint(w, `[{"name":"claude","state":"available"},{"name":"codex","state":"missing"}]`)
		}
	}))
	defer srv.Close()
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	m.targets = []row{{"id": float64(1), "name": "Here", "kind": "local"}}
	m.agents = []row{{"name": "codex"}, {"name": "claude"}}
	_, cmd := m.Update(key("n"))
	if m.form != nil || cmd == nil {
		t.Fatal("the form opened before the machine was checked")
	}
	run(m, cmd)
	if m.form == nil || m.form.fields[1].Value != "claude" || m.form.fields[2].Value != "bypass" {
		t.Fatalf("form after check: %+v", m.form)
	}
	m.form = nil
	m.Update(key("n"))
	if m.form == nil || len(paths) != 2 {
		t.Fatalf("the machine was checked again: %v", paths)
	}
}

func TestNewSessionStartsInTheFolderItWasOpenedIn(t *testing.T) {
	m := sampleDashboard()
	m.cwd = "/home/me/site/src"
	m.projects = []row{{"id": float64(3), "name": "Other", "repo_path": "/home/me/other"}, {"id": float64(4), "name": "Site", "repo_path": "/home/me/site"}}
	m.recentProjects = []int64{3}
	m.newForm()
	if got := m.form.fields[0].Value; got != "project:4" {
		t.Fatalf("where = %q, want the project containing the folder", got)
	}
	m.form = nil
	m.cwd = "/home/me/loose"
	m.recentProjects = nil
	m.rows = nil
	m.filter()
	m.newForm()
	if got := m.form.fields[0].Value; got != "dir:/home/me/loose" {
		t.Fatalf("where = %q, want this folder", got)
	}
	if !strings.Contains(m.formView(), "This folder — /home/me/loose") {
		t.Fatal("the folder is not offered by name")
	}
}

// Enter moves through the questions and starts the session on the last one;
// More options folds the rest out of the way.
func TestNewSessionEnterStartsOnTheLastQuestion(t *testing.T) {
	srv, rec := recordingServer(t, 201, `{"id":9,"name":"Site"}`)
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	m.attach = func(string, string) error { return nil }
	m.projects = []row{{"id": float64(4), "name": "Site", "repo_path": "/srv/site"}}
	m.newForm()
	visible := 0
	for i := range m.form.fields {
		if fieldVisible(m.form.fields, i) {
			visible++
		}
	}
	if visible != 5 {
		t.Fatalf("the first screen shows %d fields, want four questions and More options", visible)
	}
	var cmd tea.Cmd
	for i := 0; i < 3; i++ {
		cmd = m.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil && len(rec.requests) > 0 {
			t.Fatal("submitted before the last question")
		}
	}
	for _, r := range "hello" {
		m.updateForm(key(string(r)))
	}
	cmd = m.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
	run(m, cmd)
	if len(rec.requests) != 1 || rec.bodies[0]["prime"] != "hello" || rec.bodies[0]["name"] != "Site" || rec.bodies[0]["project_id"] != float64(4) {
		t.Fatalf("sent %v %v", rec.requests, rec.bodies)
	}
	if m.focusSessionID != "9" || !m.attachAfterRefresh {
		t.Fatal("the new session is not attached like `lectern claude`")
	}
}

func TestReviewCommitExplainsARefusalAndReturnsToTheReview(t *testing.T) {
	srv, rec := recordingServer(t, 409, `{"detail":"refusing to commit directly on \"main\" — this session has no isolated branch or worktree"}`)
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	selectID(m, "1")
	m.review = &codeReview{base: "/term/session/1/changes", scope: "working"}
	m.Update(key("c"))
	if m.form == nil || m.review != nil {
		t.Fatal("c did not open the commit form")
	}
	for _, r := range "Add notes" {
		m.updateForm(key(string(r)))
	}
	run(m, m.updateForm(tea.KeyMsg{Type: tea.KeyEnter}))
	if len(rec.requests) != 1 || rec.requests[0] != "POST /api/sessions/1/git/commit" || rec.bodies[0]["stage_all"] != true {
		t.Fatalf("commit sent %v %v", rec.requests, rec.bodies)
	}
	if !strings.Contains(m.formView(), "Ctrl+] |") || m.form == nil {
		t.Fatalf("refusal not explained with the next step:\n%s", m.formView())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.form != nil || m.review == nil {
		t.Fatal("Esc did not return to the review")
	}
}

// The dashboard says what the web says: the server's state when it sends
// one, and the same rules applied to the raw facts when it does not.
func TestSessionStatusUsesTheSharedVocabulary(t *testing.T) {
	m := sampleDashboard()
	for _, tc := range []struct {
		r        row
		approval bool
		want     string
	}{
		{row{"state": "idle", "state_label": "Idle", "status": "waiting"}, false, "Idle"},
		{row{"state": "ended", "state_reason": "agent_exited"}, false, "Stopped"},
		{row{"state": "idle"}, true, "Needs you"},
		{row{"status": "waiting"}, false, "Idle"},
		{row{"status": "running"}, false, "Working"},
		{row{"status": "running"}, true, "Needs you"},
		{row{"status": "idle", "agent_exited_at": float64(1)}, false, "Stopped"},
		{row{"status": "idle", "setup_state": "creating"}, false, "Working · setting up"},
		{row{"status": "dead", "ended_at": float64(1)}, false, "Ended"},
		{row{"status": "idle", "agent_state": "waiting_permission"}, false, "Needs you · permission prompt"},
	} {
		tc.r["id"] = float64(1)
		m.approvals = nil
		if tc.approval {
			m.approvals = []row{pendingApproval(1)}
		}
		if got := m.sessionStatus(tc.r); got != tc.want {
			t.Errorf("%v (approval %v) = %q, want %q", tc.r, tc.approval, got, tc.want)
		}
	}
}

// On a session that works straight on main, commit offers a new branch
// first; main itself is the second choice and asks again.
func TestReviewCommitOnMainOffersANewBranch(t *testing.T) {
	srv, rec := recordingServer(t, 200, `{"steps":[]}`)
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	selectID(m, "1")
	review := &codeReview{base: "/term/session/1/changes", scope: "working", data: reviewData{Branch: "main"}}
	m.review = review
	m.Update(key("c"))
	m.updateForm(tea.KeyMsg{Type: tea.KeyTab})
	if m.form.index != 0 || m.notice != "Commit message is required" {
		t.Fatal("empty commit message was allowed past its field")
	}
	for _, r := range "Add notes" {
		m.updateForm(key(string(r)))
	}
	if !strings.Contains(m.formView(), "lectern/alpha-ui") {
		t.Fatalf("no new branch offered:\n%s", m.formView())
	}
	run(m, m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlS}))
	if len(rec.bodies) != 1 || rec.bodies[0]["new_branch"] != "lectern/alpha-ui" || rec.bodies[0]["allow_base_branch"] != nil {
		t.Fatalf("new-branch commit sent %v", rec.bodies)
	}
	if m.review != review || !strings.Contains(m.notice, "this folder is now on lectern/alpha-ui") {
		t.Fatalf("did not return to the review: notice=%q", m.notice)
	}
	m.busy = false
	m.Update(key("c"))
	for _, r := range "Straight on main" {
		m.updateForm(key(string(r)))
	}
	m.form.fields[1].Value = "base"
	m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.pending == nil || len(rec.bodies) != 1 {
		t.Fatal("committing onto main did not ask first")
	}
	if !strings.Contains(ansi.Strip(m.View()), "straight onto main") {
		t.Fatalf("the confirmation is not shown:\n%s", ansi.Strip(m.View()))
	}
	_, cmd := m.Update(key("y"))
	run(m, cmd)
	if len(rec.bodies) != 2 || rec.bodies[1]["allow_base_branch"] != true || m.review == nil {
		t.Fatalf("main commit sent %v, review=%v", rec.bodies, m.review != nil)
	}
}

// Answering from the attached terminal's Ctrl+] m popup returns to the
// agent at once.
func TestPopupClosesAfterAnApprovalDecision(t *testing.T) {
	srv, _ := recordingServer(t, 200, `{}`)
	m := controlsDashboard(DashboardOptions{Popup: true})
	m.client = New(srv.URL, "")
	m.rows = []row{runningSessionRow(2, "Asking")}
	m.filter()
	m.approvals = []row{pendingApproval(2)}
	m.openMenu()
	list := m.contextActions()
	if list[0].Label != "Allow once" || list[2].Label != "Deny" {
		t.Fatalf("popup menu does not lead with the answers: %+v", list)
	}
	_, cmd := m.Update(key("y"))
	if cmd == nil {
		t.Fatal("y did not allow")
	}
	if _, next := m.Update(cmd()); !quitFrom(next) {
		t.Fatal("the popup stayed open after allowing")
	}
}

// A machine with no git name and email: the commit form asks for both right
// there, saves them for every repository, and commits — no detour to a
// shell to run git config.
func TestReviewCommitAsksForAGitIdentity(t *testing.T) {
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var got map[string]any
		json.NewDecoder(r.Body).Decode(&got)
		rec.mu.Lock()
		rec.requests = append(rec.requests, r.Method+" "+r.URL.Path)
		rec.bodies = append(rec.bodies, got)
		rec.mu.Unlock()
		if got["identity"] == nil {
			w.WriteHeader(409)
			fmt.Fprint(w, `{"detail":"Git needs your name and email before it can commit.","code":"no_git_identity"}`)
			return
		}
		fmt.Fprint(w, `{"steps":[]}`)
	}))
	t.Cleanup(srv.Close)
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	selectID(m, "1")
	review := &codeReview{base: "/term/session/1/changes", scope: "working"}
	m.review = review
	m.Update(key("c"))
	for _, r := range "Add notes" {
		m.updateForm(key(string(r)))
	}
	run(m, m.updateForm(tea.KeyMsg{Type: tea.KeyEnter}))
	if m.form == nil || m.form.title != "Git needs your name and email" {
		t.Fatalf("no identity form after the refusal: notice=%q", m.notice)
	}
	view := ansi.Strip(m.formView())
	if strings.Contains(view, "git config --global user.name \"") || !strings.Contains(view, "all your repositories") {
		t.Fatalf("identity form:\n%s", view)
	}
	for _, r := range "Ada Lovelace" {
		m.updateForm(key(string(r)))
	}
	m.updateForm(tea.KeyMsg{Type: tea.KeyTab})
	for _, r := range "ada@example.invalid" {
		m.updateForm(key(string(r)))
	}
	run(m, m.updateForm(tea.KeyMsg{Type: tea.KeyCtrlS}))
	if len(rec.bodies) != 2 {
		t.Fatalf("sent %v", rec.bodies)
	}
	identity, _ := rec.bodies[1]["identity"].(map[string]any)
	if rec.bodies[1]["message"] != "Add notes" || identity["name"] != "Ada Lovelace" || identity["email"] != "ada@example.invalid" || identity["scope"] != "global" {
		t.Fatalf("retry sent %v", rec.bodies[1])
	}
	if m.form != nil || m.review != review || !strings.Contains(m.notice, "Committed: Add notes") {
		t.Fatalf("did not return to the review: form=%v notice=%q", m.form != nil, m.notice)
	}
}
