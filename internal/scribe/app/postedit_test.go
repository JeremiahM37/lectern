package app

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fakeDiags answers Diagnostics from a script: each call pops the next
// result for the file. Active is fixed per test.
type fakeDiags struct {
	active bool
	script [][]lsp.FileDiagnostic
	calls  int
}

func (f *fakeDiags) Active(string) bool { return f.active }

func (f *fakeDiags) Diagnostics(context.Context, string) ([]lsp.FileDiagnostic, error) {
	if f.calls >= len(f.script) {
		return nil, nil
	}
	out := f.script[f.calls]
	f.calls++
	return out, nil
}

func errDiag(line int, msg string) lsp.FileDiagnostic {
	return lsp.FileDiagnostic{Line: line, Column: 1, Severity: "error", Source: "gopls", Code: "T", Message: msg}
}

func warnDiag(line int, msg string) lsp.FileDiagnostic {
	return lsp.FileDiagnostic{Line: line, Column: 1, Severity: "warning", Source: "vet", Message: msg}
}

func writeTemp(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPostEditGofmtFormatsWrittenFile checks that an unformatted write is
// formatted on disk and the result says so.
func TestPostEditGofmtFormatsWrittenFile(t *testing.T) {
	gofmt := filepath.Join(runtime.GOROOT(), "bin", "gofmt")
	if _, err := os.Stat(gofmt); err != nil {
		if gofmt, err = exec.LookPath("gofmt"); err != nil {
			t.Skip("gofmt not available")
		}
	}
	t.Setenv("PATH", filepath.Dir(gofmt)+string(os.PathListSeparator)+os.Getenv("PATH"))

	dir := t.TempDir()
	writeTemp(t, dir, "a.go", "package p\nfunc  f( ) {  }\n")
	p := newPostEditor(dir, nil)
	res := tools.Result{Output: "wrote a.go", Files: []string{"a.go"}}

	p.AfterTool(context.Background(), "write_file", nil, &res)

	got, err := os.ReadFile(filepath.Join(dir, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "package p\n\nfunc f() {}\n"; string(got) != want {
		t.Fatalf("file on disk = %q, want %q", got, want)
	}
	if !strings.Contains(res.Output, "Formatted: a.go.") {
		t.Fatalf("output lacks formatted note: %q", res.Output)
	}
}

// TestPostEditDiagnosticsDiff checks that only new errors are reported, that
// warnings never are, and that an error which only moved lines is not new.
func TestPostEditDiagnosticsDiff(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "notes.txt", "hello\n") // no formatter for .txt
	e1 := errDiag(3, "undefined: foo")
	e2 := errDiag(9, "cannot use x (type int) as string")
	src := &fakeDiags{active: true, script: [][]lsp.FileDiagnostic{
		{e1, warnDiag(4, "unused")},        // first check: E1 is new, the warning is not
		{errDiag(5, "undefined: foo"), e2}, // E1 moved down a line; E2 is new
		{errDiag(5, "undefined: foo")},     // E2 fixed: nothing new
	}}
	p := newPostEditor(dir, src)
	call := func() tools.Result {
		res := tools.Result{Output: "ok", Files: []string{"notes.txt"}}
		p.AfterTool(context.Background(), "edit_file", nil, &res)
		return res
	}

	r1 := call()
	if !strings.Contains(r1.Output, "New diagnostics: 1 new error(s).") || !strings.Contains(r1.Output, "undefined: foo") {
		t.Fatalf("first edit: %q", r1.Output)
	}
	if strings.Contains(r1.Output, "unused") {
		t.Fatalf("warning reported as new: %q", r1.Output)
	}

	r2 := call()
	if !strings.Contains(r2.Output, "1 new error(s)") || !strings.Contains(r2.Output, "cannot use x") {
		t.Fatalf("second edit should report only E2: %q", r2.Output)
	}
	if strings.Contains(r2.Output, "undefined: foo") {
		t.Fatalf("moved error reported again: %q", r2.Output)
	}

	// Errors that went away are reported as fixed, and nothing is new.
	r3 := call()
	if r3.Output != "ok\n\n1 fixed since the last check." {
		t.Fatalf("third edit should report one fixed error and nothing new, got %q", r3.Output)
	}
}

// TestPostEditDuplicateErrorCountsAsNew checks that a second copy of an
// existing error is new, since the cache matches one entry per copy.
func TestPostEditDuplicateErrorCountsAsNew(t *testing.T) {
	prev := []lsp.FileDiagnostic{errDiag(1, "dup")}
	cur := []lsp.FileDiagnostic{errDiag(1, "dup"), errDiag(7, "dup")}
	got := newErrors(prev, cur)
	if len(got) != 1 || got[0].Line != 7 {
		t.Fatalf("newErrors = %+v, want only the second copy", got)
	}
}

// TestPostEditBlockCapped checks the "New diagnostics:" block stays within
// maxNewDiagLines lines.
func TestPostEditBlockCapped(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "notes.txt", "x\n")
	var many []lsp.FileDiagnostic
	for i := 1; i <= 15; i++ {
		many = append(many, errDiag(i, fmt.Sprintf("error %d", i)))
	}
	src := &fakeDiags{active: true, script: [][]lsp.FileDiagnostic{many}}
	p := newPostEditor(dir, src)
	res := tools.Result{Output: "ok", Files: []string{"notes.txt"}}
	p.AfterTool(context.Background(), "edit_file", nil, &res)

	block := res.Output[strings.Index(res.Output, "New diagnostics:"):]
	if n := strings.Count(block, "\n") + 1; n > maxNewDiagLines {
		t.Fatalf("block has %d lines, cap is %d:\n%s", n, maxNewDiagLines, block)
	}
	if !strings.Contains(block, "and 7 more") {
		t.Fatalf("missing overflow line:\n%s", block)
	}
}

// TestPostEditSkips checks the cases that must leave the result alone: an
// error result, undo_edit, no files, no language server, and a deleted file.
func TestPostEditSkips(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "notes.txt", "x\n")
	src := &fakeDiags{active: true, script: [][]lsp.FileDiagnostic{{errDiag(1, "boom")}, {errDiag(1, "boom")}}}
	p := newPostEditor(dir, src)
	ctx := context.Background()

	cases := map[string]struct {
		name string
		res  tools.Result
	}{
		"error result": {"edit_file", tools.Result{Output: "no", IsError: true, Files: []string{"notes.txt"}}},
		"undo":         {"undo_edit", tools.Result{Output: "undone", Files: []string{"notes.txt"}}},
		"no files":     {"shell", tools.Result{Output: "ran"}},
		"deleted":      {"edit_file", tools.Result{Output: "gone", Files: []string{"missing.txt"}}},
	}
	for label, c := range cases {
		res := c.res
		p.AfterTool(ctx, c.name, nil, &res)
		if res.Output != c.res.Output {
			t.Errorf("%s: output changed to %q", label, res.Output)
		}
	}
	if src.calls != 0 {
		t.Fatalf("diagnostics fetched %d times for skipped results", src.calls)
	}

	inactive := newPostEditor(dir, &fakeDiags{active: false, script: [][]lsp.FileDiagnostic{{errDiag(1, "boom")}}})
	res := tools.Result{Output: "ok", Files: []string{"notes.txt"}}
	inactive.AfterTool(ctx, "edit_file", nil, &res)
	if res.Output != "ok" {
		t.Fatalf("no server should add nothing, got %q", res.Output)
	}
}

