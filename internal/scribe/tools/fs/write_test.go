package fs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteNewFileCreatesDirs(t *testing.T) {
	env, root, files, _ := newEnv(t)
	r := mustOK(t, call(t, NewWriteFile(nil), env, map[string]any{"path": "pkg/new/a.go", "content": "package a\n"}))
	if readFile(t, filepath.Join(root, "pkg/new/a.go")) != "package a\n" {
		t.Fatal("content")
	}
	if r.Summary != "new file, 1 lines" {
		t.Fatalf("summary %q", r.Summary)
	}
	if len(r.Files) != 1 || r.Files[0] != "pkg/new/a.go" {
		t.Fatalf("files %v", r.Files)
	}
	if !strings.Contains(r.Diff, "--- /dev/null\n+++ b/pkg/new/a.go\n@@ -0,0 +1,1 @@\n+package a") {
		t.Fatalf("diff: %q", r.Diff)
	}
	if len(files.writes) != 1 {
		t.Fatalf("BeforeWrite calls: %d", len(files.writes))
	}
}

func TestWriteExistingRequiresRead(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "old\n")
	mustErr(t, call(t, NewWriteFile(nil), env, map[string]any{"path": "a.txt", "content": "new\n"}), "has not been read")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.txt"}))
	r := mustOK(t, call(t, NewWriteFile(nil), env, map[string]any{"path": "a.txt", "content": "new\n"}))
	if r.Summary != "+1 -1" || readFile(t, filepath.Join(root, "a.txt")) != "new\n" {
		t.Fatalf("summary %q", r.Summary)
	}
}

func TestWriteKeepsCRLF(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "w.txt", "a\r\nb\r\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "w.txt"}))
	mustOK(t, call(t, NewWriteFile(nil), env, map[string]any{"path": "w.txt", "content": "a\nc\n"}))
	if got := readFile(t, abs); got != "a\r\nc\r\n" {
		t.Fatalf("CRLF not kept: %q", got)
	}
}

func TestWriteKeepsMode(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "run.sh", "#!/bin/sh\n")
	os.Chmod(abs, 0o755)
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "run.sh"}))
	mustOK(t, call(t, NewWriteFile(nil), env, map[string]any{"path": "run.sh", "content": "#!/bin/sh\necho\n"}))
	fi, _ := os.Stat(abs)
	if fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode().Perm())
	}
}

func TestEditExactUniqueAndMissingRead(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.go", "func a() {\n\treturn 1\n}\n")
	edit := map[string]any{"path": "f.go", "old_string": "return 1", "new_string": "return 2"}
	mustErr(t, call(t, NewEditFile(nil), env, edit), "has not been read")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.go"}))
	r := mustOK(t, call(t, NewEditFile(nil), env, edit))
	if got := readFile(t, abs); got != "func a() {\n\treturn 2\n}\n" {
		t.Fatalf("content %q", got)
	}
	if r.Summary != "+1 -1" || !strings.Contains(r.Diff, "-\treturn 1\n+\treturn 2") {
		t.Fatalf("diff %q summary %q", r.Diff, r.Summary)
	}
	if !strings.Contains(r.Output, "line 2") {
		t.Fatalf("output %q", r.Output)
	}
}

// The coordinator's rule: a file Scribe just wrote must not look stale to the
// next edit. Edit the same file twice in a row, with no read in between.
func TestEditTwiceInARowIsNotStale(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "t.txt", "one\ntwo\nthree\n")
	undo := NewUndoStack()
	edit := NewEditFile(undo)
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "t.txt"}))
	mustOK(t, call(t, edit, env, map[string]any{"path": "t.txt", "old_string": "one", "new_string": "ONE"}))
	mustOK(t, call(t, edit, env, map[string]any{"path": "t.txt", "old_string": "two", "new_string": "TWO"}))
	mustOK(t, call(t, NewWriteFile(undo), env, map[string]any{"path": "t.txt", "content": "x\n"}))
	mustErr(t, call(t, NewMultiEdit(undo), env, map[string]any{"path": "t.txt", "edits": []map[string]any{}}), "at least one edit")
	mustOK(t, call(t, NewWriteFile(undo), env, map[string]any{"path": "t.txt", "content": "y\n"}))
	mustOK(t, call(t, NewUndoEdit(undo), env, map[string]any{"path": "t.txt"}))
	mustOK(t, call(t, edit, env, map[string]any{"path": "t.txt", "old_string": "x", "new_string": "z"}))
	if got := readFile(t, filepath.Join(root, "t.txt")); got != "z\n" {
		t.Fatalf("after edits: %q", got)
	}
}

