package tui

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"
)

// ---- busy line: a readable word, and flashes that show

func TestBusyPhraseNamesAWaitForTheUser(t *testing.T) {
	tb := &tab{pui: &promptUI{kind: "question"}}
	if got := busyPhrase(tb, time.Now()); got != "Waiting for your answer" {
		t.Fatalf("a question waiting on the user reads %q", got)
	}
	tb = &tab{pui: &promptUI{kind: "approval"}}
	if got := busyPhrase(tb, time.Now()); got != "Waiting for your approval" {
		t.Fatalf("an approval waiting on the user reads %q", got)
	}
}

func TestBusyPhraseQueuedPromptIsWaiting(t *testing.T) {
	tb := &tab{prompts: []Prompt{{Question: &tools.Question{}}}}
	if got := busyPhrase(tb, time.Now()); got != "Waiting for your answer" {
		t.Fatalf("a queued question reads %q", got)
	}
	tb = &tab{prompts: []Prompt{{Approval: &tools.Approval{}}}}
	if got := busyPhrase(tb, time.Now()); got != "Waiting for your approval" {
		t.Fatalf("a queued approval reads %q", got)
	}
}

func TestBusyPhraseIsNeverGarble(t *testing.T) {
	// a tab with no live turn: the phrase is the chat's idle word, readable
	m, _ := newTestModel(t, 120, 40)
	_ = m
	tb := &tab{chat: chat.New(chat.Options{})}
	got := busyPhrase(tb, time.Now())
	if chat.IsGarbled(got) || got == "" {
		t.Fatalf("busy phrase must be readable, got %q", got)
	}
	// a garbled phrase falls back to the last readable word
	tb.lastPhrase = "Writing changes"
	if chat.IsGarbled("▓▒░") != true || chat.IsGarbled("Writing changes") {
		t.Fatalf("IsGarbled must tell garble from words")
	}
}

func TestFlashShowsOnBusyLineAtNarrowWidth(t *testing.T) {
	// A turn is running and a long flash lands (the /cancel all text). The
	// flash takes the status word's place and is cut to fit, so the stop hint
	// stays on the row.
	m, _ := newTestModel(t, 80, 30)
	settle(m)
	m.phase = phaseChat
	t0 := m.cur()
	t0.busy = true
	t0.since = time.Now().Add(-3 * time.Minute)
	m.setFlash("ok", "Cancelled the running dispatch; finished subagents keep their results.")
	s := screen(m)
	mustContain(t, s, "Cancelled the running dispatch", "Esc to stop")
	mustNotContain(t, s, "Thinking")
}

// cancelBackend records cancels; the fake has no running subagents to stop.
type cancelBackend struct {
	*FakeBackend
	cancelled  []string
	dispatched bool
}

func (c *cancelBackend) CancelSubagent(id string) error {
	c.cancelled = append(c.cancelled, id)
	return nil
}

func (c *cancelBackend) CancelDispatch() error {
	c.dispatched = true
	return nil
}

func TestCancelSubagentFlashesConfirmation(t *testing.T) {
	cb := &cancelBackend{FakeBackend: NewFakeBackend()}
	m := modalModel(t, 120, 40, cb)
	runCmd(m, m.runCommand("/cancel a2"))
	if len(cb.cancelled) != 1 || cb.cancelled[0] != "a2" {
		t.Fatalf("/cancel a2 should stop subagent a2, got %v", cb.cancelled)
	}
	if !strings.Contains(m.flash, "Cancelled subagent a2") {
		t.Fatalf("/cancel a2 should flash what it did, flash=%q", m.flash)
	}
	if m.flashKind != "ok" {
		t.Fatalf("a cancel is a success, kind=%q", m.flashKind)
	}
}

func TestCancelAllFlashesConfirmation(t *testing.T) {
	cb := &cancelBackend{FakeBackend: NewFakeBackend()}
	m := modalModel(t, 120, 40, cb)
	runCmd(m, m.runCommand("/cancel all"))
	if !cb.dispatched {
		t.Fatalf("/cancel all should cancel the dispatch")
	}
	if !strings.HasPrefix(m.flash, "Cancelled the running dispatch") {
		t.Fatalf("/cancel all should flash what it did, flash=%q", m.flash)
	}
}

