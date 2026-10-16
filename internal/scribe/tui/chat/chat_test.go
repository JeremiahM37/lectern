package chat

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// clock is a settable time source for deterministic renders.
type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

var t0 = time.Date(2026, 10, 16, 15, 4, 0, 0, time.UTC)

func newView(t *testing.T, o Options) (*View, *clock) {
	t.Helper()
	c := &clock{t: t0}
	o.Now = c.now
	if o.Theme.Name == "" {
		o.Theme = theme.Default()
	}
	v := New(o)
	v.SetSize(100, 60)
	return v, c
}

func plain(v *View) string { return ansi.Strip(v.View()) }

func at(c *clock) time.Time { return c.t }

func text(v *View, s string, c *clock) {
	v.Apply(agent.Event{Kind: agent.EvText, Text: s, At: at(c)})
}

func tool(v *View, id, name string, args map[string]any, c *clock) {
	raw, _ := json.Marshal(args)
	v.Apply(agent.Event{Kind: agent.EvToolStart, CallID: id, Tool: name, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvToolRun, CallID: id, Tool: name, Args: raw, At: at(c)})
}

func result(v *View, id, name string, r tools.Result, d time.Duration, c *clock) {
	v.Apply(agent.Event{Kind: agent.EvToolResult, CallID: id, Tool: name, Result: &r, Duration: d, At: at(c)})
}

func finish(v *View, c *clock, d time.Duration) {
	v.Apply(agent.Event{Kind: agent.EvTurnDone, At: at(c), Stats: &agent.TurnStats{Duration: d}})
}

func checkShape(t *testing.T, v *View) {
	t.Helper()
	out := v.View()
	lines := strings.Split(out, "\n")
	if len(lines) != v.h {
		t.Fatalf("View has %d lines, want %d", len(lines), v.h)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != v.w {
			t.Fatalf("line %d width %d, want %d: %q", i, w, v.w, ansi.Strip(l))
		}
	}
}

func TestTextStreamHeaderAndFooter(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddUser("hello there", 0, c.now(), false)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "hello there", At: at(c)})
	text(v, "Hi! ", c)
	text(v, "I can help with **code**.", c)
	// With the clock frozen the drip has not revealed anything yet.
	if strings.Contains(plain(v), "code") {
		t.Fatalf("drip revealed text without time passing")
	}
	c.advance(10 * time.Second)
	v.Tick(c.now())
	finish(v, c, 65*time.Second)
	out := plain(v)
	for _, want := range []string{"You · 3:04 PM", "✒ Scribe · 3:04 PM", "Hi!", "code", "✓ Completed in 1m 05s"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if v.Busy() {
		t.Errorf("Busy after turn_done")
	}
	checkShape(t, v)
}

func TestToolGroupingAndRail(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	for i, p := range []string{"/w/a.go", "/w/b.go", "/w/c.go"} {
		id := fmt.Sprintf("r%d", i)
		tool(v, id, "read", map[string]any{"path": p}, c)
		result(v, id, "read", tools.Result{Output: "x", Summary: "3 lines"}, time.Second, c)
	}
	tool(v, "g1", "grep", map[string]any{"pattern": "foo"}, c)
	tool(v, "g2", "grep", map[string]any{"pattern": "bar"}, c)
	result(v, "g1", "grep", tools.Result{Output: "1", Summary: "1 hit in 1 file"}, 0, c)
	result(v, "g2", "grep", tools.Result{Output: "0", Summary: "0 hits"}, 0, c)
	finish(v, c, 3*time.Second)
	out := plain(v)
	if !strings.Contains(out, "3 files") {
		t.Errorf("read batch not grouped:\n%s", out)
	}
	if !strings.Contains(out, "2 searches") {
		t.Errorf("search batch not grouped:\n%s", out)
	}
	if !strings.Contains(out, "a.go") || !strings.Contains(out, "c.go") {
		t.Errorf("file tree missing entries:\n%s", out)
	}
	checkShape(t, v)
}

