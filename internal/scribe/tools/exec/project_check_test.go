package exec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// A read-only project check runs check and list, never fix, and is marked
// read-only so read-only subagents may be given it.
func TestProjectCheckIsReadOnlyAndRefusesFix(t *testing.T) {
	jobs := NewJobs(Options{})
	tool := NewProjectCheck(jobs)
	if !tool.ReadOnly() {
		t.Fatal("project_check must be read-only")
	}
	if tool.Spec().Name != "project_check" {
		t.Fatalf("name = %q", tool.Spec().Name)
	}
	dir := fixture(t, map[string]string{"go.mod": "module example.com/x\n\ngo 1.22\n", "a.go": "package x\n"})
	env := newTestEnv(dir, nil)

	res := tool.Run(t.Context(), env, json.RawMessage(`{"action":"list"}`))
	if res.IsError || !strings.Contains(res.Output, "go") {
		t.Fatalf("list: %+v", res)
	}
	// fix is not a field of the read-only tool, so it cannot reach the write path.
	res = tool.Run(t.Context(), env, json.RawMessage(`{"action":"lint","fix":true}`))
	if !res.IsError || !strings.Contains(res.Output, "only check or list") {
		t.Fatalf("lint should be refused: %+v", res)
	}
	res = tool.Run(t.Context(), env, json.RawMessage(`{"action":"build"}`))
	if !res.IsError {
		t.Fatalf("build should be refused: %+v", res)
	}
}

// The schema offers only the read-only actions, and the main registry keeps
// the full project tool without the new one.
func TestProjectCheckNotInMainRegistry(t *testing.T) {
	r, _ := newTestRegistry(t, Options{})
	if _, ok := r.Get("project_check"); ok {
		t.Fatal("project_check must not be registered with the main agent")
	}
	if _, ok := r.Get("project"); !ok {
		t.Fatal("project tool missing from the main registry")
	}
	var _ tools.Tool = NewProjectCheck(NewJobs(Options{}))
}
