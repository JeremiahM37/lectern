package tui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fixedServers is a backend whose server list the test sets.
type fixedServers struct {
	*FakeBackend
	list []ServerStatus
}

func (f fixedServers) Servers() []ServerStatus { return f.list }

// editFile records one file change in the transcript the way a turn does.
func editFile(m *Model, path string, created bool) {
	t := m.cur()
	id := fmt.Sprintf("c%d", len(path))
	diff := "--- a/" + path + "\n+++ b/" + path + "\n@@ -1 +1 @@\n-a\n+b\n"
	if created {
		diff = "--- /dev/null\n+++ b/" + path + "\n@@ -0,0 +1 @@\n+x\n"
	}
	raw, _ := json.Marshal(map[string]any{"path": path})
	at := time.Now()
	t.chat.Apply(agent.Event{Kind: agent.EvTurnStart, At: at})
	t.chat.Apply(agent.Event{Kind: agent.EvToolStart, CallID: id, Tool: "edit_file", At: at})
	t.chat.Apply(agent.Event{Kind: agent.EvToolRun, CallID: id, Tool: "edit_file", Args: raw, At: at})
	t.chat.Apply(agent.Event{Kind: agent.EvToolResult, CallID: id, Tool: "edit_file", At: at,
		Result: &tools.Result{Output: "ok", Diff: diff, Files: []string{path}}})
	t.chat.Apply(agent.Event{Kind: agent.EvTurnDone, At: at, Stats: &agent.TurnStats{Duration: time.Second}})
}

// colourKey reads a palette colour as text so two colours can be compared.
func colourKey(c lipgloss.TerminalColor) string {
	if s, ok := c.(lipgloss.Color); ok {
		return strings.ToLower(string(s))
	}
	return fmt.Sprint(c)
}

// plainLines strips the colour codes so tests can read the layout.
func plainLines(s string) []string {
	return strings.Split(ansi.Strip(s), "\n")
}

func TestStatusBlockReportsFailedMCP(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.phase = phaseLanding
	m.tabs[0].be = fixedServers{FakeBackend: NewFakeBackend(), list: []ServerStatus{
		{Name: "gopls", Kind: "lsp", State: "ready"},
		{Name: "github", Kind: "mcp", State: "ready", Tools: 12},
		{Name: "postgres", Kind: "mcp", State: "failed", Error: "connection refused"},
	}}
	got := ansi.Strip(m.statusBlock())
	if !strings.Contains(got, "✕ MCP") {
		t.Fatalf("the fake backend has a failed MCP server; status %q", got)
	}
	if !strings.Contains(got, "1 MCP") {
		t.Fatalf("one MCP server is ready; status %q", got)
	}
	if strings.Contains(got, "LSP") {
		t.Fatalf("no LSP server failed, so the line should not name LSP: %q", got)
	}
}

func TestStatusBlockBlankWhenNothingToSay(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.phase = phaseLanding
	m.tabs[0].be = fixedServers{FakeBackend: NewFakeBackend(), list: []ServerStatus{{Name: "gopls", Kind: "lsp", State: "ready"}}}
	if got := m.statusBlock(); got != "" {
		t.Fatalf("a healthy LSP and no MCP should leave the line blank, got %q", ansi.Strip(got))
	}
}

func TestLandingInputWidthTweens(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.phase = phaseLanding
	if got := m.landingPct(); got != 60 {
		t.Fatalf("landing page input should be 60%% wide, got %d", got)
	}
	m.phase = phaseTransition
	m.trans = time.Now().Add(-300 * time.Millisecond)
	if got := m.landingPct(); got < 75 || got > 85 {
		t.Fatalf("halfway through the transition the input should be about 80%%, got %d", got)
	}
	m.phase = phaseChat
	if got := m.landingPct(); got != 100 {
		t.Fatalf("in the chat the input is the full width, got %d", got)
	}
}

func TestSidePanelOnlyOnWideScreens(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.phase = phaseChat
	if got := m.sideWidth(); got != 0 {
		t.Fatalf("a 100-column screen keeps the full width for the transcript, got side %d", got)
	}
	m.width = 120
	giveChange(m)
	if got := m.sideWidth(); got != 24 {
		t.Fatalf("a fifth of 120 columns is 24, got %d", got)
	}
	m.phase = phaseLanding
	if got := m.sideWidth(); got != 0 {
		t.Fatalf("the landing page has no side panels, got %d", got)
	}
}

