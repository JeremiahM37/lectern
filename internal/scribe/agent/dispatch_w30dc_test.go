package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// w30dcClaims is a fake tab-claim registry: absolute path -> owning tab label.
type w30dcClaims map[string]string

func (c w30dcClaims) OtherClaims() map[string]string { return c }

func w30dcTasks(files ...string) []subTask {
	return []subTask{{Task: "edit the thing", Role: "code", Files: files}}
}

func TestW30DCClaimedTargetGetsWarningOnTask(t *testing.T) {
	dir := t.TempDir()
	tasks := w30dcTasks("pkg/a.go:10-20")
	others := map[string]string{filepath.Join(dir, "pkg", "a.go"): "Scribe session 1a2b3c4d"}

	notes := claimConflicts(tasks, dir, others)
	if len(notes) != 1 || !strings.HasPrefix(notes[0], "agent 1: ") {
		t.Fatalf("dispatch notes = %q, want one line for agent 1", notes)
	}
	for _, want := range []string{"pkg/a.go (Scribe session 1a2b3c4d)", "may conflict", "still runs"} {
		if !strings.Contains(notes[0], want) {
			t.Fatalf("note %q lacks %q", notes[0], want)
		}
	}
	if len(tasks[0].claimNotes) != 1 {
		t.Fatalf("task carries %d claim notes, want 1", len(tasks[0].claimNotes))
	}
	// The subagent reads the same warning in its task text.
	text := taskText(tasks[0])
	if !strings.Contains(text, "Files claimed by other tabs: pkg/a.go (Scribe session 1a2b3c4d)") {
		t.Fatalf("task text lacks the claim warning:\n%s", text)
	}
}

func TestW30DCUnclaimedAndExploreTasksGetNoWarning(t *testing.T) {
	dir := t.TempDir()
	others := map[string]string{filepath.Join(dir, "a.go"): "another Scribe tab"}

	// A code task on other files is clean.
	clean := w30dcTasks("b.go")
	if notes := claimConflicts(clean, dir, others); len(notes) != 0 || len(clean[0].claimNotes) != 0 {
		t.Fatalf("unclaimed target warned: %q", notes)
	}
	// An explore task reads the claimed file; reads are never blocked or warned.
	explore := []subTask{{Task: "look", Role: "explore", Files: []string{"a.go"}}}
	if notes := claimConflicts(explore, dir, others); len(notes) != 0 || len(explore[0].claimNotes) != 0 {
		t.Fatalf("explore task warned: %q", notes)
	}
	// No claims, or no workdir to resolve relative targets against, is silent.
	if notes := claimConflicts(w30dcTasks("a.go"), dir, nil); notes != nil {
		t.Fatalf("no claims produced notes: %q", notes)
	}
	if notes := claimConflicts(w30dcTasks("a.go"), "", others); notes != nil {
		t.Fatalf("empty workdir produced notes: %q", notes)
	}
}

func TestW30DCOneWarningPerTaskNamesEveryClaimedFileOnce(t *testing.T) {
	dir := t.TempDir()
	others := map[string]string{
		filepath.Join(dir, "x.go"): "Scribe session aaaaaaaa",
		filepath.Join(dir, "y.go"): "Scribe session bbbbbbbb",
	}
	tasks := w30dcTasks("x.go", "x.go:5", "y.go", "z.go")
	notes := claimConflicts(tasks, dir, others)
	if len(notes) != 1 {
		t.Fatalf("notes = %q, want one", notes)
	}
	want := "x.go (Scribe session aaaaaaaa), y.go (Scribe session bbbbbbbb)"
	if !strings.Contains(notes[0], want) {
		t.Fatalf("note %q does not name %q", notes[0], want)
	}
	if strings.Contains(notes[0], "z.go") {
		t.Fatalf("unclaimed z.go named: %q", notes[0])
	}
}

// A dispatch whose code task names a file another tab holds runs the task,
// warns on the task and in the result, and never refuses it.
func TestW30DCDispatchWarnsCodeTaskAndStillRunsIt(t *testing.T) {
	f := newFake(t)
	f.setPick(func(_ int, req recordedReq) reply {
		task := openingOf(req)
		if strings.Contains(task, "CODE-1") {
			return turn(sseTurn{text: []string{"report CODE-1"}})
		}
		return turn(sseTurn{text: []string{"report EXPLORE-1"}})
	})

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte("package pkg\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	log := &eventLog{}
	reg := tools.NewRegistry()
	reg.Register(readTool{})
	d := &Dispatcher{
		MaxConcurrent: 2,
		Sink:          log.sink,
		New: func(spec SubagentSpec, sink Sink) *Agent {
			opts := baseOptions(f, reg, log, dir)
			opts.Sink = sink
			opts.ID = spec.ID
			opts.System = func(mode string) string {
				return BuildSystem(PromptInput{Workdir: dir, Subagent: spec.Role}, mode)
			}
			return New(opts)
		},
	}
	tr := tools.NewRegistry()
	d.Register(tr)
	dispatch, _ := tr.Get("dispatch")

	args := `{"tasks":[` +
		`{"task":"CODE-1 change the parser","role":"code","files":["pkg/a.go"]},` +
		`{"task":"EXPLORE-1 find the lexers","role":"explore","files":["pkg/a.go"]}]}`
	env := &tools.Env{
		Workdir: dir, CallID: "call_w30", Mode: "default",
		Claims: w30dcClaims{filepath.Join(dir, "pkg", "a.go"): "Scribe session 77778888"},
	}
	res := dispatch.Run(context.Background(), env, json.RawMessage(args))

	if res.IsError {
		t.Fatalf("dispatch refused a claimed target; it must only warn:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "## Dispatch notes") ||
		!strings.Contains(res.Output, "agent 1: Files claimed by other tabs: pkg/a.go (Scribe session 77778888)") {
		t.Fatalf("result does not name the claimed file and its tab:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "agent 2: Files claimed") {
		t.Fatalf("explore task warned:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "report CODE-1") {
		t.Fatalf("claimed code task did not run:\n%s", res.Output)
	}
	var codeReq string
	for _, r := range f.requests() {
		if strings.Contains(openingOf(r), "CODE-1") {
			codeReq = lastUserText(r)
		}
	}
	if !strings.Contains(codeReq, "Files claimed by other tabs: pkg/a.go (Scribe session 77778888)") {
		t.Fatalf("subagent text lacks the claim warning:\n%s", codeReq)
	}
}

// With no claims in the environment the planning check is silent, and a nil
// claim source is allowed (headless runs).
func TestW30DCDispatchWithoutClaimSourceIsSilent(t *testing.T) {
	dir := t.TempDir()
	tasks := w30dcTasks("a.go")
	if notes := claimConflicts(tasks, dir, nil); len(notes) != 0 {
		t.Fatalf("notes = %q", notes)
	}
}
