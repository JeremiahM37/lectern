package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// key turns a name such as "ctrl+k", "enter" or "y" into a key message.
func key(name string) tea.KeyMsg {
	named := map[string]tea.KeyMsg{
		"enter":     {Type: tea.KeyEnter},
		"esc":       {Type: tea.KeyEsc},
		"tab":       {Type: tea.KeyTab},
		"shift+tab": {Type: tea.KeyShiftTab},
		"backspace": {Type: tea.KeyBackspace},
		"up":        {Type: tea.KeyUp},
		"down":      {Type: tea.KeyDown},
		"left":      {Type: tea.KeyLeft},
		"right":     {Type: tea.KeyRight},
		"pgup":      {Type: tea.KeyPgUp},
		"pgdown":    {Type: tea.KeyPgDown},
		"ctrl+c":    {Type: tea.KeyCtrlC},
		"ctrl+k":    {Type: tea.KeyCtrlK},
		"ctrl+l":    {Type: tea.KeyCtrlL},
		"ctrl+d":    {Type: tea.KeyCtrlD},
		"ctrl+t":    {Type: tea.KeyCtrlT},
		"ctrl+w":    {Type: tea.KeyCtrlW},
		"ctrl+r":    {Type: tea.KeyCtrlR},
		"ctrl+p":    {Type: tea.KeyCtrlP},
		"ctrl+j":    {Type: tea.KeyCtrlJ},
		"ctrl+u":    {Type: tea.KeyCtrlU},
	}
	if k, ok := named[name]; ok {
		return k
	}
	if strings.HasPrefix(name, "alt+") {
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name[4:]), Alt: true}
	}
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(name)}
}

// newTestModel builds a Model around a FakeBackend without animation delay.
func newTestModel(t *testing.T, w, h int) (*Model, *FakeBackend) {
	t.Helper()
	fb := NewFakeBackend()
	fb.Delay = 0
	fb.keys["openai"] = ""
	m := NewModel(fb, func() (Backend, error) {
		n := NewFakeBackend()
		n.Delay = 0
		return n, nil
	}, Options{Width: w, Height: h, Theme: "scribe"})
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m, fb
}

// send feeds a key and returns the command it produced (not run).
func send(m *Model, name string) tea.Cmd {
	_, cmd := m.Update(key(name))
	return cmd
}

// typeText types each rune of s as a key press.
func typeText(m *Model, s string) {
	for _, r := range s {
		send(m, string(r))
	}
}

// settle finishes a landing-to-chat transition without waiting for it.
func settle(m *Model) {
	if m.phase == phaseTransition {
		m.trans = time.Now().Add(-time.Second)
		m.Update(tickMsg(time.Now()))
	}
}

// screen returns the view with ANSI sequences removed.
func screen(m *Model) string { return ansi.Strip(m.View()) }

// runCmd executes a command and feeds the message back into the model.
func runCmd(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			runCmd(m, c)
		}
		return
	}
	if msg != nil {
		m.Update(msg)
	}
}

func TestLandingToChatTransition(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	if !strings.Contains(screen(m), "Knows your code") {
		t.Fatalf("landing page should show the tagline:\n%s", screen(m))
	}
	typeText(m, "hello there")
	send(m, "enter")
	if m.phase != phaseTransition {
		t.Fatalf("submitting should start the transition, phase=%v", m.phase)
	}
	// age the transition past 600ms and tick
	m.trans = time.Now().Add(-700 * time.Millisecond)
	m.Update(tickMsg(time.Now()))
	if m.phase != phaseChat {
		t.Fatalf("transition should finish after 600ms, phase=%v", m.phase)
	}
	if !strings.Contains(screen(m), "hello there") {
		t.Fatalf("submitted prompt should appear in the transcript:\n%s", screen(m))
	}
}

func TestSlashAutocompleteFilters(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	typeText(m, "/mod")
	s := screen(m)
	if !strings.Contains(s, "/model") || !strings.Contains(s, "/models") {
		t.Fatalf("popup should list /model and /models:\n%s", s)
	}
	if strings.Contains(s, "/help") {
		t.Fatalf("popup should filter out /help:\n%s", s)
	}
	send(m, "esc")
	if strings.Contains(screen(m), "/models") && m.cur().in.popupKind() != "" {
		t.Fatalf("esc should dismiss the popup")
	}
}