func TestRailCollapsesAboveFiveRows(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("s%d", i)
		tool(v, id, "shell", map[string]any{"command": fmt.Sprintf("echo %d", i)}, c)
		result(v, id, "shell", tools.Result{Output: "ok", Summary: "exit 0"}, 0, c)
	}
	// Still streaming: rail is live. Finish later.
	out := plain(v)
	if !strings.Contains(out, "✓ +3 completed") {
		t.Fatalf("expected +3 collapse row:\n%s", out)
	}
	if n := strings.Count(out, " Ran "); n != 4 {
		t.Errorf("visible shell rows = %d, want 4\n%s", n, out)
	}
	checkShape(t, v)
}

func TestErrorAndRetryRows(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvRetry, Category: llm.CatRateLimited, Attempt: 2, MaxAttempts: 3,
		Delay: 4 * time.Second, Text: "429", At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvError, Category: llm.CatOverloaded, Text: "overloaded", At: at(c)})
	out := plain(v)
	if !strings.Contains(out, "↻ Retry 2/3 — Rate Limited — waiting ~4s") {
		t.Errorf("retry row missing:\n%s", out)
	}
	if !strings.Contains(out, "✗ Overloaded — The provider is overloaded") {
		t.Errorf("error row missing:\n%s", out)
	}
	checkShape(t, v)
}

func TestDiffShownOnExpand(t *testing.T) {
	v, c := newView(t, Options{Workdir: "/w"})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "e1", "edit_file", map[string]any{"path": "/w/main.go"}, c)
	diff := "--- a/main.go\n+++ b/main.go\n@@ -1,3 +1,3 @@\n package main\n-func old() {}\n+func new() {}\n func x() {}\n"
	result(v, "e1", "edit_file", tools.Result{Output: "edited", Summary: "+1 -1", Diff: diff}, 0, c)
	finish(v, c, 2*time.Second)
	if strings.Contains(plain(v), "Edit ─ main.go") {
		t.Fatalf("diff shown before expand")
	}
	v.ToggleExpand()
	out := plain(v)
	for _, want := range []string{"Edit ─ main.go +1 -1", "func new()", "main.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("expanded view missing %q:\n%s", want, out)
		}
	}
	v.ToggleExpand()
	if strings.Contains(plain(v), "Edit ─ main.go") {
		t.Errorf("diff still shown after collapse")
	}
	checkShape(t, v)
}

func TestSubagentTree(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "d1", "dispatch", map[string]any{"tasks": []any{map[string]any{"task": "find where sessions are saved", "role": "explore"}}}, c)
	v.Apply(agent.Event{Kind: agent.EvSubagent, CallID: "d1", At: at(c), Agent: &agent.AgentInfo{
		ID: "a1", Task: "find where sessions are saved", Role: "explore", Status: "running",
		Model: "anthropic/claude-haiku-4-5-20251001", Step: 4,
		Tokens: llm.Usage{InputTokens: 12000, OutputTokens: 300}, Last: "read store.go",
	}})
	out := plain(v)
	for _, want := range []string{"a1 explore · haiku-4-5 · step 4 · 12k", "find where sessions are saved", "read store.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("subagent tree missing %q:\n%s", want, out)
		}
	}
	// Subagent events update the row without creating their own rows.
	raw, _ := json.Marshal(map[string]any{"path": "x.go"})
	v.Apply(agent.Event{Kind: agent.EvToolRun, AgentID: "a1", CallID: "sub-call", Tool: "grep", Args: raw, At: at(c)})
	if !strings.Contains(plain(v), "grep pattern") && !strings.Contains(plain(v), "grep x.go") {
		t.Errorf("subagent last action not shown")
	}
	if strings.Contains(plain(v), "sub-call") {
		t.Errorf("subagent call leaked into transcript")
	}
	checkShape(t, v)
}

