package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// projectScript is a fake project tool. Each check returns the next scripted
// result; the last one repeats. Every call's action is checked.
type projectScript struct {
	mu      sync.Mutex
	results []tools.Result
	calls   int
}

func (p *projectScript) tool() fnTool {
	return fnTool{name: "project", ro: true, run: func(_ context.Context, _ *tools.Env, args json.RawMessage) tools.Result {
		var in struct {
			Action string `json:"action"`
		}
		_ = json.Unmarshal(args, &in)
		p.mu.Lock()
		defer p.mu.Unlock()
		if in.Action != "check" {
			return tools.Errorf("unexpected action %q", in.Action)
		}
		i := p.calls
		if i >= len(p.results) {
			i = len(p.results) - 1
		}
		p.calls++
		return p.results[i]
	}}
}

func (p *projectScript) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

func withProject(p *projectScript) func() *tools.Registry {
	return func() *tools.Registry {
		r := plainRegistry()
		r.Register(p.tool())
		return r
	}
}

func TestDispatchVerifiesCodeEditsAndFixesFailingCheck(t *testing.T) {
	proj := &projectScript{results: []tools.Result{
		{IsError: true, Output: "parser.go:3: unused variable x"},
		{Output: "check passed"},
	}}
	h := newDispatchHarness(t, withProject(proj))
	usage := &sseUsage{prompt: 100, completion: 10}
	edit := writeThenReport("parser.go", "package p\n", "edited parser", usage)
	fix := writeThenReport("parser.go", "package p // fixed\n", "removed the unused variable", usage)
	h.f.setPick(func(_ int, req recordedReq) reply {
		task := lastUserText(req)
		switch {
		case strings.Contains(task, "project check failed"):
			return fix(req)
		case strings.Contains(task, "EDIT-PARSER"):
			return edit(req)
		}
		return providerError(400)
	})

	res := h.run(`{"tasks":[{"task":"EDIT-PARSER add the parser file","role":"code","files":["parser.go"]}]}`)
	if res.IsError || res.Summary != "1/1 agents finished" {
		t.Fatalf("result = %+v\n%s", res, res.Output)
	}
	if !strings.Contains(res.Output, "## Verification\nproject check failed; one follow-up code agent fixed it") {
		t.Fatalf("output has no verification outcome:\n%s", res.Output)
	}
	if res.Meta["verification"] != "passed after fix" {
		t.Fatalf("Meta[verification] = %v", res.Meta["verification"])
	}
	if proj.callCount() != 2 {
		t.Fatalf("project check ran %d times, want 2 (before and after the fix)", proj.callCount())
	}
	fixReq := requestMentioning(t, h.f, "project check failed")
	if !strings.Contains(lastUserText(fixReq), "unused variable x") || !strings.Contains(lastUserText(fixReq), "parser.go") {
		t.Fatalf("fix agent did not get the check output and the changed files:\n%s", lastUserText(fixReq))
	}
	if got, _ := os.ReadFile(filepath.Join(h.dir, "parser.go")); string(got) != "package p // fixed\n" {
		t.Fatalf("file after fix = %q", got)
	}
	if len(res.Files) != 1 || res.Files[0] != "parser.go" {
		t.Fatalf("Files = %v", res.Files)
	}
	u, _ := res.Meta["subagent_usage"].(llm.Usage)
	// Code task (2 requests) plus fix-up (2 requests), 100 input tokens each.
	if u.InputTokens != 400 {
		t.Fatalf("subagent usage input = %d, want 400 (both agents)", u.InputTokens)
	}
	// priced: $3 per MTok input, $15 per MTok output; 400 in and 40 out.
	cost, _ := res.Meta["subagent_cost_usd"].(float64)
	if want := (400*3 + 40*15) / 1e6; cost < want*0.999 || cost > want*1.001 {
		t.Fatalf("subagent cost = %v, want %v", cost, want)
	}
}

