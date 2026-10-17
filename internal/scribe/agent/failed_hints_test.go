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

// fileProject lays out files under a fresh project root and returns it.
func fileProject(t *testing.T, files ...string) string {
	t.Helper()
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// missingFileTool fails every call on a path that is not in exists, with the
// same wording the fs tools use, and succeeds on the others.
func missingFileTool(name string, exists ...string) fnTool {
	have := map[string]bool{}
	for _, e := range exists {
		have[e] = true
	}
	return fnTool{name: name, run: func(_ context.Context, _ *tools.Env, args json.RawMessage) tools.Result {
		var in struct {
			Path string `json:"path"`
		}
		_ = json.Unmarshal(args, &in)
		if !have[in.Path] {
			return tools.Errorf("File not found: %s. Use glob or list_dir to find the right path.", in.Path)
		}
		return tools.Result{Output: "ok " + in.Path}
	}}
}

// finalUserText is the user text of the last request. Each request carries
// the whole history, so the last one holds every notice sent in the run once.
func finalUserText(t *testing.T, f *fakeLLM) string {
	t.Helper()
	reqs := f.requests()
	if len(reqs) == 0 {
		t.Fatal("no requests recorded")
	}
	return w16UserText(reqs[len(reqs)-1])
}

// A missing path is named on the next step with the closest file in the
// project, in a reminder rather than the system prompt.
func TestFailedPathHintNamesSimilarFileOnNextStep(t *testing.T) {
	root := fileProject(t, "src/main.go", "src/other.go")
	reg := tools.NewRegistry()
	reg.Register(missingFileTool("read", "src/main.go"))
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "read", args: `{"path":"src/mian.go"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, root))
	if _, err := a.Run(context.Background(), "read the file", nil); err != nil {
		t.Fatal(err)
	}

	reqs := f.requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	want := "`src/mian.go` does not exist. Did you mean `src/main.go`?"
	if got := w16UserText(reqs[1]); !strings.Contains(got, want) {
		t.Fatalf("next step has no hint %q; user text:\n%s", want, got)
	}
	if got := w16UserText(reqs[1]); !strings.Contains(got, "<system-reminder>\n"+want) {
		t.Fatalf("hint is not wrapped in a system reminder:\n%s", got)
	}
	if strings.Contains(w16UserText(reqs[0]), "does not exist") {
		t.Fatal("hint sent before any failure")
	}
	// The system prompt is the same on every request.
	sys0 := msgsWithRole(reqs[0], "system")
	sys1 := msgsWithRole(reqs[1], "system")
	if len(sys0) == 0 || len(sys0) != len(sys1) || sys0[0].text() != sys1[0].text() {
		t.Fatal("system prompt changed between steps")
	}
}

// The same path is hinted once in a turn, even when the model keeps asking
// for it.
func TestFailedPathHintIsNotRepeatedInTurn(t *testing.T) {
	root := fileProject(t, "src/main.go")
	reg := tools.NewRegistry()
	reg.Register(missingFileTool("read", "src/main.go"))
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "read", args: `{"path":"src/mian.go"}`}}}),
		turn(sseTurn{calls: []sseCall{{id: "c2", name: "read", args: `{"path":"src/mian.go"}`}}}),
		turn(sseTurn{calls: []sseCall{{id: "c3", name: "read", args: `{"path":"src/mian.go"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, root))
	if _, err := a.Run(context.Background(), "read it", nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(finalUserText(t, f), "`src/mian.go` does not exist"); n != 1 {
		t.Fatalf("hint sent %d times, want 1", n)
	}
}

// A new turn starts with a clean slate, so the same path can be hinted again.
func TestFailedPathHintResetsOnNewTurn(t *testing.T) {
	root := fileProject(t, "src/main.go")
	reg := tools.NewRegistry()
	reg.Register(missingFileTool("read", "src/main.go"))
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "read", args: `{"path":"src/mian.go"}`}}}),
		turn(sseTurn{text: []string{"first"}}),
		turn(sseTurn{calls: []sseCall{{id: "c2", name: "read", args: `{"path":"src/mian.go"}`}}}),
		turn(sseTurn{text: []string{"second"}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, root))
	if _, err := a.Run(context.Background(), "first", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Run(context.Background(), "second", nil); err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(finalUserText(t, f), "`src/mian.go` does not exist"); n != 2 {
		t.Fatalf("hint sent %d times across two turns, want 2", n)
	}
}