func TestPaletteOpensAndFilters(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	send(m, "ctrl+k")
	if m.topModal() == nil || m.topModal().Title() != "Command Palette" {
		t.Fatalf("ctrl+k should open the command palette, top=%v", m.topModal())
	}
	typeText(m, "theme")
	s := screen(m)
	if !strings.Contains(s, "/theme") {
		t.Fatalf("palette should show /theme after filtering:\n%s", s)
	}
	if strings.Contains(s, "/undo") {
		t.Fatalf("palette should hide unrelated commands:\n%s", s)
	}
}

func TestModelPickerListsProviders(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	cmd := send(m, "ctrl+l")
	_ = cmd
	mp, ok := m.topModal().(*modelPicker)
	if !ok {
		t.Fatalf("ctrl+l should open the model picker, top=%T", m.topModal())
	}
	// fetch every provider that has a key, as the picker would in the background
	for _, p := range fb.Providers() {
		if p.HasKey || p.Local {
			runCmd(m, mp.fetch(p.ID))
		}
	}
	s := screen(m)
	for _, want := range []string{"Anthropic", "Claude Sonnet 5", "Ollama", "add a key"} {
		if !strings.Contains(s, want) {
			t.Fatalf("model picker should show %q:\n%s", want, s)
		}
	}
}

func TestModeCycleChangesHeader(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	// Spec order: default, architect, socratic, challenge, plan, auto.
	send(m, "ctrl+d")
	if !strings.Contains(screen(m), "[ARCHITECT]") {
		t.Fatalf("header should show [ARCHITECT] after one cycle:\n%s", screen(m))
	}
	send(m, "ctrl+d")
	send(m, "ctrl+d")
	send(m, "ctrl+d")
	if !strings.Contains(screen(m), "[PLAN]") {
		t.Fatalf("header should show [PLAN] after four cycles")
	}
	send(m, "shift+tab")
	send(m, "shift+tab")
	send(m, "shift+tab")
	if strings.Contains(screen(m), "[PLAN]") {
		t.Fatalf("mode should have moved past plan")
	}
}

func TestApprovalAnsweredWithYesSendsAllowOnce(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	reply := make(chan PromptReply, 1)
	m.Update(promptMsg{tab: m.cur().id, p: Prompt{
		Approval: &tools.Approval{Tool: "shell", Kind: "shell", Title: "Run shell command", Detail: "go test ./..."},
		Reply:    reply,
	}})
	settle(m)
	if !strings.Contains(screen(m), "Allow once") {
		t.Fatalf("approval panel should show the options:\n%s", screen(m))
	}
	cmd := send(m, "y")
	runCmd(m, cmd)
	select {
	case r := <-reply:
		if r.Decision != tools.AllowOnce {
			t.Fatalf("y should send AllowOnce, got %v", r.Decision)
		}
	default:
		t.Fatalf("no reply was sent")
	}
}

func TestCtrlCSemantics(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	typeText(m, "draft")
	send(m, "ctrl+c")
	if !m.cur().in.empty() {
		t.Fatalf("ctrl+c should clear a non-empty input")
	}
	send(m, "ctrl+c")
	if !strings.Contains(screen(m), "Press Ctrl+C again") {
		t.Fatalf("first ctrl+c on an empty input should show the quit hint")
	}
	send(m, "ctrl+c")
	if m.phase != phaseShutdown {
		t.Fatalf("second ctrl+c within 2s should quit, phase=%v", m.phase)
	}
}

func TestPasteCollapsesLongText(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a\nb\nc\nd\ne\nf"), Paste: true})
	if !strings.Contains(screen(m), "<pasted (a +6 lines)>") {
		t.Fatalf("a six-line paste should collapse to a placeholder:\n%s", screen(m))
	}
	// the full text is sent on submit
	if got := m.cur().in.expand(m.cur().in.value()); !strings.Contains(got, "a\nb\nc\nd\ne\nf") {
		t.Fatalf("expand should restore the pasted text, got %q", got)
	}
}

func TestSteeringQueueLine(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	// a turn is running: the backend accepts steering and the tab is busy
	fb.mu.Lock()
	fb.running = true
	fb.mu.Unlock()
	m.cur().busy = true
	typeText(m, "change the plan")
	send(m, "enter")
	settle(m)
	if !strings.Contains(screen(m), "Steering: change the plan") {
		t.Fatalf("steering line should show the queued message:\n%s", screen(m))
	}
	typeText(m, "and tests")
	send(m, "enter")
	if !strings.Contains(screen(m), "(+1 queued)") {
		t.Fatalf("second steer should show the queue count:\n%s", screen(m))
	}
}