func TestEditStaleFileRefused(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "s.txt", "hello\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "s.txt"}))
	os.WriteFile(abs, []byte("changed elsewhere\n"), 0o644)
	bump(t, abs)
	mustErr(t, call(t, NewEditFile(nil), env, map[string]any{"path": "s.txt", "old_string": "hello", "new_string": "bye"}), "changed on disk since you read it")
}

func TestEditAmbiguousListsLines(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "d.txt", "x\nfoo\nx\nfoo\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "d.txt"}))
	mustErr(t, call(t, NewEditFile(nil), env, map[string]any{"path": "d.txt", "old_string": "foo", "new_string": "bar"}), "lines 2, 4")
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{"path": "d.txt", "old_string": "foo", "new_string": "bar", "replace_all": true}))
	if got := readFile(t, filepath.Join(root, "d.txt")); got != "x\nbar\nx\nbar\n" {
		t.Fatalf("replace_all: %q", got)
	}
}

func TestEditWhitespaceTolerantMatchReindents(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "ws.go", "func f() {\n    if x {\n        y()   \n    }\n}\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "ws.go"}))
	// Model dropped the indentation and added trailing space.
	r := mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path":       "ws.go",
		"old_string": "if x {\ny()\n}",
		"new_string": "if x {\n    z()\n}",
	}))
	if got := readFile(t, abs); got != "func f() {\n    if x {\n        z()\n    }\n}\n" {
		t.Fatalf("content %q", got)
	}
	if !strings.Contains(r.Output, "ignoring indentation") {
		t.Fatalf("output should say fuzzy match was used: %q", r.Output)
	}
}

func TestEditNotFoundShowsClosestRegions(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "n.go", "func alpha() {}\nfunc beta() {}\nfunc gamma() {}\nfunc delta() {}\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "n.go"}))
	r := call(t, NewEditFile(nil), env, map[string]any{"path": "n.go", "old_string": "func betta() {}", "new_string": "x"})
	mustErr(t, r, "Closest regions")
	if !strings.Contains(r.Output, "lines 2-2") {
		t.Fatalf("closest region should be line 2: %q", r.Output)
	}
}

func TestEditCRLFPreserved(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "c.txt", "a\r\nb\r\nc\r\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "c.txt"}))
	// The model sends LF newlines; the file must stay CRLF.
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{"path": "c.txt", "old_string": "a\nb", "new_string": "a\nB\nB2"}))
	if got := readFile(t, abs); got != "a\r\nB\r\nB2\r\nc\r\n" {
		t.Fatalf("CRLF lost: %q", got)
	}
}

func TestEditCreatesNewFileWithEmptyOld(t *testing.T) {
	env, root, _, _ := newEnv(t)
	r := mustOK(t, call(t, NewEditFile(nil), env, map[string]any{"path": "new/x.txt", "old_string": "", "new_string": "hi\n"}))
	if readFile(t, filepath.Join(root, "new/x.txt")) != "hi\n" || r.Summary != "new file, 1 lines" {
		t.Fatalf("create: %q %q", r.Summary, r.Output)
	}
	mustErr(t, call(t, NewEditFile(nil), env, map[string]any{"path": "missing.txt", "old_string": "a", "new_string": "b"}), "File not found")
	mustErr(t, call(t, NewEditFile(nil), env, map[string]any{"path": "new/x.txt", "old_string": "", "new_string": "again"}), "old_string must not be empty")
}

func TestMultiEditAppliesInOrder(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "m.txt", "alpha beta gamma\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "m.txt"}))
	r := mustOK(t, call(t, NewMultiEdit(nil), env, map[string]any{"path": "m.txt", "edits": []map[string]any{
		{"old_string": "alpha", "new_string": "ALPHA"},
		{"old_string": "ALPHA beta", "new_string": "ONE"},
	}}))
	if got := readFile(t, abs); got != "ONE gamma\n" {
		t.Fatalf("content %q", got)
	}
	if r.Summary != "+1 -1" {
		t.Fatalf("summary %q", r.Summary)
	}
}

func TestMultiEditIsAtomic(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "a.txt", "one two three\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.txt"}))
	r := call(t, NewMultiEdit(nil), env, map[string]any{"path": "a.txt", "edits": []map[string]any{
		{"old_string": "one", "new_string": "1"},
		{"old_string": "absent", "new_string": "x"},
	}})
	mustErr(t, r, "NO edits were applied")
	if got := readFile(t, abs); got != "one two three\n" {
		t.Fatalf("file changed despite failure: %q", got)
	}
}

func TestMultiEditEditsAsJSONString(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "j.txt", "a b\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "j.txt"}))
	mustOK(t, call(t, NewMultiEdit(nil), env, map[string]any{"path": "j.txt",
		"edits": `[{"old_string":"a","new_string":"A"}]`}))
	if readFile(t, abs) != "A b\n" {
		t.Fatal("JSON-string edits not applied")
	}
}

