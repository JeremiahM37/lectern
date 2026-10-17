package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// The model reads a dispatch result between tags, so it can tell a subagent's
// report from its own text (spec 03 §3.1). The user still sees the plain
// output in the tool event.
func TestDispatchOutputReachesTheModelAsDispatchResult(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(_ int, req recordedReq) reply {
		if strings.HasPrefix(systemText(req), "SYS explore") {
			return turn(sseTurn{text: []string{"the parser is in parse.go:12"}})
		}
		if lastRole(req) == "tool" {
			return turn(sseTurn{text: []string{"done"}})
		}
		return turn(sseTurn{calls: []sseCall{{id: "call_d", name: "dispatch", args: `{"tasks":[{"task":"find the parser","role":"explore"}]}`}}})
	})
	reg := plainRegistry()
	h.d.Register(reg)
	log := &eventLog{}
	a := New(baseOptions(h.f, reg, log, h.dir))
	if _, err := a.Run(context.Background(), "where is the parser?", nil); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := h.f.requests()
	mainReq := reqs[len(reqs)-1]
	got := toolMsg(t, mainReq, "call_d").text()
	if !strings.HasPrefix(got, "<dispatch_result>\n") || !strings.HasSuffix(got, "\n</dispatch_result>") {
		t.Fatalf("model got %q, want the output inside <dispatch_result>", got)
	}
	if !strings.Contains(got, "parse.go:12") {
		t.Fatalf("the subagent report is missing from %q", got)
	}
	for _, e := range log.of(EvToolResult) {
		if e.Tool == "dispatch" && strings.Contains(e.Result.Output, "dispatch_result") {
			t.Fatalf("the user-facing tool output carries the model tags: %q", e.Result.Output)
		}
	}
}

// A task that names a task-list item marks the item done when it finishes,
// and blocked when it fails. The report says which item it was for.
func TestDispatchMarksTaskListItemsDoneOrBlocked(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(_ int, req recordedReq) reply {
		switch openingOf(req) {
		case "find the parser", "no list item":
			return turn(sseTurn{text: []string{"parse.go:12"}})
		case "check the lexer":
			return providerError(400)
		}
		t.Errorf("unexpected task %q", openingOf(req))
		return providerError(500)
	})
	var mu sync.Mutex
	marks := map[string]string{}
	h.d.TaskStatus = func(id, status string) {
		mu.Lock()
		defer mu.Unlock()
		marks[id] = status
	}
	res := h.run(`{"tasks":[` +
		`{"task":"find the parser","role":"explore","taskId":"3"},` +
		`{"task":"check the lexer","role":"explore","taskId":"4"},` +
		`{"task":"no list item","role":"explore"}]}`)
	if marks["3"] != "done" || marks["4"] != "blocked" {
		t.Fatalf("marks = %v, want 3 done and 4 blocked", marks)
	}
	if _, ok := marks[""]; ok || len(marks) != 2 {
		t.Fatalf("a task without a taskId must not mark anything: %v", marks)
	}
	for _, want := range []string{"taskId=3", "taskId=4"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("report does not link %s:\n%s", want, res.Output)
		}
	}
}

// A subagent that throws after posting findings and reading files still
// hands back what it found, not only the failure (spec 03 §3.6).
func TestFailedSubagentKeepsItsFindingsAndReads(t *testing.T) {
	f := newFake(t)
	dir := t.TempDir()
	must(t, os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello"), 0o644))
	log := &eventLog{}
	d := &Dispatcher{MaxConcurrent: 3, Sink: log.sink, retryWait: time.Millisecond}
	d.New = func(spec SubagentSpec, sink Sink) *Agent {
		reg := plainRegistry()
		for _, bt := range BusTools(spec.ID, spec.Bus) {
			reg.Register(bt)
		}
		opts := baseOptions(f, reg, log, dir)
		opts.Sink, opts.ID = sink, spec.ID
		return New(opts)
	}
	f.setPick(func(_ int, req recordedReq) reply {
		if lastRole(req) == "tool" {
			return providerError(400)
		}
		return turn(sseTurn{calls: []sseCall{
			{id: "c_read", name: "read", args: `{"path":"a.txt"}`},
			{id: "c_find", name: "report_finding", args: `{"content":"the lexer lives here","file":"lexer.go","line":"10"}`},
		}})
	})
	h := &dispatchHarness{t: t, f: f, log: log, dir: dir, d: d}
	res := h.run(`{"tasks":[{"role":"explore","task":"find the lexer"}]}`)
	for _, want := range []string{"stopped early", "Agent failed but produced partial results", "lexer.go:10", "Files read: a.txt"} {
		if !strings.Contains(res.Output, want) {
			t.Fatalf("salvaged report lacks %q:\n%s", want, res.Output)
		}
	}
}

// A failure with no work behind it has nothing to salvage.
func TestSalvageReportIsEmptyWithoutWork(t *testing.T) {
	if got := salvageReport(nil, "a1", nil, nil); got != "" {
		t.Fatalf("salvage with no work = %q, want empty", got)
	}
	got := salvageReport([]Finding{{Agent: "a2", File: "x.go", Line: 3, Text: "not mine"}}, "a1", nil, nil)
	if got != "" {
		t.Fatalf("another subagent's finding must not be salvaged: %q", got)
	}
}
