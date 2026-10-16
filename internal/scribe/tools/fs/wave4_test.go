package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// isolateHome points the global config home at a temp dir so a developer's
// real forbidden.json never leaks into the tests.
func isolateHome(t *testing.T) {
	t.Helper()
	t.Setenv("SCRIBE_HOME", t.TempDir())
}

func TestForbiddenProjectFileBlocksReadWithTag(t *testing.T) {
	isolateHome(t)
	env, root, _, _ := newEnv(t)
	write(t, root, ".scribe/forbidden.json", `{"patterns": ["*.log", "secrets/**"]}`)
	write(t, root, "app.log", "x\n")
	write(t, root, "secrets/deep/key.txt", "x\n")
	write(t, root, "main.go", "package main\n")

	for _, p := range []string{"app.log", "secrets/deep/key.txt"} {
		r := call(t, NewRead(), env, map[string]any{"path": p})
		if !r.IsError || r.Tag != "forbidden" {
			t.Fatalf("read %s: want forbidden error, got %+v", p, r)
		}
		if !strings.Contains(r.Output, "Access denied") {
			t.Fatalf("read %s: message %q", p, r.Output)
		}
	}
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "main.go"}))
}

func TestForbiddenAppliesToWritesAndSearch(t *testing.T) {
	isolateHome(t)
	env, root, _, _ := newEnv(t)
	write(t, root, ".scribe/forbidden.json", `{"patterns": ["*.env"]}`)
	write(t, root, "prod.env", "TOKEN=1\n")
	r := call(t, NewWriteFile(nil), env, map[string]any{"path": "prod.env", "content": "x"})
	if r.Tag != "forbidden" {
		t.Fatalf("write: want forbidden tag, got %+v", r)
	}
	r = call(t, NewGrep(), env, map[string]any{"pattern": "TOKEN", "path": "prod.env"})
	if !r.IsError || r.Tag != "forbidden" {
		t.Fatalf("grep on forbidden file: %+v", r)
	}
}

func TestForbiddenGlobfulSessionPattern(t *testing.T) {
	isolateHome(t)
	env, root, _, _ := newEnv(t)
	env.Services = map[string]any{"forbidden": []string{"notes/*.md"}}
	write(t, root, "notes/todo.md", "x\n")
	write(t, root, "notes/deep/todo.md", "x\n") // * does not cross directories
	r := call(t, NewRead(), env, map[string]any{"path": "notes/todo.md"})
	if r.Tag != "forbidden" {
		t.Fatalf("session pattern not applied: %+v", r)
	}
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "notes/deep/todo.md"}))
}

func TestForbiddenMalformedFileFailsClosed(t *testing.T) {
	isolateHome(t)
	env, root, _, _ := newEnv(t)
	write(t, root, ".scribe/forbidden.json", `{"patterns": [`)
	write(t, root, "a.txt", "x\n")
	r := call(t, NewRead(), env, map[string]any{"path": "a.txt"})
	if !r.IsError || !strings.Contains(r.Output, "not valid JSON") {
		t.Fatalf("malformed forbidden file must refuse access: %+v", r)
	}
}

func TestGlobToRegexpMatching(t *testing.T) {
	cases := []struct {
		pat, path string
		want      bool
	}{
		{"*.log", "app.log", true},
		{"*.log", "dir/app.log", true},
		{"*.log", "app.logx", false},
		{"**/key.txt", "key.txt", true},
		{"**/key.txt", "a/b/key.txt", true},
		{"**/key.txt", "a/b/xkey.txt", false},
		{"secrets/**", "secrets/a/b", true},
		{"a?c", "abc", true},
		{"a?c", "a/c", false},
		{"A.TXT", "a.txt", true}, // case-insensitive
	}
	for _, c := range cases {
		rules, err := compileForbidden([]string{c.pat})
		if err != nil {
			t.Fatal(err)
		}
		got := matchForbidden(rules, c.path) != nil
		if got != c.want {
			t.Errorf("pattern %q vs %q: got %v want %v", c.pat, c.path, got, c.want)
		}
	}
}

