package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// openStatus runs /status and returns the dashboard that is now on top.
func openStatus(t *testing.T, m *Model) *statusView {
	t.Helper()
	m.runCommand("/status")
	v, ok := m.topModal().(*statusView)
	if !ok {
		t.Fatalf("/status should open the status dashboard, top modal is %q", topTitle(m))
	}
	return v
}

func TestStatusOpensAsModal(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	openStatus(t, m)
	if got := topTitle(m); got != "Status" {
		t.Fatalf("title = %q, want Status", got)
	}
}

func TestStatusOverviewShowsSessionModelModeAndGit(t *testing.T) {
	m, fb := newTestModel(t, 80, 24)
	openStatus(t, m)
	s := screen(m)
	mustContain(t, s, "Session", "Workdir", "Version", "0.9.0", "Model", "Provider", "Reasoning", "Mode", fb.Mode(), "Git", fb.Git().Branch)
	mustContain(t, s, "changed")
}

func TestStatusUsageShowsContextSpendBudgetAndSubagents(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	openStatus(t, m)
	send(m, "tab")
	s := screen(m)
	mustContain(t, s, "Context", "Window", "System", "Tools", "Conversation", "estimate")
	mustContain(t, s, "Spend", "Tokens", "in ·", "out ·", "Cost", "$", "Budget", "today")
	mustContain(t, s, "Subagents", "Dispatch")
}

func TestStatusServersShowsMCPLSPCodeMapAndMemory(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	openStatus(t, m)
	send(m, "tab")
	send(m, "tab")
	s := screen(m)
	mustContain(t, s, "MCP servers", "github", "postgres", "connection refused")
	mustContain(t, s, "Language servers", "gopls")
	mustContain(t, s, "Code map", "files", "symbols", "edges")
	mustContain(t, s, "Memory", "To review")
}

func TestStatusTabsSwitchAndWrap(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	v := openStatus(t, m)
	send(m, "right")
	if v.tab != 1 {
		t.Fatalf("right should go to Usage, tab = %d", v.tab)
	}
	send(m, "tab")
	send(m, "tab")
	if v.tab != 0 {
		t.Fatalf("tab past the last tab should wrap to Overview, tab = %d", v.tab)
	}
	send(m, "shift+tab")
	if v.tab != 2 {
		t.Fatalf("shift+tab from Overview should wrap to Servers, tab = %d", v.tab)
	}
	send(m, "left")
	if v.tab != 1 {
		t.Fatalf("left should go back to Usage, tab = %d", v.tab)
	}
}

// Every tab must fit the frame at an 80 column terminal, not only the big ones.
func TestStatusFitsEightyColumns(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {80, 14}, {100, 30}} {
		m, _ := newTestModel(t, size[0], size[1])
		openStatus(t, m)
		for tab := 0; tab < len(statusTabs); tab++ {
			checkWidths(t, m, size[0], size[1])
			send(m, "tab")
		}
	}
}

// A tab taller than the frame scrolls with PgUp and PgDn, and the first line
// of the tab goes out of view.
func TestStatusScrollsATallTab(t *testing.T) {
	m, _ := newTestModel(t, 80, 12)
	openStatus(t, m)
	send(m, "shift+tab") // Servers, the tallest tab
	if s := screen(m); !strings.Contains(s, "MCP servers") {
		t.Fatalf("Servers should start at its first section:\n%s", s)
	}
	send(m, "pgdown")
	s := screen(m)
	mustNotContain(t, s, "MCP servers")
	mustContain(t, s, "To review")
	send(m, "pgup")
	mustContain(t, screen(m), "MCP servers")
}

func TestStatusEscCloses(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	openStatus(t, m)
	send(m, "esc")
	if topTitle(m) == "Status" {
		t.Fatal("esc should close the status dashboard")
	}
}

// The reading is kept for statusRefresh, so a frame does not read the backend
// again, and it is read again once that time has passed.
func TestStatusRereadsOnlyAfterRefreshInterval(t *testing.T) {
	m, fb := newTestModel(t, 80, 24)
	openStatus(t, m)
	old := fb.Mode()
	const next = "socratic"
	if old == next {
		t.Fatalf("test needs a mode other than %q", next)
	}
	fb.SetMode(next)
	mustContain(t, screen(m), old)
	v := m.topModal().(*statusView)
	v.snap.at = time.Now().Add(-2 * statusRefresh)
	mustContain(t, screen(m), next)
}

func TestStatusNoProviderOrModelIsSaidPlainly(t *testing.T) {
	name, state := providerState(NewFakeBackend(), "")
	if name != "" || state != "no model selected" {
		t.Fatalf("empty model gave %q, %q", name, state)
	}
	_, state = providerState(NewFakeBackend(), "nowhere/some-model")
	if state != "not a known provider" {
		t.Fatalf("unknown provider gave %q", state)
	}
}

func TestSubagentTextCountsByStatus(t *testing.T) {
	got := subagentText([]string{"running", "done", "done", "error"})
	if got != "4 total: 1 running, 2 done, 1 error" {
		t.Fatalf("subagentText = %q", got)
	}
	if subagentText(nil) != "none" {
		t.Fatal("no subagents should read none")
	}
}

func TestGitTextSaysCleanAheadAndBehind(t *testing.T) {
	if got := gitText(GitStatus{Branch: "main"}); got != "main · clean" {
		t.Fatalf("clean branch = %q", got)
	}
	if got := gitText(GitStatus{Branch: "main", Dirty: true, Changed: []string{"a", "b"}, Ahead: 1, Behind: 2}); got != "main · 2 changed · 1 ahead · 2 behind" {
		t.Fatalf("dirty branch = %q", got)
	}
	if got := gitText(GitStatus{}); got != "(no branch)" {
		t.Fatalf("no git = %q", got)
	}
}

// Keys reach the dashboard only as key messages; other messages must not
// change the tab.
func TestStatusIgnoresNonKeyMessages(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	v := newStatusView(m.th, NewFakeBackend())
	if _, _, done := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24}); done || v.tab != 0 {
		t.Fatal("a window size message should not close or move the dashboard")
	}
}