func TestDispatchReportsFailingCheckAfterFixAttempt(t *testing.T) {
	proj := &projectScript{results: []tools.Result{
		{IsError: true, Output: "parser.go:3: unused variable x"},
		{IsError: true, Output: "parser.go:3: still unused"},
	}}
	h := newDispatchHarness(t, withProject(proj))
	usage := &sseUsage{prompt: 10, completion: 1}
	edit := writeThenReport("parser.go", "package p\n", "edited parser", usage)
	fix := writeThenReport("parser.go", "package p // tried\n", "tried a fix", usage)
	h.f.setPick(func(_ int, req recordedReq) reply {
		task := lastUserText(req)
		switch {
		case strings.Contains(task, "project check failed"):
			return fix(req)
		case strings.Contains(task, "EDIT-PARSER"):
			return edit(req)
		}
		return providerError(400)
	})

	res := h.run(`{"tasks":[{"task":"EDIT-PARSER add the parser file","role":"code","files":["parser.go"]}]}`)
	if res.IsError {
		t.Fatalf("one agent finished, but IsError is set: %+v", res)
	}
	if res.Summary != "1/1 agents finished, check failing" {
		t.Fatalf("Summary = %q", res.Summary)
	}
	if !strings.Contains(res.Output, "project check still fails after one fix attempt") || !strings.Contains(res.Output, "still unused") {
		t.Fatalf("output does not report the failing check:\n%s", res.Output)
	}
	if res.Meta["verification"] != "failing" {
		t.Fatalf("Meta[verification] = %v", res.Meta["verification"])
	}
	if proj.callCount() != 2 {
		t.Fatalf("project check ran %d times, want 2: one fix attempt only", proj.callCount())
	}
}

func TestDispatchSkipsVerificationWithoutProjectTool(t *testing.T) {
	h := newDispatchHarness(t, plainRegistry)
	usage := &sseUsage{prompt: 10, completion: 1}
	edit := writeThenReport("parser.go", "package p\n", "edited parser", usage)
	h.f.setPick(func(_ int, req recordedReq) reply {
		if strings.Contains(lastUserText(req), "EDIT-PARSER") {
			return edit(req)
		}
		return providerError(400)
	})

	res := h.run(`{"tasks":[{"task":"EDIT-PARSER add the parser file","role":"code","files":["parser.go"]}]}`)
	if res.IsError || res.Summary != "1/1 agents finished" {
		t.Fatalf("result = %+v", res)
	}
	if !strings.Contains(res.Output, "skipped: no project tool is available") {
		t.Fatalf("output does not say the check was skipped:\n%s", res.Output)
	}
	if res.Meta["verification"] != "skipped" {
		t.Fatalf("Meta[verification] = %v", res.Meta["verification"])
	}
	if h.f.count() != 2 {
		t.Fatalf("%d model requests, want 2 (the code agent only; no fix-up)", h.f.count())
	}
}

func TestDispatchDoesNotVerifyExploreOnlyDispatch(t *testing.T) {
	proj := &projectScript{results: []tools.Result{{IsError: true, Output: "should not run"}}}
	h := newDispatchHarness(t, withProject(proj))
	h.f.setPick(func(_ int, req recordedReq) reply {
		return turn(sseTurn{text: []string{"found it"}})
	})

	res := h.run(`{"tasks":[{"task":"EXPLORE-ONLY where is the parser","role":"explore"}]}`)
	if res.IsError || res.Summary != "1/1 agents finished" {
		t.Fatalf("result = %+v", res)
	}
	if proj.callCount() != 0 {
		t.Fatalf("project check ran %d times for a dispatch with no edits", proj.callCount())
	}
	if strings.Contains(res.Output, "## Verification") {
		t.Fatalf("verification section present without edits:\n%s", res.Output)
	}
	if _, ok := res.Meta["verification"]; ok {
		t.Fatalf("Meta has a verification entry without edits: %v", res.Meta)
	}
}
