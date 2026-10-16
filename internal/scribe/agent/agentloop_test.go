package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestFallbackCyclesBackToPrimaryThenGivesUp(t *testing.T) {
	primary := newFake(t)
	primary.setPick(func(int, recordedReq) reply {
		return apiError(503, "", `{"error":{"message":"overloaded"}}`)
	})
	backup := newFake(t)
	backup.setPick(func(int, recordedReq) reply {
		return apiError(503, "", `{"error":{"message":"overloaded"}}`)
	})

	log := &eventLog{}
	opts := baseOptions(primary, tools.NewRegistry(), log, t.TempDir())
	opts.MaxRetries = 1
	opts.RetryBase = 250 * time.Millisecond
	opts.Fallback = []Model{fakeModel(backup.srv.URL, "fake/backup", priced)}
	a := New(opts)

	stats, err := a.Run(context.Background(), "hi", nil)
	if err == nil || !strings.Contains(stats.Error, "exhausted 3 fallback cycles") {
		t.Fatalf("err = %v, stats.Error = %q; want the cycle limit", err, stats.Error)
	}
	// Four passes over the chain (the first and three cycles), each trying
	// the primary and the backup twice (one retry each).
	if primary.count() != 8 || backup.count() != 8 {
		t.Fatalf("requests primary=%d backup=%d, want 8 and 8", primary.count(), backup.count())
	}
	cycles := 0
	for _, e := range log.of(EvFallback) {
		if strings.Contains(e.Text, "All fallbacks exhausted, retrying primary") {
			cycles++
		}
	}
	if cycles != 3 {
		t.Fatalf("%d cycle notices, want 3", cycles)
	}
}

func TestPrimaryRecoveringInASecondCycleEndsTheTurn(t *testing.T) {
	primary := newFake(t)
	primary.setPick(scripted(t,
		apiError(503, "", `{"error":{"message":"overloaded"}}`),
		apiError(503, "", `{"error":{"message":"overloaded"}}`),
		turn(sseTurn{text: []string{"primary is back"}}),
	))
	backup := newFake(t)
	backup.setPick(func(int, recordedReq) reply {
		return apiError(503, "", `{"error":{"message":"overloaded"}}`)
	})
	log := &eventLog{}
	opts := baseOptions(primary, tools.NewRegistry(), log, t.TempDir())
	opts.MaxRetries = 1
	opts.RetryBase = 250 * time.Millisecond
	opts.Fallback = []Model{fakeModel(backup.srv.URL, "fake/backup", priced)}
	a := New(opts)

	stats, err := a.Run(context.Background(), "hi", nil)
	if err != nil || stats.Error != "" {
		t.Fatalf("err = %v, stats.Error = %q", err, stats.Error)
	}
	if got := log.of(EvTurnDone)[0].Text; got != "primary is back" {
		t.Fatalf("final text = %q", got)
	}
}

func TestStallNoticeUsesTheChatWording(t *testing.T) {
	e := &stallError{after: 120 * time.Second}
	if got := e.notice(); got != "Connection stalled — no activity for 2m0s" {
		t.Fatalf("notice = %q", got)
	}
	if got := (&stallError{after: 40 * time.Millisecond}).notice(); got != "Connection stalled — no activity for 40ms" {
		t.Fatalf("sub-second notice = %q", got)
	}
}

func TestBackoffBaseSetsTheJitterWindow(t *testing.T) {
	// The chat's base is 1 s: the first retry waits 1–1.5 s. A subagent's
	// base is 2 s: 2–3 s.
	for i := 0; i < 50; i++ {
		d := backoffFrom(time.Second, 0, &llm.APIError{StatusCode: 529})
		if d < time.Second || d > 1500*time.Millisecond {
			t.Fatalf("chat retry 1 waited %v", d)
		}
		d = backoffFrom(2*time.Second, 1, &llm.APIError{StatusCode: 529})
		if d < 4*time.Second || d > 5*time.Second {
			t.Fatalf("subagent retry 2 waited %v", d)
		}
	}
}

func TestPolicyForCapsTheLastStepAndNarrowsCodeAgents(t *testing.T) {
	explore := &StepBudget{Kind: "explore", NudgeAt: 8}
	code := &StepBudget{Kind: "code", NudgeAt: 12}
	specs := []llm.ToolSpec{{Name: "read"}, {Name: "edit_file"}, {Name: "multi_edit"}, {Name: "report_finding"}}

	if p := policyFor(explore, 12, 11, false); p.textOnly || p.editOnly {
		t.Fatalf("explore step 11 of 12 should still allow tools, got %+v", p)
	}
	if p := policyFor(explore, 12, 12, false); !p.textOnly || p.filterTools(specs) != nil {
		t.Fatalf("explore final step should be text only, got %+v", p)
	}
	if p := policyFor(code, 18, 17, false); !p.editOnly || p.allows("read") || !p.allows("multi_edit") {
		t.Fatalf("code step 17 of 18 should be edit only, got %+v", p)
	}
	if got := policyFor(code, 18, 17, false).filterTools(specs); len(got) != 3 {
		t.Fatalf("edit-only step offered %d tools, want 3", len(got))
	}
	if p := policyFor(nil, 0, 5, true); !p.textOnly {
		t.Fatal("a pending token nudge must make the next step text only")
	}
}

