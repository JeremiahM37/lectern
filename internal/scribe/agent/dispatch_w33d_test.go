package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// w33dLintTool is a project tool that can lint-fix files. Each fix is recorded
// so a test can see whether the lint pass ran and on what.
type w33dLintTool struct {
	fixed *[]string
}

func (w33dLintTool) Spec() llm.ToolSpec {
	return llm.ToolSpec{
		Name:        "project",
		Description: "project test tool",
		Schema:      tools.Schema(map[string]any{"action": tools.Str("action")}, "action"),
	}
}
func (w33dLintTool) ReadOnly() bool { return true }
func (w33dLintTool) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	return tools.Result{Output: "check ok"}
}
func (t w33dLintTool) LintFixFiles(_ context.Context, _ *tools.Env, files []string, _ time.Duration) (string, bool) {
	*t.fixed = append(*t.fixed, strings.Join(files, ","))
	return "fixed 1 file", true
}

// w33dWriteThenFailOnReply edits notes.txt, then the provider fails on the
// reply after the tool result, so the subagent stops with an edit and no text.
func w33dWriteThenFail(req recordedReq) reply {
	if lastRole(req) == "tool" {
		return providerError(400)
	}
	args, _ := json.Marshal(map[string]string{"path": "notes.txt", "content": "hello\n"})
	return turn(sseTurn{calls: []sseCall{{id: "call_write", name: "write_file", args: string(args)}}})
}

func TestW33DVerdictReadsTheWordBeforeAnyLocation(t *testing.T) {
	cases := map[string]string{
		"VERDICT: PASS":                       "pass",
		"reads fine\nVERDICT: FAIL":           "fail",
		"**VERDICT: FAIL — main.go:3**":       "fail",
		"VERDICT: FAIL — notes.txt:12":        "fail",
		"VERDICT: PARTIAL":                    "partial",
		"VERDICT: PARTIAL — half the fix":     "partial",
		"VERDICT: MAYBE":                      "unknown",
		"VERDICT:":                            "unknown",
		"no verdict at all":                   "unknown",
		"VERDICT: FAILING the whole thing":    "unknown",
		"VERDICT: FAIL\nlater\nVERDICT: PASS": "pass",
	}
	for report, want := range cases {
		if got := verdictOf(report); got != want {
			t.Errorf("verdictOf(%q) = %q, want %q", report, got, want)
		}
	}
}

func TestW33DRenderReviewNamesTheVerdictOnItsOwnLine(t *testing.T) {
	if got := renderReview("bad call at x.go:4", "fail"); got != "Verdict: FAIL\nbad call at x.go:4" {
		t.Fatalf("fail review = %q", got)
	}
	if got := renderReview("ok", "partial"); got != "Verdict: PARTIAL\nok" {
		t.Fatalf("partial review = %q", got)
	}
	if got := renderReview("not run: breaker", "unknown"); !strings.HasPrefix(got, "Verdict: none stated") {
		t.Fatalf("unknown review = %q", got)
	}
}

func TestW33DRenderCleanupKeepsLintCleanupAndItsEdits(t *testing.T) {
	got := renderCleanup("fixed 2 files", "removed a debug print", []string{"a.go", "b.go"})
	want := "Lint fix: fixed 2 files\nCleanup agent: removed a debug print\nFiles edited by cleanup: a.go, b.go\n"
	if got != want {
		t.Fatalf("cleanup = %q, want %q", got, want)
	}
	if renderCleanup("", "", nil) != "" {
		t.Fatal("an empty cleanup must render nothing")
	}
}

func TestW33DCacheTallyCountsPerSubagentAndInTotal(t *testing.T) {
	var c cacheTally
	c.note(CacheNote{Agent: "a1", Outcome: CacheStore})
	c.note(CacheNote{Agent: "a2", Outcome: CacheHit, From: "a1"})
	c.note(CacheNote{Agent: "a2", Outcome: CacheHit, From: "a1"})
	got := c.render(map[string]string{"a2": "explore: x"})
	want := "File reads: 2 hits, 0 waits, 1 miss\n- a1: 1 miss\n- a2 (explore: x): 2 hits (from a1)\n"
	if got != want {
		t.Fatalf("cache section =\n%q\nwant\n%q", got, want)
	}
	var none cacheTally
	if none.render(nil) != "" {
		t.Fatal("a tally with no notes must render nothing")
	}
}