func TestPlanRendersStepGlyphs(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvPlan, At: at(c), Plan: &agent.Plan{Title: "Ship sessions", Steps: []agent.PlanStep{
		{ID: "1", Text: "Read store", Status: "done"},
		{ID: "2", Text: "Write fix", Status: "active"},
		{ID: "3", Text: "Test", Status: "pending"},
	}}})
	out := plain(v)
	for _, want := range []string{"Ship sessions", "1/3", "✓ Read store", "Write fix", "○ Test"} {
		if !strings.Contains(out, want) {
			t.Errorf("plan missing %q:\n%s", want, out)
		}
	}
	checkShape(t, v)
}

func TestWidthsAreExact(t *testing.T) {
	for _, w := range []int{40, 80, 140} {
		v, c := newView(t, Options{ChatStyle: "bubble"})
		v.SetSize(w, 30)
		v.AddUser(strings.Repeat("a long user message with words ", 12), 2, at(c), false)
		v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
		tool(v, "e1", "edit_file", map[string]any{"path": "/some/very/long/path/to/a/file/that/goes/on.go"}, c)
		result(v, "e1", "edit_file", tools.Result{Output: "ok", Summary: "+1 -1", Diff: "--- a/f\n+++ b/f\n@@ -1 +1 @@\n-x\n+y\n"}, 0, c)
		v.ToggleExpand()
		text(v, "Done with the **change** — see `file.go`.", c)
		c.advance(time.Minute)
		v.Tick(c.now())
		finish(v, c, 61*time.Second)
		checkShape(t, v)
	}
}

func TestStickyAndDetachedScroll(t *testing.T) {
	v, c := newView(t, Options{})
	v.SetSize(60, 10)
	for i := 0; i < 20; i++ {
		v.AddUser(fmt.Sprintf("message %02d", i), 0, at(c), false)
		v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
		v.Apply(agent.Event{Kind: agent.EvTurnDone, At: at(c), Stats: &agent.TurnStats{Duration: time.Second}})
	}
	if out := plain(v); !strings.Contains(out, "message 19") {
		t.Fatalf("sticky view should show newest message:\n%s", out)
	}
	v.ScrollUp(15)
	if v.AtBottom() {
		t.Fatalf("AtBottom after scrolling up")
	}
	before := plain(v)
	v.AddUser("late message", 0, at(c), true) // steering: does not re-attach
	v.Apply(agent.Event{Kind: agent.EvText, Text: "reply", At: at(c)})
	out := plain(v)
	if strings.Contains(out, "late message") && !strings.Contains(out, "End to jump") {
		t.Errorf("detached view should show the indicator:\n%s", out)
	}
	if !strings.Contains(out, "End to jump") {
		t.Errorf("missing new-lines indicator:\n%s", out)
	}
	if before == out {
		t.Errorf("view did not change after new content")
	}
	v.Bottom()
	if !v.AtBottom() {
		t.Errorf("Bottom did not re-attach")
	}
	checkShape(t, v)
}