func TestUndoStepsRevertsSeveralEdits(t *testing.T) {
	env, root, _, _ := newEnv(t)
	undo := NewUndoStack()
	abs := write(t, root, "u.txt", "one\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "u.txt"}))
	edit := NewEditFile(undo)
	mustOK(t, call(t, edit, env, map[string]any{"path": "u.txt", "old_string": "one", "new_string": "two"}))
	mustOK(t, call(t, edit, env, map[string]any{"path": "u.txt", "old_string": "two", "new_string": "three"}))
	mustOK(t, call(t, edit, env, map[string]any{"path": "u.txt", "old_string": "three", "new_string": "four"}))

	ud := NewUndoEdit(undo)
	r := mustOK(t, call(t, ud, env, map[string]any{"path": "u.txt", "steps": 2}))
	if got := readFile(t, abs); got != "two\n" {
		t.Fatalf("after 2 steps: %q", got)
	}
	if !strings.Contains(r.Output, "last 2 edits") || !strings.Contains(r.Output, "1 earlier step") {
		t.Fatalf("output %q", r.Output)
	}
	// Asking for more than remain undoes what is left.
	mustOK(t, call(t, ud, env, map[string]any{"path": "u.txt", "steps": "20"}))
	if got := readFile(t, abs); got != "one\n" {
		t.Fatalf("after clamped undo: %q", got)
	}
	mustErr(t, call(t, ud, env, map[string]any{"path": "u.txt"}), "Nothing to undo")
}

func TestUndoStaleNewestEntryTagged(t *testing.T) {
	env, root, _, _ := newEnv(t)
	undo := NewUndoStack()
	abs := write(t, root, "s.txt", "a\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "s.txt"}))
	mustOK(t, call(t, NewEditFile(undo), env, map[string]any{"path": "s.txt", "old_string": "a", "new_string": "b"}))
	if err := os.WriteFile(abs, []byte("someone else\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := call(t, NewUndoEdit(undo), env, map[string]any{"path": "s.txt", "steps": 1})
	if !r.IsError || r.Tag != "stale_undo" {
		t.Fatalf("want stale_undo, got %+v", r)
	}
	if readFile(t, abs) != "someone else\n" {
		t.Fatal("stale undo must not write")
	}
}

func TestEditRefusesLargeEmptyDelete(t *testing.T) {
	env, root, _, _ := newEnv(t)
	var lines []string
	for i := 1; i <= 12; i++ {
		lines = append(lines, fmt.Sprintf("line %d", i))
	}
	write(t, root, "d.txt", strings.Join(lines, "\n")+"\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "d.txt"}))
	r := call(t, NewEditFile(nil), env, map[string]any{
		"path": "d.txt", "old_string": strings.Join(lines, "\n"), "new_string": "",
	})
	mustErr(t, r, "Refusing to delete 12 lines with empty replacement.")

	// Ten lines is still allowed.
	ten := strings.Join(lines[:10], "\n")
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{"path": "d.txt", "old_string": ten, "new_string": ""}))
}

func TestEditConcurrentModificationTag(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "c.txt", "alpha\nbeta\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "c.txt"}))

	r := call(t, NewEditFile(nil), env, map[string]any{
		"path": "c.txt", "old_string": "alpha", "new_string": "gamma", "ifHash": "000000000000",
	})
	if !r.IsError || r.Tag != "concurrent modification" {
		t.Fatalf("ifHash mismatch: %+v", r)
	}

	write(t, root, "c.txt", "changed elsewhere\n")
	bump(t, abs)
	r = call(t, NewEditFile(nil), env, map[string]any{"path": "c.txt", "old_string": "changed", "new_string": "x"})
	if !r.IsError || r.Tag != "concurrent modification" {
		t.Fatalf("stale since read: %+v", r)
	}
}

func TestNotFoundTag(t *testing.T) {
	env, _, _, _ := newEnv(t)
	r := call(t, NewRead(), env, map[string]any{"path": "missing.txt"})
	if !r.IsError || r.Tag != "not found" {
		t.Fatalf("want not found tag: %+v", r)
	}
}

func TestFuzzyMatchBackslashTiers(t *testing.T) {
	env, root, _, _ := newEnv(t)
	// The file holds regex escapes; the model copied them without the
	// backslashes (tier 2).
	write(t, root, "re.txt", "start\n    match \\.foo\\(bar\\)\nend\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "re.txt"}))
	r := mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "re.txt", "old_string": "match .foo(bar)", "new_string": "match .baz()",
	}))
	if !strings.Contains(r.Output, "backslash escapes") {
		t.Fatalf("tier note missing: %q", r.Output)
	}
	if got := readFile(t, filepath.Join(root, "re.txt")); !strings.Contains(got, "match .baz()") {
		t.Fatalf("edit not applied: %q", got)
	}

	// A quoted string escaped with backslashes (tier 3).
	write(t, root, "q.txt", "say \\\"hi\\\"\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "q.txt"}))
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "q.txt", "old_string": `say "hi"`, "new_string": `say "bye"`,
	}))
	if got := readFile(t, filepath.Join(root, "q.txt")); got != "say \"bye\"\n" {
		t.Fatalf("tier 3 edit: %q", got)
	}
}