func TestW33DSplitRangeReadsTheSpanAndKeepsTheRestAsWritten(t *testing.T) {
	cases := []struct {
		in         string
		path       string
		start, end int
		ok         bool
	}{
		{"a.go:10-20", "a.go", 10, 20, true},
		{"a.go:7", "a.go", 7, 7, true},
		{"a.go:20-10", "a.go", 10, 20, true},
		{"a.go:0-3", "a.go", 1, 3, true},
		{"a.go:5-", "a.go:5-", 0, 0, false},
		{"plain.go", "plain.go", 0, 0, false},
	}
	for _, c := range cases {
		path, start, end, ok := splitRange(c.in)
		if path != c.path || start != c.start || end != c.end || ok != c.ok {
			t.Errorf("splitRange(%q) = %q %d %d %v, want %q %d %d %v", c.in, path, start, end, ok, c.path, c.start, c.end, c.ok)
		}
	}
}

func TestW33DCodeLockKeysDropRangesAndDedupe(t *testing.T) {
	got := codeLockKeys([]string{"a.go:1-5", "a.go:9-9", "a.go", "b.go:3"})
	if len(got) != 2 || got[0] != "a.go" || got[1] != "b.go" {
		t.Fatalf("lock keys = %v, want [a.go b.go]", got)
	}
}

func TestW33DRangeReachesTheSubagentAsALineSpan(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	if err := os.WriteFile(filepath.Join(h.dir, "notes.txt"), []byte("1\n2\n3\n4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	usage := &sseUsage{prompt: 10, completion: 1}
	write := writeThenReport("notes.txt", "x\n", "edited the range", usage)
	h.f.setPick(func(_ int, req recordedReq) reply { return write(req) })

	res := h.run(`{"tasks":[{"task":"W33-RANGE tidy the middle","role":"code","files":["notes.txt:2-3"]}]}`)
	if res.IsError {
		t.Fatalf("dispatch failed: %+v", res)
	}
	want := "Relevant files: notes.txt (lines 2-3: read and edit this range)"
	if !strings.Contains(lastUserText(requestMentioning(t, h.f, "W33-RANGE")), want) {
		t.Fatalf("the subagent was not given the range; task text:\n%s", lastUserText(requestMentioning(t, h.f, "W33-RANGE")))
	}
}

func TestW33DCodeTaskWithTwoRangesOfOneFileDoesNotDeadlock(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	usage := &sseUsage{prompt: 10, completion: 1}
	write := writeThenReport("notes.txt", "x\n", "done", usage)
	h.f.setPick(func(_ int, req recordedReq) reply { return write(req) })
	// A second lock of the same file would hang the dispatch; h.run fails the
	// test if it does not return.
	res := h.run(`{"tasks":[{"task":"W33-TWO two spans","role":"code","files":["notes.txt:1-1","notes.txt:2-2"]}]}`)
	if res.IsError {
		t.Fatalf("dispatch failed: %+v", res)
	}
}

func TestW33DFooterListsEditedFilesWhenTheSubagentFails(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.f.setPick(func(_ int, req recordedReq) reply {
		if strings.Contains(lastUserText(req), "W33-EDITFAIL") {
			return w33dWriteThenFail(req)
		}
		return providerError(400)
	})
	res := h.run(`{"tasks":[{"task":"W33-EDITFAIL write then fail","role":"code","files":["notes.txt"]}]}`)
	if !strings.Contains(res.Output, "Files edited: notes.txt") {
		t.Fatalf("a failed subagent's footer must list the file it edited:\n%s", res.Output)
	}
	// Spec 03 §3.6: a failed subagent with reads, edits or findings is
	// salvaged and counts as done, marked as stopped early.
	if res.IsError || !strings.Contains(res.Output, "stopped early") {
		t.Fatalf("a salvaged subagent must count as done and say it stopped early (IsError=%v):\n%s", res.IsError, res.Output)
	}
}

func TestW33DReviewSectionStatesTheVerdict(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	usage := &sseUsage{prompt: 10, completion: 1}
	write := writeThenReport("notes.txt", "hello\n", "wrote notes", usage)
	h.f.setPick(func(_ int, req recordedReq) reply {
		task := lastUserText(req)
		switch {
		case strings.Contains(task, "W33-REVIEW"):
			return write(req)
		case strings.Contains(task, "Review the edits"):
			return turn(sseTurn{text: []string{"the call at notes.txt:1 is wrong\nVERDICT: FAIL — notes.txt:1"}, usage: usage})
		}
		return providerError(400)
	})
	h.d.Features = &Features{VerifyEdits: true, TargetFileValidation: true, TierRouting: true, DispatchCache: true}

	res := h.run(`{"tasks":[{"task":"W33-REVIEW add notes","role":"code","files":["notes.txt"]}]}`)
	if !strings.Contains(res.Output, "## Review\nVerdict: FAIL\nthe call at notes.txt:1 is wrong") {
		t.Fatalf("review section does not state the verdict:\n%s", res.Output)
	}
	if res.Meta["review"] != "fail" || !strings.Contains(res.Summary, "review failing") {
		t.Fatalf("review meta/summary = %v / %q", res.Meta["review"], res.Summary)
	}
}

func TestW33DLintFixRunsOnlyUnderDesloppify(t *testing.T) {
	var fixed []string
	h := newDispatchHarness(t, func() *tools.Registry {
		r := plainRegistry()
		r.Register(w33dLintTool{fixed: &fixed})
		return r
	})
	usage := &sseUsage{prompt: 10, completion: 1}
	write := writeThenReport("notes.txt", "hello\n", "wrote notes", usage)
	h.f.setPick(func(_ int, req recordedReq) reply {
		task := lastUserText(req)
		switch {
		case strings.Contains(task, "W33-LINT"):
			return write(req)
		case strings.Contains(task, "Clean up the code"):
			return turn(sseTurn{text: []string{"removed a debug print"}, usage: usage})
		}
		return providerError(400)
	})

	// Default features: desloppify is off, so no lint fix runs.
	res := h.run(`{"tasks":[{"task":"W33-LINT add notes","role":"code","files":["notes.txt"]}]}`)
	if len(fixed) != 0 || strings.Contains(res.Output, "## Cleanup") {
		t.Fatalf("lint ran with desloppify off: fixed=%v\n%s", fixed, res.Output)
	}

	fixed = nil
	h.d.Features = &Features{Desloppify: true, TargetFileValidation: true, TierRouting: true, DispatchCache: true}
	res = h.run(`{"tasks":[{"task":"W33-LINT add notes","role":"code","files":["notes.txt"]}]}`)
	if len(fixed) != 1 || fixed[0] != "notes.txt" {
		t.Fatalf("lint fixed %v, want [notes.txt]", fixed)
	}
	for _, want := range []string{"## Cleanup", "Lint fix: fixed 1 file", "Cleanup agent: removed a debug print"} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("cleanup section lacks %q:\n%s", want, res.Output)
		}
	}
}

