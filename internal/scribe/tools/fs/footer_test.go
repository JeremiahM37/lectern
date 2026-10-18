package fs

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fakeMemory answers FileHint from a map, the way the memory store will.
type fakeMemory struct{ hints map[string]string }

func (f fakeMemory) FileHint(rel string) string { return f.hints[rel] }

func withMemory(env *tools.Env, hints map[string]string) *tools.Env {
	env.Services = map[string]any{"memory": fakeMemory{hints: hints}}
	return env
}

// A read result ends with the hash line, and a memory hint, when there is
// one, goes on the line after it.
func TestReadFooterCarriesMemoryHintAfterHash(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.go", "package a\n")
	env = withMemory(env, map[string]string{"a.go": "gotcha: generated, edit the template"})

	out := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.go", "lines": "1-1"})).Output
	idx := strings.LastIndex(out, "\nhash:")
	if idx < 0 {
		t.Fatalf("no hash line: %q", out)
	}
	tail := out[idx+1:]
	lines := strings.Split(tail, "\n")
	if len(lines) != 2 || !strings.HasPrefix(lines[0], "hash:") || lines[1] != "gotcha: generated, edit the template" {
		t.Fatalf("footer is %q, want the hash line then the hint", tail)
	}
}

// A plain full read has no hash line, as before. The hint goes at its end.
func TestPlainReadFooterIsTheHintOnly(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.go", "package a\n")
	env = withMemory(env, map[string]string{"a.go": "gotcha: generated"})
	out := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.go"})).Output
	if strings.Contains(out, "hash:") {
		t.Fatalf("a plain read should carry no hash line: %q", out)
	}
	if !strings.HasSuffix(out, "\ngotcha: generated") {
		t.Fatalf("plain read has no hint at its end: %q", out)
	}
}

// With no store published, a plain read is unchanged: no footer at all.
func TestReadWithoutMemoryHasNoFooter(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.go", "package a\n")
	out := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.go"})).Output
	if out != "     1  package a" {
		t.Fatalf("read without memory changed: %q", out)
	}
}

// An unchanged read returns no body and no footer, as spec 04 asks.
func TestUnchangedReadHasNoMemoryFooter(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.go", "package a\n")
	env = withMemory(env, map[string]string{"a.go": "gotcha"})
	first := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.go", "lines": "1-1"}))
	hash, _, _ := strings.Cut(first.Output[strings.LastIndex(first.Output, "hash:")+len("hash:"):], "\n")
	second := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.go", "lines": "1-1", "ifHash": hash}))
	if !strings.HasPrefix(second.Output, "unchanged:") || strings.Contains(second.Output, "gotcha") {
		t.Fatalf("an unchanged read should be the bare notice: %q", second.Output)
	}
}

// An edit result gets the hint for the file it changed.
func TestEditFooterCarriesMemoryHint(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.go", "one\ntwo\n")
	env = withMemory(env, map[string]string{"a.go": "gotcha: two is pinned by a test"})
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.go"}))
	res := mustOK(t, call(t, NewEditFile(nil), env, map[string]any{"path": "a.go", "old_string": "one", "new_string": "1"}))
	if !strings.HasPrefix(res.Output, "Edited a.go:") {
		t.Fatalf("edit message changed: %q", res.Output)
	}
	if !strings.HasSuffix(res.Output, "\ngotcha: two is pinned by a test") {
		t.Fatalf("edit result has no footer: %q", res.Output)
	}
}

// Another file's hint never lands on this one's result.
func TestEditFooterIsForTheEditedFileOnly(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.go", "one\n")
	env = withMemory(env, map[string]string{"b.go": "gotcha about b"})
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.go"}))
	res := mustOK(t, call(t, NewEditFile(nil), env, map[string]any{"path": "a.go", "old_string": "one", "new_string": "1"}))
	if strings.Contains(res.Output, "gotcha") {
		t.Fatalf("hint for b.go leaked into an edit of a.go: %q", res.Output)
	}
}

// A hint is one line, with its whitespace collapsed and its length capped,
// because every result pays for it.
func TestFileHintIsOneShortLine(t *testing.T) {
	env, _, _, _ := newEnv(t)
	long := "line one\n\n   line   two " + strings.Repeat("x", 500)
	env = withMemory(env, map[string]string{"f.go": long})
	got := memoryFooter(env, "f.go")
	if strings.Contains(got[1:], "\n") {
		t.Fatalf("hint spans lines: %q", got)
	}
	if !strings.HasPrefix(got, "\nline one line two ") {
		t.Fatalf("whitespace not collapsed: %q", got)
	}
	if n := len([]rune(got)) - 1; n > maxFileHint+1 {
		t.Fatalf("hint is %d runes, want at most %d", n, maxFileHint+1)
	}
}

// An empty hint, or a store that is not a FileMemory, adds nothing.
func TestNoHintNoFooter(t *testing.T) {
	env, _, _, _ := newEnv(t)
	if got := memoryFooter(env, "f.go"); got != "" {
		t.Fatalf("no store gave %q", got)
	}
	env = withMemory(env, map[string]string{})
	if got := memoryFooter(env, "f.go"); got != "" {
		t.Fatalf("an empty hint gave %q", got)
	}
	env.Services["memory"] = "not a store"
	if got := memoryFooter(env, "f.go"); got != "" {
		t.Fatalf("a non-store gave %q", got)
	}
}