// TestPostEditOutsideWorkdirIgnored checks that a path outside the workdir
// is never formatted or checked.
func TestPostEditOutsideWorkdirIgnored(t *testing.T) {
	dir := t.TempDir()
	outside := writeTemp(t, t.TempDir(), "x.go", "package p\nfunc  f( ) {  }\n")
	p := newPostEditor(dir, nil)
	res := tools.Result{Output: "ok", Files: []string{outside}}
	p.AfterTool(context.Background(), "write_file", json.RawMessage(`{}`), &res)
	got, _ := os.ReadFile(outside)
	if string(got) != "package p\nfunc  f( ) {  }\n" || res.Output != "ok" {
		t.Fatalf("file outside workdir was touched: %q / %q", got, res.Output)
	}
}

// TestPostEditFirstCheckOnlyNearTheEdit checks that the first check of a file
// does not blame the edit for errors far from the lines it changed.
func TestPostEditFirstCheckOnlyNearTheEdit(t *testing.T) {
	dir := t.TempDir()
	writeTemp(t, dir, "notes.txt", "hello\n")
	src := &fakeDiags{active: true, script: [][]lsp.FileDiagnostic{
		{errDiag(3, "old error far above"), errDiag(41, "error from this edit")},
	}}
	p := newPostEditor(dir, src)
	res := tools.Result{Output: "ok", Files: []string{"notes.txt"},
		Diff: "--- a/notes.txt\n+++ b/notes.txt\n@@ -40,2 +40,3 @@\n a\n+b\n c\n"}
	p.AfterTool(context.Background(), "edit_file", nil, &res)
	if !strings.Contains(res.Output, "error from this edit") || strings.Contains(res.Output, "old error far above") {
		t.Fatalf("first check should report only errors near the change: %q", res.Output)
	}
}