func TestSidePanelsShowEmptyStates(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.phase = phaseChat
	giveChange(m)
	t0 := m.cur()
	col := plainLines(strings.Join(m.sideColumn(t0, 12), "\n"))
	if len(col) != 12 {
		t.Fatalf("the side column should be 12 rows, got %d", len(col))
	}
	joined := strings.Join(col, "\n")
	for _, want := range []string{"Changes", "main.go", "Terminals", "No terminals"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("side panel should say %q:\n%s", want, joined)
		}
	}
	for _, l := range col {
		if ansi.StringWidth(l) != m.sideWidth() {
			t.Fatalf("every side row should be %d cells, got %d: %q", m.sideWidth(), ansi.StringWidth(l), l)
		}
	}
}

func TestChangesPanelListsFilesWithStatus(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	t0 := m.cur()
	editFile(m, "internal/parser/parse.go", true)
	editFile(m, "cmd/app/main.go", false)
	rows := plainLines(strings.Join(m.changesPanel(t0, 22, 8), "\n"))
	joined := strings.Join(rows, "\n")
	if !strings.Contains(joined, "A") || !strings.Contains(joined, "M") {
		t.Fatalf("created and modified files should be marked A and M:\n%s", joined)
	}
	if !strings.Contains(joined, "├─") && !strings.Contains(joined, "└─") {
		t.Fatalf("files should hang off a tree:\n%s", joined)
	}
}

func TestTailTruncateKeepsTheFileName(t *testing.T) {
	got := tailTruncate("internal/scribe/tui/termpane_editor.go", 12)
	if !strings.HasSuffix(got, "editor.go") {
		t.Fatalf("the end of a path names the file; got %q", got)
	}
	if ansi.StringWidth(got) > 12 {
		t.Fatalf("too wide: %q", got)
	}
	if tailTruncate("short", 10) != "short" {
		t.Fatal("text that fits should come back unchanged")
	}
}

func TestShortPathAbbreviatesDeepPaths(t *testing.T) {
	home, _ := os.UserHomeDir()
	got := shortPath(filepath.Join(home, "projects", "a", "b", "c"))
	if !strings.HasPrefix(got, "~/") || !strings.Contains(got, "…") || !strings.HasSuffix(got, "b/c") {
		t.Fatalf("deep home path should read ~/…/b/c, got %q", got)
	}
	if shortPath("") != "" {
		t.Fatal("no cwd should give no row")
	}
}

func TestChatBorderIsFramedAtWidth(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.phase = phaseChat
	rows := m.chatRows()
	out := m.viewChatBody(rows)
	if len(out) < rows {
		t.Fatalf("chat body too short: %d < %d", len(out), rows)
	}
	first := ansi.Strip(out[0])
	if !strings.HasPrefix(first, "╭") || !strings.HasSuffix(strings.TrimRight(first, " "), "╮") {
		t.Fatalf("the transcript should open with a rounded top border, got %q", first)
	}
	for _, l := range out[:rows] {
		if ansi.StringWidth(l) != m.width {
			t.Fatalf("framed rows should be %d cells, got %d", m.width, ansi.StringWidth(l))
		}
	}
}

func TestChatBorderPingsWhileBusy(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	tb := m.cur()
	tb.busy = false
	idle := m.chatBorderColor(tb)
	if idle != m.th.TextSubtle {
		t.Fatal("an idle transcript should have the textSubtle border")
	}
	tb.busy = true
	seen := map[string]bool{}
	for f := 0; f < 12; f++ {
		m.frame = f
		seen[colourKey(m.chatBorderColor(tb))] = true
	}
	if len(seen) < 3 {
		t.Fatalf("the busy border should move through several colours, saw %d", len(seen))
	}
	m.frame = 6
	if colourKey(m.chatBorderColor(tb)) != colourKey(m.th.BrandSecondary) {
		t.Fatal("halfway through the ping the border should be brandSecondary")
	}
}