func TestWrapUpHintsStartAtTheNudgeStep(t *testing.T) {
	explore := &StepBudget{Kind: "explore", NudgeAt: 8}
	if h := wrapUpHint(explore, 12, 7); h != "" {
		t.Fatalf("step 7 should have no hint, got %q", h)
	}
	if h := wrapUpHint(explore, 12, 8); !strings.Contains(h, "Wrap up") {
		t.Fatalf("step 8 hint = %q", h)
	}
	if h := wrapUpHint(explore, 12, 10); !strings.Contains(h, "Wrap up") {
		t.Fatalf("step 10 hint = %q", h)
	}
	if h := wrapUpHint(explore, 12, 11); !strings.Contains(h, "Stop searching") {
		t.Fatalf("step 11 hint = %q", h)
	}
	if h := wrapUpHint(explore, 12, 12); !strings.Contains(h, "final step") {
		t.Fatalf("step 12 hint = %q", h)
	}
	if h := wrapUpHint(nil, 12, 12); h != "" {
		t.Fatalf("main agent got hint %q", h)
	}
}

func TestTokenNudgeFiresOnceThenStops(t *testing.T) {
	a := New(Options{Budget: &StepBudget{Kind: "explore", Window: 1000}})
	a.lastIn = 850
	hint, stop := a.tokenCheck()
	if stop || !strings.Contains(hint, "concise text summary") {
		t.Fatalf("at 85%% got hint=%q stop=%v, want the nudge", hint, stop)
	}
	if !a.pendingText {
		t.Fatal("the nudge must make the next step text only")
	}
	a.lastIn = 950
	if _, stop := a.tokenCheck(); !stop {
		t.Fatal("past 90% after the nudge the run must stop")
	}
}

func TestMainAgentHasNoTokenLimit(t *testing.T) {
	a := New(Options{})
	a.lastIn = 10_000_000
	if hint, stop := a.tokenCheck(); hint != "" || stop {
		t.Fatalf("main agent got hint=%q stop=%v", hint, stop)
	}
}

func TestStripRangeAndTargetCheck(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.go"), []byte("package x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := stripRange("real.go:10-20"); got != "real.go" {
		t.Fatalf("stripRange = %q", got)
	}
	if got := stripRange("plain"); got != "plain" {
		t.Fatalf("stripRange(plain) = %q", got)
	}
	tasks := []subTask{{Role: "code", Task: "x", Files: []string{"real.go:1-3", "ghost.go", "README"}}}
	notes := checkTargets(tasks, dir)
	if len(notes) != 1 || !strings.Contains(notes[0], "ghost.go") {
		t.Fatalf("notes = %v", notes)
	}
	if got := tasks[0].Files; len(got) != 2 || got[0] != "real.go:1-3" || got[1] != "README" {
		t.Fatalf("kept files = %v", got)
	}
}

