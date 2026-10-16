package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/lsp"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestSymbolDeltasCountsDefinitionsFromDiff(t *testing.T) {
	diff := "--- a/x.ts\n+++ b/x.ts\n@@ -1,3 +1,4 @@\n+import { a } from \"./a\";\n export function keep() {}\n-export function gone() {}\n+export function added() {}\n+export const k = 1;\n"
	got := symbolDeltas(diff)
	want := "Definitions changed (from the diff): imports +1, exports +1."
	if got != want {
		t.Fatalf("symbolDeltas = %q, want %q", got, want)
	}
	if symbolDeltas("+x = 1\n") != "" {
		t.Fatal("a plain assignment is not a definition")
	}
}

func TestFormatNewDiagnosticsShowsHintsAndFixed(t *testing.T) {
	p := newPostEditor(t.TempDir(), nil)
	ds := []lsp.FileDiagnostic{{Path: p.workdir + "/a.go", Line: 3, Column: 2, Severity: "error", Message: "undefined: foo", Source: "compiler"}}
	out := p.formatNewDiagnostics(ds, [][]string{{"Add import \"foo\""}}, 2)
	for _, want := range []string{"New diagnostics: 1 new error(s). 2 fixed since the last check.", "a.go:3:2 error: undefined: foo", `fix: Add import "foo"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if fixedOnly := p.formatNewDiagnostics(nil, nil, 1); fixedOnly != "1 fixed since the last check." {
		t.Fatalf("fixed-only block = %q", fixedOnly)
	}
}

// fakeFixer is a diagSource that also runs source fixes and quick fixes.
type fakeFixer struct {
	fakeDiags
	fixed   string // content the source fix writes to the file
	fixes   int
	actions []lsp.CodeAction
}

func (f *fakeFixer) SourceFix(_ context.Context, abs string) ([]string, error) {
	f.fixes++
	if f.fixed != "" {
		if err := os.WriteFile(abs, []byte(f.fixed), 0o644); err != nil {
			return nil, err
		}
	}
	return []string{abs}, nil
}

func (f *fakeFixer) QuickFixes(context.Context, string, lsp.FileDiagnostic) ([]lsp.CodeAction, error) {
	return f.actions, nil
}

func TestAutoFixRunsAfterCompoundToolOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.go")
	if err := os.WriteFile(path, []byte("package a\n\nimport \"os\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := &fakeFixer{fakeDiags: fakeDiags{active: true}, fixed: "package a\n"}
	p := newPostEditor(dir, src)

	// An ordinary edit does not trigger source fixes.
	res := tools.Result{Output: "ok", Files: []string{"a.go"}}
	p.AfterTool(context.Background(), "edit_file", nil, &res)
	if src.fixes != 0 {
		t.Fatalf("edit_file ran source fixes %d times", src.fixes)
	}

	res = tools.Result{Output: "moved", Files: []string{"a.go"}, Diff: ""}
	p.AfterTool(context.Background(), "move_symbol", nil, &res)
	if src.fixes != 1 {
		t.Fatalf("move_symbol should run source fixes once, ran %d", src.fixes)
	}
	if !strings.Contains(res.Output, "Auto-fixed: organized imports and applied fixes in a.go.") {
		t.Fatalf("auto-fix not reported: %q", res.Output)
	}
	if !strings.Contains(res.Diff, "import \"os\"") {
		t.Fatalf("auto-fix diff missing the removed import:\n%s", res.Diff)
	}
}

func TestAutoFixSkipsWhenServerNotRunning(t *testing.T) {
	dir := t.TempDir()
	src := &fakeFixer{fakeDiags: fakeDiags{active: false}}
	p := newPostEditor(dir, src)
	res := tools.Result{Output: "moved", Files: []string{"a.go"}}
	p.AfterTool(context.Background(), "rename_symbol", nil, &res)
	if src.fixes != 0 {
		t.Fatalf("source fixes started a server (%d runs); they must only use a running one", src.fixes)
	}
}