func TestW33DCacheSectionNamesEachSubagentsHitsAndMisses(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	if err := os.WriteFile(filepath.Join(h.dir, "notes.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Each subagent reads notes.txt once, then answers. The second waits for
	// the first, so its read is served from the cache.
	h.f.setPick(func(_ int, req recordedReq) reply {
		if lastRole(req) == "tool" {
			return turn(sseTurn{text: []string{"read it"}})
		}
		args, _ := json.Marshal(map[string]string{"path": "notes.txt"})
		return turn(sseTurn{calls: []sseCall{{id: "call_read", name: "read", args: string(args)}}})
	})
	h.d.New = func(spec SubagentSpec, sink Sink) *Agent {
		reg := tools.NewRegistry()
		var rt tools.Tool = readTool{}
		if spec.ReadCache {
			rt = ReadCacheTool(readTool{}, spec.Bus, spec.ID)
		}
		reg.Register(rt)
		opts := baseOptions(h.f, reg, h.log, h.dir)
		opts.Sink = sink
		opts.ID = spec.ID
		opts.System = func(string) string { return "SYS" }
		return New(opts)
	}

	res := h.run(`{"tasks":[` +
		`{"id":"first","task":"W33-CACHE-FIRST look at notes","role":"explore"},` +
		`{"id":"second","task":"W33-CACHE-SECOND look at notes","role":"explore","depends_on":["first"]}]}`)
	for _, want := range []string{
		"## Cache\nFile reads: 1 hit, 0 waits, 1 miss\n",
		"- a1 (explore: W33-CACHE-FIRST look at notes): 1 miss\n",
		"- a2 (explore: W33-CACHE-SECOND look at notes): 1 hit (from a1)",
	} {
		if !strings.Contains(res.Output, want) {
			t.Errorf("output lacks %q:\n%s", want, res.Output)
		}
	}
}

func TestW33DCacheSectionIsAbsentWhenTheCacheIsOff(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	h.d.Features = &Features{TargetFileValidation: true, TierRouting: true}
	h.f.setPick(func(_ int, req recordedReq) reply { return turn(sseTurn{text: []string{"done"}}) })
	res := h.run(`{"tasks":[{"task":"W33-OFF look","role":"explore"}]}`)
	if strings.Contains(res.Output, "## Cache") {
		t.Fatalf("a cache section appeared with the cache off:\n%s", res.Output)
	}
}