func TestMouseClickTogglesReasoning(t *testing.T) {
	v, c := newView(t, Options{ShowReasoning: true})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvReasoning, Text: "first thought\nsecond", At: at(c)})
	text(v, "answer", c)
	finish(v, c, time.Second)
	lines := strings.Split(plain(v), "\n")
	row := -1
	for i, l := range lines {
		if strings.Contains(l, "Thought") {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatalf("no thought row:\n%s", plain(v))
	}
	if strings.Contains(plain(v), "first thought") {
		t.Errorf("collapsed thought shows its text:\n%s", plain(v))
	}
	v.HandleMouse(tea.MouseMsg{X: 1, Y: row, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if !strings.Contains(plain(v), "▼") || !strings.Contains(plain(v), "first thought") {
		t.Errorf("click did not expand the thought row:\n%s", plain(v))
	}
	v.HandleMouse(tea.MouseMsg{X: 1, Y: row, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	if strings.Contains(plain(v), "first thought") {
		t.Errorf("second click did not collapse the thought row")
	}
}

// thinkStep is one model step: a reasoning stream that runs two seconds.
func thinkStep(v *View, c *clock, step int, reasoning string) {
	v.Apply(agent.Event{Kind: agent.EvStepStart, Step: step, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvReasoning, Text: reasoning, At: at(c)})
	c.advance(2 * time.Second)
	v.Apply(agent.Event{Kind: agent.EvReasoning, Text: " (more)", At: at(c)})
}

// runSteps plays n steps: reasoning, a line of narration, then one read.
func runSteps(v *View, c *clock, n int) {
	for i := 1; i <= n; i++ {
		thinkStep(v, c, i, fmt.Sprintf("reasoning for step %d", i))
		text(v, fmt.Sprintf("narration %d", i), c)
		id := fmt.Sprintf("c%d", i)
		tool(v, id, "read", map[string]any{"path": fmt.Sprintf("f%d.go", i)}, c)
		result(v, id, "read", tools.Result{Output: "x", Summary: "1 line"}, time.Second, c)
		c.advance(time.Second)
	}
}

func rowOf(out, sub string) int {
	for i, l := range strings.Split(out, "\n") {
		if strings.Contains(l, sub) {
			return i
		}
	}
	return -1
}

func TestMultiStepReasoningCollapsesToOneRow(t *testing.T) {
	v, c := newView(t, Options{ShowReasoning: true})
	v.AddUser("go", 0, at(c), false)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	runSteps(v, c, 5)
	text(v, "final answer.", c)
	finish(v, c, 20*time.Second)
	out := plain(v)
	if n := strings.Count(out, "Thought"); n != 1 {
		t.Fatalf("want one thought row, got %d:\n%s", n, out)
	}
	if !strings.Contains(out, "Thought for 10s · 5 steps") {
		t.Errorf("summary wrong (5 steps x 2s):\n%s", out)
	}
	if strings.Contains(out, "reasoning for step") {
		t.Errorf("collapsed summary shows step text:\n%s", out)
	}
	if thought, rail := rowOf(out, "Thought"), rowOf(out, "5 tool calls"); thought < 0 || rail < 0 || thought > rail {
		t.Errorf("thought row must sit above the rail (thought %d, rail %d):\n%s", thought, rail, out)
	}
}

func TestDuplicateOfAnswerReasoningIsHidden(t *testing.T) {
	v, c := newView(t, Options{ShowReasoning: true})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvStepStart, Step: 1, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvReasoning, Text: "The answer is 42.", At: at(c)})
	text(v, "The answer is 42. Here is why.", c)
	finish(v, c, time.Second)
	out := plain(v)
	if strings.Contains(out, "Thought") {
		t.Errorf("reasoning that repeats the answer was rendered:\n%s", out)
	}
	if n := strings.Count(out, "The answer is 42."); n != 1 {
		t.Errorf("answer text appears %d times, want 1", n)
	}
	v.ToggleExpand()
	if strings.Contains(plain(v), "step 1") {
		t.Errorf("expanded view shows the hidden reasoning")
	}
}

func TestLiveRowShowsLatestReasoningTail(t *testing.T) {
	v, c := newView(t, Options{ShowReasoning: true})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	thinkStep(v, c, 1, "first thought")
	text(v, "narration 1", c)
	tool(v, "a", "read", map[string]any{"path": "a.go"}, c)
	c.advance(3 * time.Second)
	v.Apply(agent.Event{Kind: agent.EvStepStart, Step: 2, At: at(c)})
	v.Apply(agent.Event{Kind: agent.EvReasoning, Text: "alpha\nomega tail", At: at(c)})
	c.advance(time.Second)
	v.Tick(c.now())
	out := plain(v)
	if !strings.Contains(out, "Thinking · omega tail") {
		t.Errorf("live row does not show the latest reasoning tail:\n%s", out)
	}
	if strings.Contains(out, "first thought") || strings.Contains(out, "alpha") {
		t.Errorf("older reasoning is rendered while running:\n%s", out)
	}
	if !strings.Contains(out, "step 2 ·") {
		t.Errorf("rail header lacks the step count:\n%s", out)
	}
	if !strings.Contains(out, "narration 1") {
		t.Errorf("last intermediate line not shown above the rail:\n%s", out)
	}
}

func TestExpandedThoughtShowsStepDividers(t *testing.T) {
	v, c := newView(t, Options{ShowReasoning: true})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	runSteps(v, c, 3)
	text(v, "final answer.", c)
	finish(v, c, 10*time.Second)
	v.ToggleExpand()
	out := plain(v)
	for i := 1; i <= 3; i++ {
		div := fmt.Sprintf("─ step %d ", i)
		if !strings.Contains(out, div) {
			t.Errorf("missing divider %q:\n%s", div, out)
		}
		if !strings.Contains(out, fmt.Sprintf("reasoning for step %d", i)) {
			t.Errorf("missing reasoning for step %d", i)
		}
	}
	// The rail keeps five rows, so the narration in the window shows up.
	if !strings.Contains(out, "narration 2") {
		t.Errorf("expanded rail does not carry the intermediate text:\n%s", out)
	}
	if n := strings.Count(out, "final answer."); n != 1 {
		t.Errorf("final answer appears %d times, want 1", n)
	}
}

func TestIntermediateTextLeavesTheTranscriptWhenDone(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	runSteps(v, c, 2)
	if out := plain(v); !strings.Contains(out, "narration 2") || strings.Contains(out, "narration 1") {
		t.Errorf("running: want only the last intermediate line:\n%s", out)
	}
	text(v, "final answer.", c)
	finish(v, c, 5*time.Second)
	out := plain(v)
	if strings.Contains(out, "narration") {
		t.Errorf("done: intermediate text still in the transcript:\n%s", out)
	}
	if !strings.Contains(out, "final answer.") {
		t.Errorf("done: final answer missing:\n%s", out)
	}
}

func TestMultiStepWidthsAreExact(t *testing.T) {
	for _, w := range []int{40, 80, 140} {
		v, c := newView(t, Options{ShowReasoning: true})
		v.SetSize(w, 40)
		v.AddUser("go", 0, at(c), false)
		v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
		runSteps(v, c, 4)
		checkShape(t, v) // live
		text(v, "final answer, long enough to wrap at the narrow widths used here.", c)
		finish(v, c, 9*time.Second)
		checkShape(t, v)
		v.ToggleExpand()
		checkShape(t, v)
	}
}

func TestExportRestoreRendersIdentically(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddUser("fix it", 1, at(c), false)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "e1", "edit_file", map[string]any{"path": "a.go"}, c)
	result(v, "e1", "edit_file", tools.Result{Output: "ok", Summary: "+1 -1", Diff: "--- a/a.go\n+++ b/a.go\n@@ -1 +1 @@\n-x\n+y\n", Files: []string{"a.go"}}, time.Second, c)
	text(v, "Fixed.", c)
	v.AddSystem("info", "note")
	finish(v, c, 5*time.Second)
	c.advance(time.Second)
	v.Tick(c.now())
	before := v.View()

	raw, err := json.Marshal(v.Export())
	if err != nil {
		t.Fatal(err)
	}
	var recs []Record
	if err := json.Unmarshal(raw, &recs); err != nil {
		t.Fatal(err)
	}
	w := New(Options{Theme: theme.Default(), Now: c.now})
	w.SetSize(100, 60)
	w.Restore(recs)
	if got := w.View(); got != before {
		t.Errorf("restored render differs:\n--- before\n%s\n--- after\n%s", ansi.Strip(before), ansi.Strip(got))
	}
	cr, mo := w.ChangedFiles()
	if len(cr) != 0 || len(mo) != 1 || mo[0] != "a.go" {
		t.Errorf("ChangedFiles after restore = %v %v", cr, mo)
	}
}

func TestChangedFilesCreatedAndModified(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "w1", "write_file", map[string]any{"path": "new.go"}, c)
	result(v, "w1", "write_file", tools.Result{Output: "wrote", Diff: "--- /dev/null\n+++ b/new.go\n@@ -0,0 +1 @@\n+x\n", Files: []string{"new.go"}}, 0, c)
	tool(v, "e1", "edit_file", map[string]any{"path": "old.go"}, c)
	result(v, "e1", "edit_file", tools.Result{Output: "ok", Diff: "--- a/old.go\n+++ b/old.go\n@@ -1 +1 @@\n-a\n+b\n", Files: []string{"old.go"}}, 0, c)
	finish(v, c, time.Second)
	cr, mo := v.ChangedFiles()
	if fmt.Sprint(cr) != "[new.go]" || fmt.Sprint(mo) != "[old.go]" {
		t.Errorf("created=%v modified=%v", cr, mo)
	}
}