func TestEveryLineHasTerminalWidth(t *testing.T) {
	for _, w := range []int{60, 100, 160} {
		m, _ := newTestModel(t, w, 30)
		check := func(label string) {
			t.Helper()
			lines := strings.Split(m.View(), "\n")
			if len(lines) != 30 {
				t.Fatalf("%s at %d cols: want 30 lines, got %d", label, w, len(lines))
			}
			for i, l := range lines {
				if got := ansi.StringWidth(l); got != w {
					t.Fatalf("%s at %d cols: line %d is %d wide: %q", label, w, i, got, ansi.Strip(l))
				}
			}
		}
		check("landing")
		typeText(m, "hi")
		send(m, "enter")
		m.trans = time.Now().Add(-time.Second)
		m.Update(tickMsg(time.Now()))
		check("chat")
		send(m, "ctrl+k")
		check("palette")
		send(m, "esc")
		m.Update(key2Prompt(m))
		check("approval")
	}
}

// key2Prompt returns a no-op message used to force a re-render between checks.
func key2Prompt(m *Model) tea.Msg { return tickMsg(time.Now()) }

func TestNoPanicOnTinyTerminal(t *testing.T) {
	m, _ := newTestModel(t, 20, 10)
	for _, k := range []string{"ctrl+k", "esc", "ctrl+l", "esc", "ctrl+p", "esc", "ctrl+t", "esc", "?", "ctrl+r", "esc"} {
		send(m, k)
		_ = m.View()
	}
	typeText(m, "a long message to wrap around a tiny terminal")
	send(m, "enter")
	_ = m.View()
}

func TestHelpOpensOnQuestionMarkInEmptyInput(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	send(m, "?")
	if m.topModal() == nil || m.topModal().Title() != "Scribe help" {
		t.Fatalf("? on an empty input should open help")
	}
}

func TestUnknownCommandIsReported(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	typeText(m, "/nope")
	send(m, "enter")
	settle(m)
	// the newest system row types itself out; advance the chat clock past it
	m.Update(tickMsg(time.Now().Add(2 * time.Second)))
	if !strings.Contains(screen(m), "Unknown command: /nope") {
		t.Fatalf("unknown command should be reported:\n%s", screen(m))
	}
}

func TestTabsOpenAndClose(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	send(m, "ctrl+t")
	typeText(m, "review")
	send(m, "enter")
	if len(m.tabs) != 2 {
		t.Fatalf("ctrl+t should open a second tab, have %d", len(m.tabs))
	}
	if !strings.Contains(screen(m), "review") {
		t.Fatalf("tab bar should show the new tab name:\n%s", screen(m))
	}
	send(m, "alt+1")
	if m.active != 0 {
		t.Fatalf("alt+1 should switch to the first tab")
	}
	send(m, "ctrl+w")
	if len(m.tabs) != 1 {
		t.Fatalf("ctrl+w should close the idle tab")
	}
}

func TestQuitSequenceShowsResume(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	cmd := m.beginShutdown()
	runCmd(m, cmd)
	if m.phase != phaseShutdown {
		t.Fatalf("shutdown should switch screens")
	}
	if !strings.Contains(m.resumeLine, "lectern code -s ") || strings.Contains(m.resumeLine, "--resume") {
		t.Fatalf("resume hint should use the full-id form, got %q", m.resumeLine)
	}
}

func TestFakeBackendTurnStreams(t *testing.T) {
	fb := NewFakeBackend()
	fb.Delay = 0
	m := NewModel(fb, nil, Options{Width: 100, Height: 30})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	typeText(m, "fix the trailing comma bug")
	send(m, "enter")
	settle(m)
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("turn did not finish")
		case p := <-fb.Prompts():
			p.Reply <- PromptReply{Decision: tools.AllowOnce}
		case ev := <-fb.Events():
			m.Update(eventMsg{tab: m.cur().id, ev: ev})
			if ev.Kind == "turn_done" {
				if m.cur().busy {
					t.Fatalf("tab should not be busy after turn_done")
				}
				if !strings.Contains(screen(m), "Done.") {
					t.Fatalf("final answer should be on screen")
				}
				return
			}
		}
	}
}

func TestContextBarThresholds(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	tb := m.cur()
	tb.ctxUsed, tb.ctxWin, tb.ctxShown = 84000, 200000, 42
	if !strings.Contains(screen(m), "42% 84k/200k") {
		t.Fatalf("context bar should show percent and tokens:\n%s", screen(m))
	}
	_ = llm.Usage{}
}