func TestUndoRestoresPreviousState(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "u.txt", "v1\n")
	undo := NewUndoStack()
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "u.txt"}))
	mustOK(t, call(t, NewEditFile(undo), env, map[string]any{"path": "u.txt", "old_string": "v1", "new_string": "v2"}))
	mustOK(t, call(t, NewEditFile(undo), env, map[string]any{"path": "u.txt", "old_string": "v2", "new_string": "v3"}))
	r := mustOK(t, call(t, NewUndoEdit(undo), env, map[string]any{"path": "u.txt"}))
	if readFile(t, abs) != "v2\n" || !strings.Contains(r.Output, "1 earlier step") {
		t.Fatalf("after first undo: %q %q", readFile(t, abs), r.Output)
	}
	mustOK(t, call(t, NewUndoEdit(undo), env, map[string]any{"path": "u.txt"}))
	if readFile(t, abs) != "v1\n" {
		t.Fatalf("after second undo: %q", readFile(t, abs))
	}
	mustErr(t, call(t, NewUndoEdit(undo), env, map[string]any{"path": "u.txt"}), "Nothing to undo")
}

func TestUndoRefusesWhenFileChanged(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "u.txt", "a\n")
	undo := NewUndoStack()
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "u.txt"}))
	mustOK(t, call(t, NewEditFile(undo), env, map[string]any{"path": "u.txt", "old_string": "a", "new_string": "b"}))
	os.WriteFile(abs, []byte("someone else\n"), 0o644)
	bump(t, abs)
	mustErr(t, call(t, NewUndoEdit(undo), env, map[string]any{"path": "u.txt"}), "undo was refused")
	if readFile(t, abs) != "someone else\n" {
		t.Fatal("undo overwrote external change")
	}
}

func TestUndoRemovesCreatedFile(t *testing.T) {
	env, root, _, _ := newEnv(t)
	undo := NewUndoStack()
	mustOK(t, call(t, NewWriteFile(undo), env, map[string]any{"path": "made.txt", "content": "hi\n"}))
	mustOK(t, call(t, NewUndoEdit(undo), env, map[string]any{"path": "made.txt"}))
	if _, err := os.Stat(filepath.Join(root, "made.txt")); !os.IsNotExist(err) {
		t.Fatal("created file still exists after undo")
	}
}

func TestUndoStackBoundedPerFile(t *testing.T) {
	s := NewUndoStack()
	for i := 0; i < 25; i++ {
		s.push("/x", undoEntry{afterHash: "h"})
	}
	if d := s.depth("/x"); d != undoPerFile {
		t.Fatalf("depth %d, want %d", d, undoPerFile)
	}
}

func TestUnifiedDiffCorrectness(t *testing.T) {
	old := "a\nb\nc\nd\ne\nf\ng\nh\ni\nj\n"
	nw := "a\nb\nc\nD\ne\nf\ng\nh\ni\nj\nk\n"
	diff, added, removed := UnifiedDiff("x.txt", old, nw)
	if added != 2 || removed != 1 {
		t.Fatalf("counts +%d -%d", added, removed)
	}
	want := "--- a/x.txt\n+++ b/x.txt\n@@ -1,10 +1,11 @@\n a\n b\n c\n-d\n+D\n e\n f\n g\n h\n i\n j\n+k\n"
	if diff != want {
		t.Fatalf("diff:\n%s\nwant:\n%s", diff, want)
	}
}

func TestUnifiedDiffSeparatesDistantHunks(t *testing.T) {
	var a, b []string
	for i := 0; i < 30; i++ {
		a = append(a, fmt.Sprintf("line %d", i))
		b = append(b, fmt.Sprintf("line %d", i))
	}
	b[2] = "changed"
	b[27] = "changed"
	diff, _, _ := UnifiedDiff("f", strings.Join(a, "\n")+"\n", strings.Join(b, "\n")+"\n")
	if strings.Count(diff, "\n@@ -") != 2 { // two hunks
		t.Fatalf("expected two hunks:\n%s", diff)
	}
}

func TestUnifiedDiffNoChange(t *testing.T) {
	d, a, r := UnifiedDiff("f", "same\n", "same\n")
	if d != "" || a != 0 || r != 0 {
		t.Fatalf("expected empty diff, got %q", d)
	}
}

func TestUnifiedDiffFallbackAboveLimit(t *testing.T) {
	var a, b []string
	for i := 0; i < maxDiffD+500; i++ {
		a = append(a, "old")
		b = append(b, "new")
	}
	_, added, removed := UnifiedDiff("f", strings.Join(a, "\n"), strings.Join(b, "\n"))
	if added != len(b) || removed != len(a) {
		t.Fatalf("counts +%d -%d", added, removed)
	}
}