func TestJumpToTurn(t *testing.T) {
	v, c := newView(t, Options{})
	v.SetSize(60, 8)
	for i := 0; i < 6; i++ {
		v.AddUser(fmt.Sprintf("turn %d", i+1), 0, at(c), false)
		v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
		text(v, "reply", c)
		finish(v, c, time.Second)
	}
	v.JumpToTurn(2)
	out := strings.Split(plain(v), "\n")
	if !strings.Contains(strings.Join(out[:3], "\n"), "turn 2") || !strings.Contains(strings.Join(out[:2], "\n"), "You") {
		t.Errorf("turn 2 not near top:\n%s", strings.Join(out, "\n"))
	}
	checkShape(t, v)
}

func TestClearAndEmptyState(t *testing.T) {
	v, c := newView(t, Options{})
	if out := plain(v); !strings.Contains(out, "No messages yet. Type below to start.") {
		t.Errorf("empty state missing")
	}
	v.AddUser("x", 0, at(c), false)
	v.Clear()
	if len(v.Export()) != 0 {
		t.Errorf("Clear left records")
	}
	checkShape(t, v)
}

func TestFiveHundredMessagesRenderFast(t *testing.T) {
	v, c := newView(t, Options{})
	v.SetSize(120, 50)
	for i := 0; i < 500; i++ {
		v.AddUser(fmt.Sprintf("message %d with some words to wrap around the line width", i), 0, at(c), false)
		v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
		tool(v, fmt.Sprintf("r%d", i), "read", map[string]any{"path": fmt.Sprintf("f%d.go", i)}, c)
		result(v, fmt.Sprintf("r%d", i), "read", tools.Result{Output: "x", Summary: "1 line"}, 0, c)
		text(v, "Here is **the** answer with `code` in it.\n\n- one\n- two", c)
		finish(v, c, time.Second)
	}
	start := time.Now()
	v.View()
	first := time.Since(start)
	t.Logf("cold render of 500 messages: %v", first)
	start = time.Now()
	v.ScrollUp(1)
	v.View()
	second := time.Since(start)
	budget := 200 * time.Millisecond * raceBudgetScale
	if first > budget || second > budget {
		t.Fatalf("500-message render too slow: first %v, second %v", first, second)
	}
	checkShape(t, v)
}