func TestBackslashHelpers(t *testing.T) {
	if got := collapseBackslashes(`a\\\\b`); got != `a\b` {
		t.Errorf("collapse: %q", got)
	}
	if got := unescapeMeta(`\.x\(y\)`); got != `.x(y)` {
		t.Errorf("unescapeMeta: %q", got)
	}
	if got := unescapeMeta(`\d+`); got != `\d+` {
		t.Errorf("unescapeMeta must keep \\d: %q", got)
	}
	if got := dropOddEscapes(`\"q\" \n \d`); got != `"q" \n \d` {
		t.Errorf("dropOddEscapes: %q", got)
	}
}

func TestListDirDepthClampAndPaths(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a/b/c/d/e/f/g.txt", "x\n")
	write(t, root, "x/one.txt", "x\n")

	// Depth counts levels below the listed directory: f/ is level 5 and
	// g.txt would be level 6.
	r := mustOK(t, call(t, NewListDir(), env, map[string]any{"path": "a", "depth": 9}))
	if !strings.Contains(r.Output, "f/") {
		t.Fatalf("depth 5 should show f/: %s", r.Output)
	}
	if strings.Contains(r.Output, "g.txt") {
		t.Fatalf("depth must clamp to 5, but g.txt (level 6) was listed: %s", r.Output)
	}

	r = mustOK(t, call(t, NewListDir(), env, map[string]any{"paths": []string{"x", "missing"}}))
	if !strings.Contains(r.Output, "── x ──") || !strings.Contains(r.Output, "one.txt") {
		t.Fatalf("paths output: %s", r.Output)
	}
	if !strings.Contains(r.Output, "── missing ──\nerror:") {
		t.Fatalf("missing path should show its own error: %s", r.Output)
	}
	mustErr(t, call(t, NewListDir(), env, map[string]any{"paths": []string{"missing", "also-missing"}}), "error:")
}

func TestListDirSharesEntryBudget(t *testing.T) {
	env, root, _, _ := newEnv(t)
	for i := 0; i < 300; i++ {
		write(t, root, fmt.Sprintf("big/f%03d.txt", i), "x")
		write(t, root, fmt.Sprintf("more/f%03d.txt", i), "x")
	}
	r := mustOK(t, call(t, NewListDir(), env, map[string]any{"paths": []string{"big", "more"}}))
	// The first directory spends 300 of the 500 entries; the second gets
	// the 200 left and says so.
	if !strings.Contains(r.Output, "Output capped at 200 entries") {
		t.Fatalf("second directory should hit the shared cap: tail=%q", tail(r.Output))
	}
}

func tail(s string) string {
	if len(s) > 200 {
		return s[len(s)-200:]
	}
	return s
}

func TestGrepCapsMatchesPerFile(t *testing.T) {
	env, root, _, _ := newEnv(t)
	var sb strings.Builder
	for i := 0; i < 80; i++ {
		fmt.Fprintf(&sb, "hit %d\n", i)
	}
	write(t, root, "many.txt", sb.String())
	for name, tool := range map[string]tools.Tool{"go": newGrep(true), "rg": NewGrep()} {
		r := call(t, tool, env, map[string]any{"pattern": "hit", "max_results": 1000})
		if r.IsError {
			t.Fatalf("%s: %s", name, r.Output)
		}
		if n := strings.Count(r.Output, "hit "); n != maxGrepPerFile {
			t.Errorf("%s: want %d matches, got %d", name, maxGrepPerFile, n)
		}
	}
}

func TestReadRefusesOversizeImage(t *testing.T) {
	env, root, _, _ := newEnv(t)
	big := make([]byte, maxImageBytes+1)
	if err := os.WriteFile(filepath.Join(root, "huge.png"), big, 0o644); err != nil {
		t.Fatal(err)
	}
	r := call(t, NewRead(), env, map[string]any{"path": "huge.png"})
	if !r.IsError || !strings.Contains(r.Output, "cannot be attached") {
		t.Fatalf("oversize image: %+v", r)
	}
}
