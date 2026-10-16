package fs

import (
	"strings"
	"testing"
)

func TestEditLineStartPicksOneOfIdenticalMatches(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", "foo()\n\nfoo()\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))

	mustErr(t, call(t, NewEditFile(nil), env, map[string]any{"path": "f.txt", "old_string": "foo()", "new_string": "bar()"}), "matches 2 places")
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "f.txt", "old_string": "foo()", "new_string": "bar()", "lineStart": 3,
	}))
	if got := readFile(t, abs); got != "foo()\n\nbar()\n" {
		t.Fatalf("lineStart=3 changed the wrong match: %q", got)
	}
}

func TestEditLineRangeRejectsMatchOutsideWindow(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", "a\nb\nfoo()\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	r := call(t, NewEditFile(nil), env, map[string]any{
		"path": "f.txt", "old_string": "foo()", "new_string": "bar()", "lineStart": 1, "lineEnd": 2,
	})
	mustErr(t, r, "outside lines 1-2")
	if readFile(t, abs) != "a\nb\nfoo()\n" {
		t.Fatal("an out-of-range match must not be applied")
	}
}

func TestEditLineStartValidation(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "f.txt", "a\nb\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	edit := NewEditFile(nil)
	mustErr(t, call(t, edit, env, map[string]any{"path": "f.txt", "old_string": "a", "new_string": "A", "lineStart": 9}), "Invalid line range: lineStart 9")
	mustErr(t, call(t, edit, env, map[string]any{"path": "f.txt", "old_string": "a", "new_string": "A", "lineStart": 2, "lineEnd": 1}), "is before lineStart")
	mustErr(t, call(t, edit, env, map[string]any{"path": "f.txt", "old_string": "a", "new_string": "A", "lineStart": -1}), "count lines from 1")
}

func TestEditLineStartAcceptsNumericString(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", "x\nx\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "f.txt", "old_string": "x", "new_string": "y", "lineStart": "2",
	}))
	if readFile(t, abs) != "x\ny\n" {
		t.Fatalf("numeric-string lineStart: %q", readFile(t, abs))
	}
}

func TestEditDefaultLineEndSpansOldString(t *testing.T) {
	env, root, _, _ := newEnv(t)
	// Two-line old_string "p\nq" appears at lines 2-3 and 6-7. With lineStart 6
	// and no lineEnd the window is 6 to 7, so only the second copy matches.
	abs := write(t, root, "f.txt", "top\np\nq\nmid\nend\np\nq\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "f.txt", "old_string": "p\nq", "new_string": "P\nQ", "lineStart": 6,
	}))
	if readFile(t, abs) != "top\np\nq\nmid\nend\nP\nQ\n" {
		t.Fatalf("default lineEnd: %q", readFile(t, abs))
	}
}

func TestEditLineRangeReplaceAllStaysInWindow(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", "x\nx\nx\nx\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "f.txt", "old_string": "x", "new_string": "y", "replace_all": true, "lineStart": 2, "lineEnd": 3,
	}))
	if readFile(t, abs) != "x\ny\ny\nx\n" {
		t.Fatalf("replace_all inside a window: %q", readFile(t, abs))
	}
}

func TestEditIfHashAcceptsMatchingText(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", "one\ntwo\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "f.txt", "old_string": "two", "new_string": "2", "ifHash": shortHash("two"),
	}))
	if readFile(t, abs) != "one\n2\n" {
		t.Fatalf("matching ifHash: %q", readFile(t, abs))
	}
}

func TestEditIfHashStaleIsRejectedAndAskedToReread(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", "one\ntwo\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	r := call(t, NewEditFile(nil), env, map[string]any{
		"path": "f.txt", "old_string": "two", "new_string": "2", "ifHash": shortHash("something older"),
	})
	mustErr(t, r, "ifHash does not match")
	mustErr(t, r, "Re-read lines 2-2")
	if readFile(t, abs) != "one\ntwo\n" {
		t.Fatal("a stale ifHash must not apply the edit")
	}
}

// driftFile is a Go-like file indented with four spaces. The old_string below
// has its first line unindented, so the exact match fails and the
// indentation-tolerant fallback has to find it.
const driftFile = "func a() {\n    if x {\n        return 1\n    }\n}\n"

func TestEditIndentationDriftMatchesAndReindents(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", driftFile)
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	r := mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "f.txt", "old_string": "if x {\n    return 1\n}", "new_string": "if x {\n    return 2\n}",
	}))
	if !strings.Contains(r.Output, "ignoring indentation") {
		t.Fatalf("the fuzzy path should say it matched ignoring indentation: %q", r.Output)
	}
	want := "func a() {\n    if x {\n        return 2\n    }\n}\n"
	if got := readFile(t, abs); got != want {
		t.Fatalf("drifted match:\n%q\nwant\n%q", got, want)
	}
}