// The header keeps whole words: at every width each piece is shown in full or
// not at all, and the row is exactly the terminal's width.
func TestHeaderFitsWithoutCuttingWords(t *testing.T) {
	for _, w := range []int{60, 72, 84, 100, 120} {
		m, _ := newTestModel(t, w, 30)
		m.frame = 200 // the brand tag types itself out over the first frames
		tb := m.cur()
		tb.model = "anthropic/claude-sonnet-5-extended-long"
		tb.git.Branch = "feature/parser-rewrite"
		tb.cost = 0.021
		tb.ctxWin, tb.ctxUsed, tb.ctxShown = 200000, 88000, 44
		line := strings.Split(screen(m), "\n")[0]
		if n := len([]rune(line)); n > w {
			t.Errorf("width %d: header is %d cells", w, n)
		}
		if strings.Contains(line, "claude-sonnet-5-extend ") || strings.Contains(line, "feature/parser-rew ") {
			t.Errorf("width %d: a name was cut mid-word: %q", w, line)
		}
		if w >= 100 && !strings.Contains(line, "in Lectern") {
			t.Errorf("width %d: brand tag should show: %q", w, line)
		}
		if w < 100 && strings.Contains(line, "in Lectern") {
			t.Errorf("width %d: brand tag should be dropped first: %q", w, line)
		}
	}
}

// The checkpoint text legend was replaced by the rail, so browsing takes no
// extra row above the transcript.
func TestBrowsingCheckpointsTakesNoTabBarRow(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	tb := m.cur()
	tb.cps = []CheckpointInfo{{Turn: 1}, {Turn: 2}}
	tb.browse = -1
	if got := m.tabBarRows(); got != 0 {
		t.Errorf("two checkpoints, not browsing: %d rows, want 0", got)
	}
	tb.browse = 0
	if got := m.tabBarRows(); got != 0 {
		t.Errorf("browsing two checkpoints: %d rows, want 0 (the rail shows it)", got)
	}
}

func TestDangerApprovalDefaultsToDeny(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	reply := make(chan PromptReply, 1)
	m.Update(promptMsg{tab: m.cur().id, p: Prompt{
		Approval: &tools.Approval{Tool: "shell", Kind: "destructive", Title: "Delete files", Detail: "rm -rf build", Danger: true},
		Reply:    reply,
	}})
	settle(m)
	if got := m.cur().pui.sel; got != 2 {
		t.Fatalf("danger approval selects %d, want 2 (Deny)", got)
	}
	if !strings.Contains(screen(m), "Refuses this call") {
		t.Fatalf("selected option should explain itself:\n%s", screen(m))
	}
	runCmd(m, send(m, "enter"))
	select {
	case r := <-reply:
		if r.Decision != tools.Deny {
			t.Fatalf("Enter on a danger prompt should deny, got %v", r.Decision)
		}
	default:
		t.Fatalf("no reply was sent")
	}
}

func TestBusyLineUsesQuillAndStopHint(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.phase = phaseChat
	tb := m.cur()
	tb.busy = true
	tb.since = time.Now()
	out := screen(m)
	if strings.Contains(out, "ᚲ") || strings.Contains(out, "Working") {
		t.Fatalf("busy line still shows the rune spinner or the bare word:\n%s", out)
	}
	if !strings.Contains(out, "■ Esc to stop") {
		t.Fatalf("busy line should carry the stop hint:\n%s", out)
	}
}

func TestFitTipEndsOnAWordBoundary(t *testing.T) {
	for w := 12; w <= 80; w++ { // narrower than a tip's first word, no boundary exists
		for start := 0; start < len(tips); start++ {
			got := fitTip(start, w)
			if ansi.StringWidth(got) > w {
				t.Fatalf("fitTip(%d,%d) is %d cells: %q", start, w, ansi.StringWidth(got), got)
			}
			if !strings.HasSuffix(got, "…") {
				continue
			}
			stem := strings.TrimSuffix(got, "…")
			ok := false
			for _, tip := range tips {
				if strings.HasPrefix(tip, stem) && (len(tip) == len(stem) || strings.ContainsRune(" -_/.+,;:()", rune(tip[len(stem)]))) {
					ok = true
					break
				}
			}
			if !ok {
				t.Fatalf("fitTip(%d,%d) cut mid-word: %q", start, w, got)
			}
		}
	}
}
