package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fakeProject is a project tool that records the lint fix it was asked for.
type fakeProject struct {
	files   []string
	timeout time.Duration
	report  string
}

func (f *fakeProject) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "project", Schema: tools.Schema(map[string]any{}, "action")}
}
func (f *fakeProject) ReadOnly() bool { return false }
func (f *fakeProject) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	return tools.Result{Output: "check"}
}
func (f *fakeProject) LintFixFiles(_ context.Context, _ *tools.Env, files []string, timeout time.Duration) (string, bool) {
	f.files, f.timeout = files, timeout
	return f.report, true
}

func TestLintFixRunsOnceOverTheEditedFiles(t *testing.T) {
	p := &fakeProject{report: "lint fix ran on 2 target(s); clean"}
	reg := tools.NewRegistry()
	reg.Register(p)
	env := &tools.Env{Workdir: t.TempDir(), CallID: "d1"}

	got := lintFix(context.Background(), env, reg, []string{"a.go", "b.go"})
	if got != p.report {
		t.Fatalf("report = %q", got)
	}
	if strings.Join(p.files, ",") != "a.go,b.go" {
		t.Fatalf("files = %v", p.files)
	}
	if p.timeout != 30*time.Second {
		t.Fatalf("timeout = %v; the spec bounds the pass at 30 s", p.timeout)
	}
}

func TestLintFixSkipsWhenNothingCanRun(t *testing.T) {
	env := &tools.Env{Workdir: t.TempDir()}
	// No changed files: nothing to fix.
	if got := lintFix(context.Background(), env, tools.NewRegistry(), nil); got != "" {
		t.Fatalf("no files should give no report, got %q", got)
	}
	// A registry whose project tool cannot scope a fix is skipped.
	reg := tools.NewRegistry()
	reg.Register(plainProject{})
	if got := lintFix(context.Background(), env, reg, []string{"a.go"}); got != "" {
		t.Fatalf("a project tool without LintFixFiles should be skipped, got %q", got)
	}
}

type plainProject struct{ fakeProject }

func (plainProject) Spec() llm.ToolSpec {
	return llm.ToolSpec{Name: "project", Schema: tools.Schema(map[string]any{}, "action")}
}
func (plainProject) ReadOnly() bool { return false }
func (plainProject) Run(context.Context, *tools.Env, json.RawMessage) tools.Result {
	return tools.Result{}
}