func TestMergeOverLimitFoldsExploreTasksAndKeepsPinnedOnes(t *testing.T) {
	var tasks []subTask
	for i := 0; i < 9; i++ {
		tasks = append(tasks, subTask{Role: "explore", Task: fmt.Sprintf("q%d", i)})
	}
	tasks[4].Role = "code"
	out, notes, err := mergeOverLimit(tasks, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 8 || len(notes) != 1 {
		t.Fatalf("got %d tasks and %d notes, want 8 and 1", len(out), len(notes))
	}
	if !strings.Contains(out[0].Task, "Also: q8") {
		t.Fatalf("the last explore task should be folded into the first: %q", out[0].Task)
	}
	if out[4].Role != "code" {
		t.Fatal("the code task was merged away")
	}
}

func TestMergeOverLimitRefusesWhenTooFewCanMerge(t *testing.T) {
	var tasks []subTask
	for i := 0; i < 9; i++ {
		tasks = append(tasks, subTask{Role: "code", Task: "edit"})
	}
	if _, _, err := mergeOverLimit(tasks, 8); err == nil || !strings.Contains(err.Error(), "at most 8 tasks") {
		t.Fatalf("err = %v", err)
	}
}

func TestMergeOverLimitNeverMergesATaskAnotherDependsOn(t *testing.T) {
	tasks := []subTask{
		{ID: "base", Role: "explore", Task: "base"},
		{Role: "explore", Task: "a"}, {Role: "explore", Task: "b"}, {Role: "explore", Task: "c"},
		{Role: "explore", Task: "d"}, {Role: "explore", Task: "e"}, {Role: "explore", Task: "f"},
		{Role: "explore", Task: "g"}, {Role: "explore", Task: "h"}, {Role: "explore", Task: "i"},
		{Role: "explore", Task: "after", DependsOn: []string{"base"}},
	}
	out, _, err := mergeOverLimit(tasks, 8)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, t := range out {
		if t.ID == "base" {
			found = true
		}
	}
	if !found {
		t.Fatal("the task named by depends_on was merged away")
	}
}

func TestDispatchMergesOverLimitAndNotesIt(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(int, recordedReq) reply { return turn(sseTurn{text: []string{"found it"}}) })
	var tasks []string
	for i := 1; i <= 10; i++ {
		tasks = append(tasks, fmt.Sprintf(`{"role":"investigate","task":"LOOK-%d"}`, i))
	}
	res := h.run(`{"tasks":[` + strings.Join(tasks, ",") + `]}`)
	if n := h.f.count(); n != 8 {
		t.Fatalf("%d subagents ran, want 8 after merging", n)
	}
	if !strings.Contains(res.Output, "## Dispatch notes") || !strings.Contains(res.Output, "merged an explore task") {
		t.Fatalf("no merge note:\n%s", res.Output)
	}
}

func TestDispatchRefusesTooManyCodeTasks(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	var tasks []string
	for i := 1; i <= 9; i++ {
		tasks = append(tasks, fmt.Sprintf(`{"role":"code","task":"EDIT-%d","files":["f%d.go"]}`, i, i))
	}
	res := h.run(`{"tasks":[` + strings.Join(tasks, ",") + `]}`)
	if !res.IsError || !strings.Contains(res.Output+res.Summary, "at most 8 tasks") {
		t.Fatalf("result = %+v", res)
	}
	if h.f.count() != 0 {
		t.Fatalf("%d model requests before a refusal", h.f.count())
	}
}

func TestDispatchReturnFormatReachesTheSubagent(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(int, recordedReq) reply { return turn(sseTurn{text: []string{"yes"}}) })
	h.run(`{"tasks":[{"role":"explore","task":"VERDICT-1 is it there?","returnFormat":"verdict"}]}`)
	if !strings.Contains(lastUserText(h.f.requests()[0]), "Return format: verdict.") {
		t.Fatalf("the subagent got no verdict instruction: %q", lastUserText(h.f.requests()[0]))
	}
}

func TestDispatchMarksCodeTaskThatEditedNothingAsNoDone(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(int, recordedReq) reply { return turn(sseTurn{text: []string{"it is fine as is"}}) })
	res := h.run(`{"tasks":[{"role":"code","task":"NODONE-1 change it","files":["never.go"]}]}`)
	if !strings.Contains(res.Output, "[no-done] it is fine as is") {
		t.Fatalf("no [no-done] marker:\n%s", res.Output)
	}
}

func TestDispatchShowsFilesTheSubagentRead(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	if err := os.WriteFile(filepath.Join(h.dir, "seen.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(map[string]string{"path": "seen.txt"})
	h.f.setPick(func(_ int, req recordedReq) reply {
		if lastRole(req) == "tool" {
			return turn(sseTurn{text: []string{"it reads fine"}})
		}
		return turn(sseTurn{calls: []sseCall{{id: "call_r", name: "read", args: string(args)}}})
	})
	res := h.run(`{"tasks":[{"role":"explore","task":"FOOTER-1 look"}]}`)
	if !strings.Contains(res.Output, "it reads fine\nFiles read: seen.txt") {
		t.Fatalf("no read footer:\n%s", res.Output)
	}
}

func TestStepBudgetAbsentMeansNoHintsAndNoStop(t *testing.T) {
	// A subagent without a budget keeps its old behaviour: no hints, no stop.
	a := New(Options{MaxSteps: 4})
	if h, stop := a.budgetHints(nil, 2); h != "" || stop {
		t.Fatalf("got hint=%q stop=%v", h, stop)
	}
}

func TestTextOnlyKeepsAnswerTextAndDropsCalls(t *testing.T) {
	parts := []llm.Part{
		{Type: llm.PartText, Text: "half an answer"},
		{Type: llm.PartToolCall, ToolCallID: "x", ToolName: "read", Args: json.RawMessage(`{}`)},
	}
	m := textOnly(parts)
	if len(m.Parts) != 1 || m.Text() != "half an answer" || len(m.ToolCalls()) != 0 {
		t.Fatalf("partial = %+v", m)
	}
	if got := textOnly([]llm.Part{{Type: llm.PartToolCall, ToolCallID: "y", ToolName: "read"}}); len(got.Parts) != 0 {
		t.Fatalf("a reply with no text should keep nothing, got %+v", got)
	}
}

func TestAbortKeepsTheAnswerTextProducedSoFar(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t, f.hang("half of the answer")))
	log := &eventLog{}
	a := New(baseOptions(f, tools.NewRegistry(), log, t.TempDir()))
	done := make(chan error, 1)
	go func() {
		_, err := a.Run(context.Background(), "tell me", nil)
		done <- err
	}()
	waitUntil(t, 5*time.Second, "first streamed text", func() bool { return len(log.of(EvText)) > 0 })
	a.Abort()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Abort")
	}
	msgs := a.Messages()
	last := msgs[len(msgs)-1]
	if last.Role != llm.RoleAssistant || last.Text() != "half of the answer" {
		t.Fatalf("history ends with %+v, want the partial answer kept", last)
	}
}