func TestWheelAccelerates(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	base := time.Now()
	if got := m.wheelLines(base); got != 3 {
		t.Fatalf("a single notch scrolls 3 lines, got %d", got)
	}
	if got := m.wheelLines(base.Add(20 * time.Millisecond)); got != 3 {
		t.Fatalf("the second notch of a pair still counts as 3 (speed-up starts at the third), got %d", got)
	}
	if got := m.wheelLines(base.Add(40 * time.Millisecond)); got != 6 {
		t.Fatalf("a quick run of notches scrolls 6 lines, got %d", got)
	}
	for i := 0; i < 5; i++ {
		m.wheelLines(base.Add(time.Duration(60+i*10) * time.Millisecond))
	}
	if got := m.wheelLines(base.Add(130 * time.Millisecond)); got != 9 {
		t.Fatalf("a long fling scrolls 9 lines, got %d", got)
	}
	if got := m.wheelLines(base.Add(time.Second)); got != 3 {
		t.Fatalf("after a pause the scroll goes back to 3, got %d", got)
	}
}

func TestSideColumnTakesNoWheel(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.phase = phaseChat
	giveChange(m)
	if !m.inSidePanel(tea.MouseMsg{X: 119, Y: 3}) {
		t.Fatal("the last column is inside the side panel")
	}
	if m.inSidePanel(tea.MouseMsg{X: 10, Y: 3}) {
		t.Fatal("the transcript is not the side panel")
	}
}

func TestShutdownFadeWalksTheStops(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	th := m.th
	if colourKey(shutdownFadeColor(th, 0)) != colourKey(th.Amber) {
		t.Fatal("the fade starts at amber")
	}
	if colourKey(shutdownFadeColor(th, 4*time.Second)) != colourKey(th.BrandDim) {
		t.Fatal("after the fade the wordmark is brandDim")
	}
	mid := shutdownFadeColor(th, shutdownFade/2)
	if colourKey(mid) == colourKey(th.Amber) || colourKey(mid) == colourKey(th.BrandDim) {
		t.Fatal("halfway should be a blend, not a stop")
	}
}

func TestShutdownStepsAndTiming(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	start := time.Now()
	m.phase = phaseShutdown
	m.sd = &shutdownState{start: start, step: 0}
	first := ansi.Strip(m.viewShutdown())
	if !strings.Contains(first, "Stopping running turns") || !strings.Contains(first, "Done") {
		t.Fatalf("the quit screen should list four steps:\n%s", first)
	}
	if strings.Contains(first, "✓") {
		t.Fatal("nothing is done at the start")
	}
	m.sd.step = 2
	m.sd.doneAt = start
	if cmd := m.tickShutdown(start.Add(shutdownHintAfter)); cmd != nil {
		t.Fatal("the hint step does not quit")
	}
	if m.sd.step != 3 {
		t.Fatalf("350 ms after the backends close the resume hint should show, step %d", m.sd.step)
	}
	if cmd := m.tickShutdown(start.Add(shutdownQuitAfter - time.Millisecond)); cmd != nil {
		t.Fatal("it must not quit before 1000 ms after the hint")
	}
	if cmd := m.tickShutdown(start.Add(shutdownQuitAfter)); cmd == nil {
		t.Fatal("the screen should quit 1350 ms after the backends close")
	}
	m.sd.step = 3
	done := ansi.Strip(m.viewShutdown())
	if !strings.Contains(done, "✓ Done") {
		t.Fatalf("the last step should be ticked when the hint shows:\n%s", done)
	}
}

func TestWizardFlagOpensSetup(t *testing.T) {
	fb := NewFakeBackend()
	fb.NeedSetup = false
	m := NewModel(fb, nil, Options{Width: 100, Height: 30, Wizard: true})
	m.Init()
	if _, ok := m.topModal().(*wizard); !ok {
		t.Fatalf("--wizard should open the setup wizard even when setup is not needed, top modal %T", m.topModal())
	}
}

func TestThemeStampTracksUserThemeFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	if got := themeStamp(); !got.IsZero() {
		t.Fatalf("no theme files, so no stamp; got %v", got)
	}
	dir := filepath.Join(home, "themes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mine.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := themeStamp()
	if stamp.IsZero() {
		t.Fatal("a theme file should give a stamp")
	}
	later := stamp.Add(time.Minute)
	if err := os.Chtimes(filepath.Join(dir, "mine.json"), later, later); err != nil {
		t.Fatal(err)
	}
	if !themeStamp().After(stamp) {
		t.Fatal("editing a theme should move the stamp forward")
	}
}

func TestFrameStepsOnlyEveryFrameGap(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.phase = phaseTransition
	m.trans = time.Now()
	now := time.Now()
	m.frameAt = now
	before := m.frame
	m.onTick(now.Add(16 * time.Millisecond))
	if m.frame != before {
		t.Fatal("a 16 ms tick during the transition must not step the animation")
	}
	m.onTick(now.Add(50 * time.Millisecond))
	if m.frame != before+1 {
		t.Fatalf("a full frame gap steps the animation once, frame %d -> %d", before, m.frame)
	}
}

func TestTabLabelScrollsWhenActive(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.tabs[0].name = "a-very-long-tab-name-that-needs-to-scroll"
	m.frame = 0
	first := ansi.Strip(m.viewTabBar())
	m.frame = 40
	later := ansi.Strip(m.viewTabBar())
	if first == later {
		t.Fatal("the active tab label should move as the frames go by")
	}
}

// giveChange records one finished edit in the current tab, so the side
// panels (which stay hidden while there is nothing to show) appear.
func giveChange(m *Model) {
	c := m.cur().chat
	now := time.Now()
	c.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "edit", At: now})
	c.Apply(agent.Event{Kind: agent.EvToolStart, CallID: "e1", Tool: "edit_file", At: now})
	c.Apply(agent.Event{Kind: agent.EvToolRun, CallID: "e1", Tool: "edit_file", Args: json.RawMessage(`{"path":"main.go"}`), At: now})
	r := tools.Result{Output: "ok", Files: []string{"main.go"}}
	c.Apply(agent.Event{Kind: agent.EvToolResult, CallID: "e1", Tool: "edit_file", Result: &r, At: now})
}

func TestSidePanelsHiddenWhileEmpty(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.phase = phaseChat
	if got := m.sideWidth(); got != 0 {
		t.Fatalf("with no changes and no terminals the transcript keeps the width, got side %d", got)
	}
	giveChange(m)
	if got := m.sideWidth(); got != 24 {
		t.Fatalf("after an edit the side panels appear at a fifth of 120 columns, got %d", got)
	}
}

// TestSubagentTurnDoneKeepsTheTabBusy: a subagent finishing must not end the
// main turn's busy state (the busy line and "Esc to stop" disappeared while
// dispatch ran).
func TestSubagentTurnDoneKeepsTheTabBusy(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.phase = phaseChat
	tb := m.cur()
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvTurnStart, Prompt: "go", At: time.Now()}})
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvStepFinish, CostUSD: 0.5, At: time.Now()}})
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvTurnDone, AgentID: "a1", CostUSD: 0.01, At: time.Now()}})
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvContext, AgentID: "a1", ContextUsed: 5, ContextWindow: 10}})
	if !tb.busy {
		t.Fatal("a subagent's turn_done ended the main turn")
	}
	if tb.cost != 0.5 {
		t.Fatalf("a subagent's cost replaced the session cost: %v", tb.cost)
	}
	if tb.ctxWin == 10 {
		t.Fatal("a subagent's context replaced the main context bar")
	}
}

// TestTurnDoneReadsTheModeBack: the agent may leave plan mode on its own
// (plan approved with Execute); the header must follow, not keep PLAN.
func TestTurnDoneReadsTheModeBack(t *testing.T) {
	m, fb := newTestModel(t, 120, 30)
	m.phase = phaseChat
	tb := m.cur()
	tb.mode = "plan"
	fb.SetMode("default")
	m.onEvent(eventMsg{tab: tb.id, ev: agent.Event{Kind: agent.EvTurnDone, At: time.Now()}})
	if tb.mode != "default" {
		t.Fatalf("tab mode = %q after the agent switched to default", tb.mode)
	}
}
