package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
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

// statusLSPBackend is the fake backend with one language server missing: it
// has no gopls, so its session failed.
type statusLSPBackend struct{ *FakeBackend }

func (b statusLSPBackend) LSPSessions() []LSPSession {
	return []LSPSession{{Language: "go", Root: "/work/repo", State: "failed", LastError: "no language server for go is installed (looked for gopls)"}}
}

// lineWith returns the first line of s containing sub, or "".
func lineWith(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

// selectSection jumps to section i (0-based) by its number key.
func selectSection(m *Model, i int) {
	send(m, string(rune('1'+i)))
}

func TestStatusOpensAsModal(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	openStatus(t, m)
	if got := topTitle(m); got != "Status" {
		t.Fatalf("title = %q, want Status", got)
	}
}

// Each section shows its own facts. The tall frame lets every section draw
// without scrolling.
func TestStatusSectionsShowTheOldFacts(t *testing.T) {
	m, fb := newTestModel(t, 80, 40)
	openStatus(t, m)
	want := map[int][]string{
		secOverview: {"Session", fb.SessionID(), "Workdir", fb.Workdir(), "Version", "0.9.0", "State", "idle"},
		secModel:    {"Model", fb.Model(), "Provider", "Reasoning", "Mode", fb.Mode()},
		secContext:  {"Window", "System", "Tools", "Conversation", "estimate", "Code map", "Map", "214 files", "Memory", "To review"},
		secServers:  {"MCP servers", "github", "postgres", "connection refused", "Language servers", "gopls"},
		secJobs:     {"Background", "go test -count=1 ./...", "Subagents", "Dispatch", "none"},
		secUsage:    {"Tokens", "in ·", "out ·", "Cost", "$", "Budget", "today"},
		secGit:      {"Git", fb.Git().Branch, "changed", "internal/parser/parse.go"},
	}
	for i := range statusSections {
		selectSection(m, i)
		if got := m.topModal().(*statusView).sec; got != i {
			t.Fatalf("key %d selected section %d", i+1, got)
		}
		mustContain(t, screen(m), want[i]...)
	}
}

func TestStatusKeysMoveAndJump(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	v := openStatus(t, m)
	send(m, "down")
	send(m, "j")
	if v.sec != 2 {
		t.Fatalf("down, j should reach Context, sec = %d", v.sec)
	}
	send(m, "up")
	send(m, "k")
	if v.sec != 0 {
		t.Fatalf("up, k should return to Overview, sec = %d", v.sec)
	}
	send(m, "k")
	if v.sec != len(statusSections)-1 {
		t.Fatalf("up from Overview should wrap to Git, sec = %d", v.sec)
	}
	send(m, "1")
	if v.sec != secOverview {
		t.Fatalf("1 should jump to Overview, sec = %d", v.sec)
	}
	send(m, "4")
	if v.sec != secServers {
		t.Fatalf("4 should jump to Servers, sec = %d", v.sec)
	}
	send(m, "7")
	if v.sec != secGit {
		t.Fatalf("7 should jump to Git, sec = %d", v.sec)
	}
	send(m, "tab")
	if v.sec != secOverview {
		t.Fatalf("tab past the last section should wrap to Overview, sec = %d", v.sec)
	}
	send(m, "shift+tab")
	if v.sec != secGit {
		t.Fatalf("shift+tab from Overview should wrap to Git, sec = %d", v.sec)
	}
	send(m, "8")
	if v.sec != secGit {
		t.Fatalf("8 is not a section and should change nothing, sec = %d", v.sec)
	}
}

func TestStatusEscAndQClose(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	openStatus(t, m)
	send(m, "esc")
	if topTitle(m) == "Status" {
		t.Fatal("esc should close the status dashboard")
	}
	m, _ = newTestModel(t, 80, 24)
	openStatus(t, m)
	send(m, "q")
	if topTitle(m) == "Status" {
		t.Fatal("q should close the status dashboard")
	}
}

// A language server that is missing shows a warning mark on its row and on
// the Servers section in the menu.
func TestStatusMissingLanguageServerWarnsInServers(t *testing.T) {
	m := modalModel(t, 80, 24, statusLSPBackend{NewFakeBackend()})
	openStatus(t, m)
	selectSection(m, secServers)
	s := screen(m)
	row := lineWith(s, "go repo")
	if row == "" {
		t.Fatalf("the missing language server should be listed:\n%s", s)
	}
	if !strings.Contains(row, "! go repo") {
		t.Fatalf("the missing language server should carry a warning mark: %q", row)
	}
	if !strings.Contains(s, "! Servers") {
		t.Fatalf("the Servers section should carry a warning mark in the menu:\n%s", s)
	}
}

// Every section fits an 80 column terminal, and the menu and pane share each
// line without any line running past the frame.
func TestStatusFitsEightyColumns(t *testing.T) {
	for _, size := range [][2]int{{80, 24}, {80, 14}, {100, 30}} {
		m, _ := newTestModel(t, size[0], size[1])
		openStatus(t, m)
		for i := range statusSections {
			selectSection(m, i)
			checkWidths(t, m, size[0], size[1])
		}
	}
	m, _ := newTestModel(t, 80, 24)
	openStatus(t, m)
	for i := range statusSections {
		selectSection(m, i)
		for _, l := range strings.Split(screen(m), "\n") {
			if w := ansi.StringWidth(l); w > 80 {
				t.Fatalf("section %s has a %d column line: %q", statusSections[i], w, l)
			}
		}
	}
}

// A pane taller than the frame scrolls with PgUp and PgDn.
func TestStatusPaneScrolls(t *testing.T) {
	m, _ := newTestModel(t, 80, 10)
	openStatus(t, m)
	selectSection(m, secServers)
	mustNotContain(t, screen(m), "gopls")
	send(m, "pgdown")
	mustContain(t, screen(m), "gopls")
	send(m, "pgup")
	mustNotContain(t, screen(m), "gopls")
}

// The reading is kept for statusRefresh, so a frame does not read the backend
// again, and it is read again once that time has passed.
func TestStatusRereadsOnlyAfterRefreshInterval(t *testing.T) {
	m, fb := newTestModel(t, 80, 24)
	openStatus(t, m)
	selectSection(m, secModel)
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

// A section with something wrong gets a mark; the others stay plain.
func TestStatusMarksSectionsThatNeedAttention(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	openStatus(t, m)
	s := screen(m)
	mustContain(t, s, "! Servers", "● Git")
}

// Keys reach the dashboard only as key messages; other messages must not
// change the section.
func TestStatusIgnoresNonKeyMessages(t *testing.T) {
	m, _ := newTestModel(t, 80, 24)
	v := newStatusView(m.th, NewFakeBackend())
	if _, _, done := v.Update(tea.WindowSizeMsg{Width: 80, Height: 24}); done || v.sec != 0 {
		t.Fatal("a window size message should not close or move the dashboard")
	}
}