func TestNonObjectToolArgumentsAreRefusedNotRun(t *testing.T) {
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "read", args: `["not","an","object"]`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	log := &eventLog{}
	a := New(baseOptions(f, plainRegistry(), log, t.TempDir()))
	if _, err := a.Run(context.Background(), "go", nil); err != nil {
		t.Fatal(err)
	}
	var res string
	for _, m := range a.Messages() {
		for _, p := range m.Parts {
			if p.Type == llm.PartToolResult {
				res = p.Result
			}
		}
	}
	if !strings.Contains(res, "not a JSON object") || !strings.Contains(res, "was not run") {
		t.Fatalf("tool result = %q, want the refusal", res)
	}
}

func TestEditConflictsNameEveryAgentThatEditedAFile(t *testing.T) {
	got := editConflicts([]taskResult{
		{edited: []string{"a.go", "b.go"}},
		{edited: []string{"b.go"}},
		{edited: []string{"a.go"}},
	})
	want := []string{"a.go: edited by agents 1, 3", "b.go: edited by agents 1, 2"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("conflicts = %v, want %v", got, want)
	}
}

func TestSharedFileNotesNameTheAgentsThatTargetAFile(t *testing.T) {
	notes := sharedFileNotes([]subTask{
		{Role: "code", Files: []string{"x.go:1-5"}},
		{Role: "code", Files: []string{"x.go"}},
		{Role: "code", Files: []string{"y.go"}},
	})
	if len(notes) != 1 || !strings.Contains(notes[0], "agents 1, 2 all target x.go") {
		t.Fatalf("notes = %v", notes)
	}
}

func TestDispatchClampsConcurrencyToTwoToEight(t *testing.T) {
	// MaxConcurrent 1 is raised to 2: with 2 tasks that each wait on the same
	// gate, both must be in flight at once, or the test would time out.
	h := newDispatchHarness(t, plainRegistry)
	h.d.MaxConcurrent = 1
	gate := make(chan struct{})
	var once sync.Once
	h.f.setPick(func(_ int, req recordedReq) reply {
		return func(w http.ResponseWriter, r *http.Request) {
			once.Do(func() { go func() { time.Sleep(50 * time.Millisecond); close(gate) }() })
			<-gate
			turn(sseTurn{text: []string{"ok"}})(w, r)
		}
	})
	res := h.run(`{"tasks":[{"role":"explore","task":"CL-1"},{"role":"explore","task":"CL-2"}]}`)
	if res.IsError {
		t.Fatalf("both tasks should have finished: %+v", res)
	}
}

func TestFiveReadOnlyStepsInARowGetAHint(t *testing.T) {
	f := newFake(t)
	args, _ := json.Marshal(map[string]string{"path": "seen.txt"})
	var script []reply
	for i := 0; i < 5; i++ {
		script = append(script, turn(sseTurn{calls: []sseCall{{id: fmt.Sprintf("r%d", i), name: "read", args: string(args)}}}))
	}
	script = append(script, turn(sseTurn{text: []string{"summary"}}))
	f.setPick(scripted(t, script...))
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "seen.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	opts := baseOptions(f, plainRegistry(), &eventLog{}, dir)
	opts.MaxSteps = 12
	opts.Budget = &StepBudget{Kind: "explore", NudgeAt: 8}
	a := New(opts)
	if _, err := a.Run(context.Background(), "look", nil); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range a.Messages() {
		if strings.Contains(m.Text(), "5 read-only steps in a row") {
			found = true
		}
	}
	if !found {
		t.Fatal("no read-only streak hint in the history")
	}
}
