package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// Wave 9 (modal packet): one test per item, each pinning what a person sees.

func TestPickerCursorWrapsAtBothEnds(t *testing.T) {
	p := NewPicker(theme.Default(), "T", "")
	p.SetItems([]PickItem{{Value: "a", Label: "a"}, {Value: "b", Label: "b"}, {Value: "c", Label: "c", Disabled: true}})
	p.cursor = 0
	p.Update(tea.KeyMsg{Type: tea.KeyUp})
	if it, _ := p.Selected(); it.Value != "b" {
		t.Fatalf("up from the first row should wrap to the last enabled row, got %q", it.Value)
	}
	p.Update(tea.KeyMsg{Type: tea.KeyDown})
	if it, _ := p.Selected(); it.Value != "a" {
		t.Fatalf("down from the last enabled row should wrap to the first, got %q", it.Value)
	}
	// j and k wrap the same way.
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'k'}})
	if it, _ := p.Selected(); it.Value != "b" {
		t.Fatalf("k at the top should wrap, got %q", it.Value)
	}
}

func TestSlashSplitMatchesGroupThenItem(t *testing.T) {
	items := []PickItem{
		{Value: "1", Label: "commit", Group: "Git"},
		{Value: "2", Label: "commit-tree", Group: "Other"},
		{Value: "3", Label: "log", Group: "Git"},
	}
	if _, ok := matchItem(items[0], "git/commit"); !ok {
		t.Fatal("git/commit should match Git's commit")
	}
	if _, ok := matchItem(items[1], "git/commit"); ok {
		t.Fatal("git/commit must not match an item outside the Git group")
	}
	if _, ok := matchItem(items[2], "git/commit"); ok {
		t.Fatal("git/commit must not match Git's log")
	}
	if _, ok := matchItem(items[2], "git/"); !ok {
		t.Fatal("git/ with an empty item keeps every item of the group")
	}
	if _, ok := matchItem(items[0], "Git/Commit"); !ok {
		t.Fatal("matching is case-insensitive")
	}
}

type busyStub struct {
	*Picker
	busy bool
	hits int
}

func (b *busyStub) Busy() bool { return b.busy }
func (b *busyStub) Update(msg tea.Msg) (Modal, tea.Cmd, bool) {
	b.hits++
	return b, nil, false
}