// hashGit is a git repo whose HEAD abbreviates to a known hash.
type hashGit struct{ *gitRec }

func (h hashGit) Shell(ctx context.Context, command string) (string, int, error) {
	if strings.HasPrefix(command, "git rev-parse --short HEAD") {
		return "13dbdf1\n", 0, nil
	}
	return h.gitRec.FakeBackend.Shell(ctx, command)
}

func TestCommitFlashNamesTheCommit(t *testing.T) {
	rb := &gitRec{FakeBackend: NewFakeBackend(), changed: []string{"math.go"}}
	rb.Delay = 0
	m := NewModel(hashGit{rb}, func() (Backend, error) {
		n := NewFakeBackend()
		n.Delay = 0
		return n, nil
	}, Options{Width: 120, Height: 40, Theme: "scribe"})
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	settle(m)
	m.phase = phaseChat
	send(m, "ctrl+g")
	gitKey(m, "c")
	typeText(m, "QA: touch math.go")
	gitKey(m, "ctrl+s")
	if want := "Committed 13dbdf1: QA: touch math.go"; m.flash != want {
		t.Fatalf("flash = %q, want %q", m.flash, want)
	}
	if m.flashKind != "ok" {
		t.Fatalf("a commit is a success, kind=%q", m.flashKind)
	}
}

func TestHeadShortRejectsNonHash(t *testing.T) {
	if got := headShort(NewFakeBackend()); got != "" {
		t.Fatalf("a shell line that is not a hash must be dropped, got %q", got)
	}
}

// ---- /compact on a short chat is information, not a failure

// shortChatBackend is a session that has nothing old enough to compact.
type shortChatBackend struct{ *FakeBackend }

func (shortChatBackend) Compact(context.Context) error {
	return errors.New("compaction: " + agent.ErrNotEnoughHistory.Error())
}

func TestCompactShortChatIsInformation(t *testing.T) {
	m := modalModel(t, 120, 40, shortChatBackend{NewFakeBackend()})
	runCmd(m, m.runCommand("/compact"))
	if m.flashKind == "err" {
		t.Fatalf("a no-op compaction must not read as an error, flash=%q", m.flash)
	}
	if !strings.Contains(m.flash, "Nothing to compact yet") {
		t.Fatalf("the short chat should say there is nothing to compact yet, flash=%q", m.flash)
	}
}

func TestCompactRealFailureStaysAnError(t *testing.T) {
	fb := NewFakeBackend()
	m := modalModel(t, 120, 40, failingCompactBackend{fb})
	runCmd(m, m.runCommand("/compact"))
	if m.flashKind != "err" || !strings.Contains(m.flash, "Compaction failed") {
		t.Fatalf("a real failure is still an error, kind=%q flash=%q", m.flashKind, m.flash)
	}
}

type failingCompactBackend struct{ *FakeBackend }

func (failingCompactBackend) Compact(context.Context) error { return errors.New("model unreachable") }

// ---- subagents modal: paths relative to the project, cut from the left

func TestSubagentLastIsRelativeToProject(t *testing.T) {
	got := subagentLast("read /work/proj/internal/calc/calc.go", "/work/proj")
	if got != "read internal/calc/calc.go" {
		t.Fatalf("subagentLast = %q", got)
	}
	// outside the project the path stays absolute
	if got := subagentLast("read /tmp/x/y.md", "/work/proj"); got != "read /tmp/x/y.md" {
		t.Fatalf("outside-project path changed: %q", got)
	}
}

func TestSubagentLastKeepsFileNameWhenCut(t *testing.T) {
	long := "read /tmp/claude-1000/-home-admin-projects-lectern/a05bd330-3cdc-49f8-94c9-c07a611fc896/scratchpad/qa13w/repo/internal/scribe/tui/very_long_name.go"
	cut := tailTruncate(subagentLast(long, "/work/proj"), 30)
	if !strings.HasSuffix(cut, "very_long_name.go") || !strings.HasPrefix(cut, "…") {
		t.Fatalf("the cut should keep the file name and mark the front: %q", cut)
	}
}