func TestDripSnapsToWords(t *testing.T) {
	s := &Segment{Kind: "text", Text: "alpha beta gamma delta", Open: true}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	advanceSeg(s, base)
	for i := 1; i <= 200; i++ {
		advanceSeg(s, base.Add(time.Duration(i)*dripTick))
		vis := visibleText(s)
		if strings.HasSuffix(vis, "alp") || strings.HasSuffix(vis, "bet") {
			t.Fatalf("drip split a word: %q", vis)
		}
	}
	if visibleText(s) != s.Text {
		t.Fatalf("drip did not finish: %q", visibleText(s))
	}
}

func TestClickOnToolRowExpandsIt(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "s1", "shell", map[string]any{"command": "echo hi"}, c)
	result(v, "s1", "shell", tools.Result{Output: "hi-from-shell", Summary: "exit 0"}, time.Second, c)
	finish(v, c, time.Second)
	if strings.Contains(plain(v), "hi-from-shell") {
		t.Fatalf("output shown before click")
	}
	lines := strings.Split(plain(v), "\n")
	row := -1
	for i, l := range lines {
		if strings.Contains(l, "echo hi") {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatalf("shell row missing")
	}
	if !v.HandleMouse(tea.MouseMsg{X: 4, Y: row, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress}) {
		t.Fatalf("click on tool row not handled")
	}
	if !strings.Contains(plain(v), "hi-from-shell") {
		t.Errorf("click did not expand shell output:\n%s", plain(v))
	}
	checkShape(t, v)
}