func TestEditIndentationDriftWithLineStart(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", driftFile)
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "f.txt", "old_string": "if x {\n    return 1\n}", "new_string": "if x {\n    return 2\n}", "lineStart": 2,
	}))
	if got := readFile(t, abs); !strings.Contains(got, "        return 2\n") {
		t.Fatalf("windowed drifted match: %q", got)
	}
}

func TestEditIfHashCoversTheTextOnDisk(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", driftFile)
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))

	// The hash of the drifted old_string is not the hash of the text on disk,
	// so it is refused. The hash of the on-disk lines is accepted.
	drifted := "if x {\n    return 1\n}"
	mustErr(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "f.txt", "old_string": drifted, "new_string": "if x {\n    return 2\n}", "ifHash": shortHash(drifted),
	}), "ifHash does not match")

	onDisk := "    if x {\n        return 1\n    }"
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "f.txt", "old_string": drifted, "new_string": "if x {\n    return 2\n}", "ifHash": shortHash(onDisk),
	}))
	if got := readFile(t, abs); !strings.Contains(got, "        return 2\n") {
		t.Fatalf("ifHash of on-disk text: %q", got)
	}
}

func TestMultiEditLineStartSeesEarlierEdits(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", "a\nb\nc\nb\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	// The first edit adds a line, so the second edit's lineStart 3 is the
	// line that now holds "b" (the file as it stands when that edit runs).
	mustOK(t, call(t, NewMultiEdit(nil), env, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"old_string": "a", "new_string": "a\na2", "lineStart": 1},
			{"old_string": "b", "new_string": "B", "lineStart": 3},
		},
	}))
	if got := readFile(t, abs); got != "a\na2\nB\nc\nb\n" {
		t.Fatalf("sequential lineStart: %q", got)
	}
}

func TestMultiEditIfHashMismatchIsAtomic(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", "a\nb\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	r := call(t, NewMultiEdit(nil), env, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"old_string": "a", "new_string": "A"},
			{"old_string": "b", "new_string": "B", "ifHash": "000000000000"},
		},
	})
	mustErr(t, r, "ifHash does not match")
	mustErr(t, r, "NO edits were applied")
	if readFile(t, abs) != "a\nb\n" {
		t.Fatalf("failed batch wrote something: %q", readFile(t, abs))
	}
}

func TestMultiEditLineRangeOutsideFailsBatch(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "f.txt", "a\nb\nc\n")
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "f.txt"}))
	r := call(t, NewMultiEdit(nil), env, map[string]any{
		"path": "f.txt",
		"edits": []map[string]any{
			{"old_string": "a", "new_string": "A", "lineStart": 1},
			{"old_string": "c", "new_string": "C", "lineStart": 1, "lineEnd": 2},
		},
	})
	mustErr(t, r, "outside lines 1-2")
	if readFile(t, abs) != "a\nb\nc\n" {
		t.Fatalf("atomic rollback broken: %q", readFile(t, abs))
	}
}

func TestEditSpecsAdvertiseTheNewOptions(t *testing.T) {
	editSpec := NewEditFile(nil).Spec()
	multiSpec := NewMultiEdit(nil).Spec()
	for _, s := range []struct {
		name, desc, schema string
	}{
		{"edit_file", editSpec.Description, string(editSpec.Schema)},
		{"multi_edit", multiSpec.Description, string(multiSpec.Schema)},
	} {
		for _, k := range []string{"lineStart", "lineEnd", "ifHash"} {
			if !strings.Contains(s.schema, `"`+k+`"`) {
				t.Errorf("%s schema lacks %s", s.name, k)
			}
		}
		if !strings.Contains(s.desc, "lineStart") || !strings.Contains(s.desc, "ifHash") {
			t.Errorf("%s description does not mention the options: %q", s.name, s.desc)
		}
	}
	readSpec := NewRead().Spec()
	for _, k := range []string{"lines", "symbol", "force"} {
		if !strings.Contains(string(readSpec.Schema), `"`+k+`"`) {
			t.Errorf("read schema lacks %s", k)
		}
	}
	if !strings.Contains(readSpec.Description, "symbol=") || !strings.Contains(readSpec.Description, "unchanged") {
		t.Errorf("read description lacks the new options: %q", readSpec.Description)
	}
}