// ---- memory: an empty scope says what is hidden

func TestMemoryEmptyScopeNamesHiddenMemories(t *testing.T) {
	b := &data7Backend{recBackend: newRecBackend()}
	b.hidden = []MemoryItem{{ID: "h1", Scope: "project", Title: "Old note", Body: "x", Updated: time.Now()}}
	m := modalModel(t, 120, 36, b)
	m.runCommand("/memory")
	if topTitle(m) != "Memory" {
		t.Fatalf("/memory should open, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "No project memories yet", "1 hidden memory", "Tab")
}

func TestMemoryEmptyScopeWithoutHiddenSaysNothingHidden(t *testing.T) {
	m := modalModel(t, 120, 36, newRecBackend())
	m.runCommand("/memory")
	mustNotContain(t, screen(m), "hidden")
}

// ---- wizard: a remote Ollama is named by its host

func TestWizardNamesRemoteOllamaHost(t *testing.T) {
	t.Setenv("OLLAMA_HOST", "http://100.127.85.58:11434")
	if got := localRunningWhere("ollama"); got != "running on 100.127.85.58:11434" {
		t.Fatalf("remote host: %q", got)
	}
	if got := localWhere("ollama"); got != "at 100.127.85.58:11434" {
		t.Fatalf("remote where: %q", got)
	}
}

func TestWizardLocalOllamaStillSaysThisMachine(t *testing.T) {
	for _, v := range []string{"", "http://localhost:11434", "127.0.0.1:11434"} {
		t.Setenv("OLLAMA_HOST", v)
		if got := localRunningWhere("ollama"); got != "running on this machine" {
			t.Fatalf("OLLAMA_HOST=%q: %q", v, got)
		}
	}
}

// ---- context bar before the first usage event

// windowBackend reports the window of the model in use.
type windowBackend struct {
	*FakeBackend
	window, override int
}

func (w windowBackend) ContextWindow() (string, int, int) {
	return "ollama/qwen3.6", w.window, w.override
}
func (w windowBackend) SetContextWindow(int) error { return nil }

func TestContextWindowFromBackendBeforeFirstUsage(t *testing.T) {
	tb := &tab{be: windowBackend{NewFakeBackend(), 128000, 0}}
	if got := contextWindow(tb); got != 128000 {
		t.Fatalf("resumed session window = %d, want the model's 128000", got)
	}
	tb.be = windowBackend{NewFakeBackend(), 128000, 64000}
	if got := contextWindow(tb); got != 64000 {
		t.Fatalf("an override should win, got %d", got)
	}
	tb.ctxWin = 32000
	if got := contextWindow(tb); got != 32000 {
		t.Fatalf("a reported window wins over the backend, got %d", got)
	}
	tb.ctxWin = 0
	tb.be = NewFakeBackend()
	if got := contextWindow(tb); got != defaultContextWindow {
		t.Fatalf("a backend that cannot say falls back to the default, got %d", got)
	}
}

// ---- vim keys in the input

func vimModel(t *testing.T) *Model {
	t.Helper()
	m, _ := newTestModel(t, 120, 40)
	settle(m)
	m.phase = phaseChat
	if err := m.cur().be.SetConfig("vim", true, false); err != nil {
		t.Fatal(err)
	}
	return m
}

func inputText(m *Model) string { return m.cur().in.value() }

func TestVimEscThenXDeletesACharacter(t *testing.T) {
	m := vimModel(t)
	typeText(m, "hello")
	send(m, "esc")
	if !m.cur().in.vim.normal {
		t.Fatalf("esc should enter normal mode")
	}
	send(m, "x")
	if got := inputText(m); got != "hell" {
		t.Fatalf("x in normal mode: text = %q, want %q", got, "hell")
	}
	mustContain(t, screen(m), "-- NORMAL --")
}

func TestVimInsertCommands(t *testing.T) {
	m := vimModel(t)
	typeText(m, "bc")
	send(m, "esc") // normal, cursor on c
	send(m, "0")   // start
	send(m, "i")   // insert before b
	typeText(m, "a")
	if got := inputText(m); got != "abc" {
		t.Fatalf("i: %q", got)
	}
	send(m, "esc")
	send(m, "$")
	send(m, "A") // append at end
	typeText(m, "d")
	if got := inputText(m); got != "abcd" {
		t.Fatalf("A: %q", got)
	}
	mustContain(t, screen(m), "-- INSERT --")
}

func TestVimTypingInNormalModeDoesNotInsert(t *testing.T) {
	m := vimModel(t)
	typeText(m, "ab")
	send(m, "esc")
	typeText(m, "zq!") // none of these are commands; none may be typed
	if got := inputText(m); got != "ab" {
		t.Fatalf("unknown keys in normal mode changed the text: %q", got)
	}
}

func TestVimWordMotionsAndDeleteLine(t *testing.T) {
	m := vimModel(t)
	m.cur().in.setValue("one two\nthree")
	send(m, "esc") // normal; cursor at end of "three"
	send(m, "k")   // up to line 0
	send(m, "0")
	send(m, "w") // start of "two"
	send(m, "x") // deletes "t"
	if got := inputText(m); got != "one wo\nthree" {
		t.Fatalf("w then x: %q", got)
	}
	send(m, "d")
	send(m, "d") // deletes line 0
	if got := inputText(m); got != "three" {
		t.Fatalf("dd: %q", got)
	}
}

func TestVimUndoRestoresTheLastChange(t *testing.T) {
	m := vimModel(t)
	m.cur().in.setValue("abc")
	send(m, "esc")
	send(m, "0")
	send(m, "x") // "bc"
	send(m, "x") // "c"
	send(m, "u")
	if got := inputText(m); got != "bc" {
		t.Fatalf("u after two x: %q, want %q", got, "bc")
	}
	send(m, "u")
	if got := inputText(m); got != "abc" {
		t.Fatalf("second u: %q, want %q", got, "abc")
	}
}

func TestVimEscDuringTurnStillStops(t *testing.T) {
	m := vimModel(t)
	typeText(m, "hi")
	m.cur().busy = true
	send(m, "esc")
	if m.cur().in.vim.normal {
		t.Fatalf("esc during a turn stops the turn; it must not switch the box to normal mode")
	}
}

func TestVimOffLeavesTheInputAlone(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	settle(m)
	m.phase = phaseChat
	typeText(m, "hello")
	send(m, "esc")
	send(m, "x")
	if got := inputText(m); got != "hellox" {
		t.Fatalf("with vim off, esc does nothing and x is typed: %q", got)
	}
}

func TestVimHelpKeyStillWorksInNormalMode(t *testing.T) {
	m := vimModel(t)
	send(m, "esc")
	send(m, "?")
	if m.topModal() == nil {
		t.Fatalf("? on an empty box opens help in normal mode too")
	}
}

// ---- quit: one resume command, with the full session id

func TestSavedBannerPrintsOneFullResumeCommand(t *testing.T) {
	id := "59f26789-aaaa-bbbb-cccc-ddddeeeeffff"
	lines := SavedBannerLines(id, nil)
	n := 0
	for _, l := range lines {
		if strings.Contains(l, "lectern code") {
			n++
			if !strings.Contains(l, "lectern code -s "+id) {
				t.Fatalf("resume command must use the full id, got %q", l)
			}
		}
	}
	if n != 1 {
		t.Fatalf("want exactly one resume command, got %d in %q", n, lines)
	}
}

func TestQuitScreenShowsNoSecondResumeCommand(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	settle(m)
	m.phase = phaseChat
	m.startShutdown()
	if m.sd == nil {
		t.Fatalf("startShutdown should open the quit screen")
	}
	if m.sd.resume != "" {
		t.Fatalf("the quit screen must not carry its own resume command, got %q", m.sd.resume)
	}
	mustNotContain(t, m.View(), "--resume")
}