func TestSteeringSplitsTurn(t *testing.T) {
	v, c := newView(t, Options{})
	v.AddUser("start", 0, at(c), false)
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	text(v, "first part", c)
	tool(v, "x1", "shell", map[string]any{"command": "sleep 1"}, c)
	v.AddUser("actually, use tabs", 0, at(c), true)
	result(v, "x1", "shell", tools.Result{Output: "done", Summary: "exit 0"}, time.Second, c)
	text(v, "second part", c)
	finish(v, c, 2*time.Second)
	out := plain(v)
	iSteer := strings.Index(out, "actually, use tabs")
	iSecond := strings.Index(out, "second part")
	if iSteer < 0 || iSecond < 0 || iSecond < iSteer {
		t.Fatalf("steering message should sit between the two parts:\n%s", out)
	}
	if !strings.Contains(out, "steering") {
		t.Errorf("steering suffix missing")
	}
	if v.Busy() {
		t.Errorf("still busy after turn_done")
	}
	checkShape(t, v)
}

func TestFinishedRailSummarisesTheTurn(t *testing.T) {
	v, c := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, At: at(c)})
	tool(v, "r1", "read", map[string]any{"path": "/w/a.go"}, c)
	result(v, "r1", "read", tools.Result{Output: "x", Summary: "3 lines"}, time.Second, c)
	tool(v, "e1", "edit_file", map[string]any{"path": "/w/a.go"}, c)
	result(v, "e1", "edit_file", tools.Result{Output: "ok", Summary: "+1 -1", Diff: "--- a\n+++ a\n@@ -1 +1 @@\n-x\n+y\n"}, time.Second, c)
	tool(v, "s1", "shell", map[string]any{"command": "go test"}, c)
	result(v, "s1", "shell", tools.Result{Output: "ok", Summary: "exit 0"}, time.Second, c)
	finish(v, c, 15*time.Second)
	out := plain(v)
	if !strings.Contains(out, "✓ 3 tool calls · 1 file edited · 15s") {
		t.Fatalf("summary header wrong:\n%s", out)
	}
	if strings.Contains(out, "Helpers reported back") {
		t.Fatalf("a turn without dispatch must not say helpers reported back:\n%s", out)
	}
}

func TestPhaseFollowsTheRunningTool(t *testing.T) {
	run := func(tool string, running bool) *Call {
		st := "done"
		if running {
			st = "running"
		}
		return &Call{Tool: tool, Status: st}
	}
	cases := []struct {
		name  string
		calls []*Call
		want  string
	}{
		{"read while idle", []*Call{run("edit_file", false), run("read", true)}, "explore"},
		{"edit running", []*Call{run("read", false), run("edit_file", true)}, "edit"},
		{"dispatch running", []*Call{run("edit_file", false), run("dispatch", true)}, "dispatch"},
		{"nothing running, last was edit", []*Call{run("read", false), run("edit_file", false)}, "edit"},
	}
	for _, tc := range cases {
		if got := phaseOf(tc.calls); got != tc.want {
			t.Errorf("%s: phase %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestUnmappedToolGetsNoWords(t *testing.T) {
	for _, tool := range []string{"read", "shell", "dispatch", "web_search", "mcp__github__list_issues", "made_up_tool"} {
		d := defFor(tool)
		if strings.ContainsAny(icons.ToolIcon(tool), "[]") || strings.Contains(icons.ToolIcon(tool), "tool") {
			t.Errorf("%s icon %q looks like a word", tool, icons.ToolIcon(tool))
		}
		if d.run == "" {
			t.Errorf("%s has no label", tool)
		}
	}
}