func TestBusyModalSwallowsKeysButNotResults(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	stub := &busyStub{Picker: NewPicker(theme.Default(), "Busy", ""), busy: true}
	m.pushModal(stub)
	m.updateModal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if stub.hits != 0 {
		t.Fatalf("a busy modal must swallow keys, got %d", stub.hits)
	}
	m.updateModal(modalDone{owner: stub, apply: func() (tea.Cmd, bool) { return nil, false }})
	m.updateModal(tickMsg(time.Now()))
	if stub.hits != 2 {
		t.Fatalf("non-key messages must still reach a busy modal, got %d", stub.hits)
	}
	stub.busy = false
	m.updateModal(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if stub.hits != 3 {
		t.Fatalf("keys flow again once the work ends, got %d", stub.hits)
	}
}

func TestCtrlSTogglesSkillBrowserClosed(t *testing.T) {
	m := modalModel(t, 120, 40, NewFakeBackend())
	m.onKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	if topTitle(m) != "Skills" {
		t.Fatalf("ctrl+s should open the skill browser, top=%q", topTitle(m))
	}
	m.onKey(tea.KeyMsg{Type: tea.KeyCtrlS})
	if topTitle(m) == "Skills" {
		t.Fatal("a second ctrl+s should close the skill browser")
	}
}

func TestHelpShowsEachModeWithItsDescription(t *testing.T) {
	text := strings.Join(keyHelpLines(), "\n")
	for _, id := range modeOrder {
		mi := modeStyle(id, theme.Theme{})
		if !strings.Contains(text, mi.label) || !strings.Contains(text, mi.desc) {
			t.Fatalf("help should show %s with %q", mi.label, mi.desc)
		}
	}
}

func TestNewTabNameStopsAt30Characters(t *testing.T) {
	p := NewTextPrompt(theme.Default(), "New Tab", "name", false, nil).WithLimit(maxTabName)
	for _, r := range strings.Repeat("x", 45) {
		p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if n := len(p.value); n != maxTabName {
		t.Fatalf("tab name should stop at %d runes, got %d", maxTabName, n)
	}
}

func TestReleasePageAndExcerpt(t *testing.T) {
	if got := releasePage("2.7.0"); got != lecternRepo+"/releases/tag/v2.7.0" {
		t.Fatalf("release page: %s", got)
	}
	if got := releasePage("v2.7.0"); got != lecternRepo+"/releases/tag/v2.7.0" {
		t.Fatalf("a leading v must not double: %s", got)
	}
	if got := releasePage(""); got != lecternRepo+"/releases" {
		t.Fatalf("unknown version falls back to the index: %s", got)
	}
	u := &updateNotice{th: theme.Default(), st: UpdateStatus{Current: "2.6.0", Latest: "2.7.0", Newer: true, Known: true,
		Notes: []string{"feat: one", "fix: two"}}}
	out := strings.Join(u.lines(theme.Default()), "\n")
	mustContain(t, ansi.Strip(out), "What's new", "feat: one", "fix: two", "/releases/tag/v2.7.0")
}

func TestApiUpdateBusyWhileChecking(t *testing.T) {
	u := &updateNotice{th: theme.Default(), loading: true}
	if !u.Busy() {
		t.Fatal("the notice is busy while the release list is read")
	}
	u.loading = false
	if u.Busy() {
		t.Fatal("the notice is free once the check returns")
	}
}

func TestWizardOptionCycles(t *testing.T) {
	if nextOpacity(100) != 0 || nextOpacity(0) != 30 {
		t.Fatalf("opacity should cycle through the theme steps")
	}
	if nextBorder("") != "strong" || nextBorder("op") != "default" {
		t.Fatalf("border strength should cycle default, strong, op")
	}
}

func TestWizardSidebarAt80Columns(t *testing.T) {
	m := modalModel(t, 80, 24, NewFakeBackend())
	w := newWizard(m)
	w.step = wzModes
	view := ansi.Strip(w.View(72, 20))
	mustContain(t, view, "welcome", "intelligence", "shortcuts", "Modes", "auto, plan,")
	mustContain(t, view, "ARCHITECT", "design only")
}

func TestWizardNarrowFallsBackToStepCount(t *testing.T) {
	m := modalModel(t, 50, 24, NewFakeBackend())
	w := newWizard(m)
	w.step = wzIntel
	view := ansi.Strip(w.View(44, 20))
	mustContain(t, view, "Step 3 of 11", "Code Intel")
	if strings.Contains(view, "│") {
		t.Fatal("the narrow layout must not draw the sidebar")
	}
}

func TestWizardTourWalksEveryStep(t *testing.T) {
	m := modalModel(t, 80, 24, NewFakeBackend())
	w := newWizard(m)
	w.step = wzIcons
	// icons -> intelligence -> editing -> modes -> workflow -> automation ->
	// remote -> shortcuts -> theme: eight right presses.
	want := []wizStep{wzIntel, wzEdit, wzModes, wzWorkflow, wzAuto, wzRemote, wzShortcuts, wzTheme}
	for i, step := range want {
		w.onKey(tea.KeyMsg{Type: tea.KeyRight})
		if w.step != step {
			t.Fatalf("press %d should reach step %d, at %d", i+1, step, w.step)
		}
	}
	// the tour page keys: left goes back, Esc skips
	w.step = wzModes
	w.onKey(tea.KeyMsg{Type: tea.KeyLeft})
	if w.step != wzEdit {
		t.Fatalf("left should go back a page, at %v", w.step)
	}
	if _, _, done := w.onKey(tea.KeyMsg{Type: tea.KeyEsc}); !done {
		t.Fatal("esc on a tour page should skip the wizard")
	}
}

func TestWizardThemeOptionKeysApply(t *testing.T) {
	m := modalModel(t, 80, 24, NewFakeBackend())
	w := newWizard(m)
	w.enterTheme()
	w.onKey(tea.KeyMsg{Type: tea.KeyTab})
	if !w.style.Transparent {
		t.Fatal("tab toggles transparency")
	}
	w.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'m'}})
	if w.style.UserMessageOpacity == 100 {
		t.Fatal("m cycles the message background opacity")
	}
	w.onKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}})
	if w.style.BorderStrength != "strong" {
		t.Fatalf("b cycles border strength, got %q", w.style.BorderStrength)
	}
}

func TestErrorLogShowsArgsAndRelativeTime(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	m.addError("main", srcTool, "edit: file not found", time.Now().Add(-3*time.Minute))
	m.errLog[len(m.errLog)-1].Args = `{"path":"a.go"}`
	items := m.errorItems()
	if len(items) != 1 || !strings.Contains(items[0].Label, "3m") {
		t.Fatalf("error rows should show the relative time at the end, got %+v", items)
	}
	out := prettyArgs(m.errLog[0].Args)
	if !strings.Contains(out, "\"path\": \"a.go\"") {
		t.Fatalf("args should be pretty JSON: %s", out)
	}
	if got := prettyArgs("not json"); got != "not json" {
		t.Fatalf("non-JSON args pass through, got %q", got)
	}
}

func TestCompactionLogShowsModelAndAge(t *testing.T) {
	ev := agent.Event{Kind: agent.EvCompactDone, At: time.Now().Add(-2 * time.Hour),
		Meta: map[string]any{"model": "ollama/qwen3.6", "messages_before": 40, "messages_after": 8}}
	if got := compactModel(ev); got != "ollama/qwen3.6" {
		t.Fatalf("compactModel = %q", got)
	}
	if got := compactModel(agent.Event{}); got != "" {
		t.Fatalf("missing model should read as empty, got %q", got)
	}
}