// A success on the same path in the same step withdraws the hint: the model
// created the file, so telling it the file is missing would be wrong.
func TestFailedPathHintWithdrawnWhenPathSucceeds(t *testing.T) {
	root := fileProject(t)
	reg := tools.NewRegistry()
	reg.Register(missingFileTool("read"))
	reg.Register(fnTool{name: "write", run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		return tools.Result{Output: "wrote"}
	}})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{
			{id: "c1", name: "read", args: `{"path":"docs/notes.md"}`},
			{id: "c2", name: "write", args: `{"path":"docs/notes.md"}`},
		}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, root))
	if _, err := a.Run(context.Background(), "make the notes", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(finalUserText(t, f), "does not exist") {
		t.Fatal("hint sent for a path that exists after the step")
	}
}

// With no similar name in the project the reminder points at the search
// tools instead of inventing a suggestion.
func TestFailedPathHintWithoutSuggestionPointsAtSearch(t *testing.T) {
	root := fileProject(t, "unrelated/zzz.txt")
	reg := tools.NewRegistry()
	reg.Register(missingFileTool("read"))
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "read", args: `{"path":"config/server.yaml"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, root))
	if _, err := a.Run(context.Background(), "read config", nil); err != nil {
		t.Fatal(err)
	}
	got := w16UserText(f.requests()[1])
	if !strings.Contains(got, "`config/server.yaml` does not exist. Use glob or list_dir") {
		t.Fatalf("no search pointer in hint:\n%s", got)
	}
	if strings.Contains(got, "Did you mean") {
		t.Fatalf("suggested a name for an unrelated file:\n%s", got)
	}
}

// An edit that cannot find its text in an existing file is not a missing
// path, so it gets no hint.
func TestFailedPathHintIgnoresTextNotFound(t *testing.T) {
	root := fileProject(t, "src/main.go")
	reg := tools.NewRegistry()
	reg.Register(fnTool{name: "edit_file", run: func(context.Context, *tools.Env, json.RawMessage) tools.Result {
		return tools.Errorf("old_string not found in src/main.go")
	}})
	f := newFake(t)
	f.setPick(scripted(t,
		turn(sseTurn{calls: []sseCall{{id: "c1", name: "edit_file", args: `{"path":"src/main.go"}`}}}),
		turn(sseTurn{text: []string{"done"}}),
	))
	a := New(baseOptions(f, reg, &eventLog{}, root))
	if _, err := a.Run(context.Background(), "edit", nil); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(finalUserText(t, f), "does not exist") {
		t.Fatal("text-not-found failure was reported as a missing path")
	}
}

func TestSimilarPathsRanksClosestFirst(t *testing.T) {
	root := fileProject(t, "a/main.go", "b/main.go", "pkg/mainly.go", "node_modules/main.go", ".git/main.go", "zzz.txt")
	got := similarPaths(root, "mian.go", 3, 100)
	want := []string{"a/main.go", "b/main.go"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("similarPaths = %v, want %v", got, want)
	}
}

// The walk stops at its limit, so a huge tree costs a bounded amount.
func TestSimilarPathsStopsAtLimit(t *testing.T) {
	root := fileProject(t, "d1/main.go", "d2/main.go", "d3/main.go")
	// Lexical walk order: d1, d1/main.go, d2, d2/main.go, then d3 is past the limit.
	got := similarPaths(root, "main.go", 10, 4)
	if len(got) != 2 || got[0] != "d1/main.go" || got[1] != "d2/main.go" {
		t.Fatalf("similarPaths = %v, want the two entries inside the limit", got)
	}
}

func TestIsNotFoundMarkers(t *testing.T) {
	cases := map[string]bool{
		"File not found: a.go. Use glob":    true,
		"open x: no such file or directory": true,
		"ENOENT: no such file":              true,
		"old_string not found in a.go":      false,
		"permission denied":                 false,
	}
	for out, want := range cases {
		if got := isNotFound(out); got != want {
			t.Errorf("isNotFound(%q) = %v, want %v", out, got, want)
		}
	}
}

func TestJoinOr(t *testing.T) {
	cases := map[string]string{
		"":        "",
		"a":       "a",
		"a, b":    "a or b",
		"a, b, c": "a, b, or c",
	}
	for in, want := range cases {
		var items []string
		if in != "" {
			items = strings.Split(in, ", ")
		}
		if got := joinOr(items); got != want {
			t.Errorf("joinOr(%q) = %q, want %q", in, got, want)
		}
	}
}
