package fs

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestReadUnchangedReReadIsShortNotice(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "one\ntwo\nthree\n")
	r := NewRead()
	first := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt"}))
	if !strings.Contains(first.Output, "     2  two") {
		t.Fatalf("first read should send content: %q", first.Output)
	}
	second := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt"}))
	if strings.Contains(second.Output, "two") {
		t.Fatalf("unchanged re-read sent the content again: %q", second.Output)
	}
	if !strings.Contains(second.Output, "unchanged since your last read (hash ") || !strings.Contains(second.Output, "force=true") {
		t.Fatalf("unchanged notice missing hash or force hint: %q", second.Output)
	}
	if second.Summary != "unchanged" {
		t.Fatalf("summary = %q", second.Summary)
	}
}

func TestReadForceSendsAgain(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "one\ntwo\n")
	r := NewRead()
	mustOK(t, call(t, r, env, map[string]any{"path": "a.txt"}))
	again := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "force": true}))
	if !strings.Contains(again.Output, "     2  two") {
		t.Fatalf("force=true should send content: %q", again.Output)
	}
	// After a forced read the content is in context again, so a plain re-read
	// is unchanged once more.
	plain := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt"}))
	if !strings.Contains(plain.Output, "unchanged since your last read") {
		t.Fatalf("plain re-read after force should be unchanged: %q", plain.Output)
	}
}

func TestReadChangedFileSendsContent(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "a.txt", "one\ntwo\n")
	r := NewRead()
	mustOK(t, call(t, r, env, map[string]any{"path": "a.txt"}))
	if err := os.WriteFile(abs, []byte("one\n2222\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bump(t, abs)
	got := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt"}))
	if !strings.Contains(got.Output, "2222") || strings.Contains(got.Output, "unchanged") {
		t.Fatalf("changed file should be sent in full: %q", got.Output)
	}
}

func TestReadUnchangedFollowsWhatWasSent(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "l1\nl2\nl3\nl4\nl5\n")
	r := NewRead()
	mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "1-2"}))

	// Lines 1-2 are in context: asking for them again is unchanged.
	again := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "offset": 1, "limit": 2}))
	if !strings.Contains(again.Output, "unchanged") {
		t.Fatalf("lines 1-2 were already sent: %q", again.Output)
	}
	// Lines 2-5 include lines 3-5 that were never sent: content, not notice.
	part := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "2-5"}))
	if strings.Contains(part.Output, "unchanged") || !strings.Contains(part.Output, "l5") {
		t.Fatalf("new lines must be sent: %q", part.Output)
	}
	// 1-2 and 2-5 together now cover 1-5, so the whole file is unchanged.
	all := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt"}))
	if !strings.Contains(all.Output, "unchanged since your last read") {
		t.Fatalf("1-5 were sent in two pieces; expected unchanged: %q", all.Output)
	}
}

func TestReadLinesOption(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "a\nb\nc\nd\ne\n")
	r := NewRead()
	got := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "2-3"}))
	want := "     2  b\n     3  c\nhash:" + shortHash("b\nc")
	if got.Output != want {
		t.Fatalf("lines 2-3:\n%q\nwant\n%q", got.Output, want)
	}
	if got.Summary != "lines 2-3" {
		t.Fatalf("summary = %q", got.Summary)
	}

	tail := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "4-"}))
	if !strings.HasPrefix(tail.Output, "     4  d\n     5  e\nhash:") {
		t.Fatalf("open-ended range: %q", tail.Output)
	}
	one := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "1"}))
	if !strings.HasPrefix(one.Output, "     1  a\n") || !strings.HasSuffix(one.Output, "\nhash:"+shortHash("a")) {
		t.Fatalf("single line: %q", one.Output)
	}
}

func TestReadLinesCutShortSaysWhatWasNotShown(t *testing.T) {
	env, root, _, _ := newEnv(t)
	// 300 lines of 1000 characters overflow the 250 KB reply cap, so the range
	// is cut short and the reply must say which lines were left out.
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString(strings.Repeat("x", 1000) + "\n")
	}
	write(t, root, "big.txt", sb.String())
	got := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "big.txt", "lines": "1-300"}))
	if !strings.Contains(got.Output, " of this read were not shown; call read with lines=") || !strings.Contains(got.Output, "-300 to continue.") {
		t.Fatalf("missing cut-short hint: %q", got.Output[len(got.Output)-200:])
	}
}

func TestReadSymbolDoesNotOfferLinesOutsideIt(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "s.go", goSymbolsSrc)
	got := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "s.go", "symbol": "Server"}))
	if strings.Contains(got.Output, "not shown") || strings.Contains(got.Output, "more lines") {
		t.Fatalf("symbol read offered lines outside the symbol: %q", got.Output)
	}
}

func TestReadLinesRejectsBadRanges(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "a\nb\n")
	r := NewRead()
	mustErr(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "abc"}), "lines must look like")
	mustErr(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "5-3"}), "lines must look like")
	mustErr(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "0-2"}), "lines must look like")
	mustErr(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "9-12"}), "past the end")
	mustErr(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": "1-2", "symbol": "x"}), "not both")
}

func TestReadLinesAcceptsNumberAndUsesNoFooterOnPlainRead(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "a\nb\nc\n")
	r := NewRead()
	// A bare number is one line, sent as a JSON number.
	got := mustOK(t, call(t, r, env, map[string]any{"path": "a.txt", "lines": 2}))
	if !strings.HasPrefix(got.Output, "     2  b\n") || !strings.Contains(got.Output, "\nhash:") {
		t.Fatalf("numeric lines: %q", got.Output)
	}
	plain := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.txt", "offset": 2, "limit": 1}))
	if strings.Contains(plain.Output, "hash:") {
		t.Fatalf("plain offset/limit read should not carry the hash footer: %q", plain.Output)
	}
}

func TestReadMultiFileRangeAppliesToEach(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "a1\na2\na3\n")
	write(t, root, "b.txt", "b1\nb2\nb3\n")
	got := mustOK(t, call(t, NewRead(), env, map[string]any{"paths": []string{"a.txt", "b.txt"}, "lines": "2"}))
	if !strings.Contains(got.Output, "── a.txt ──\n     2  a2") || !strings.Contains(got.Output, "── b.txt ──\n     2  b2") {
		t.Fatalf("multi-file lines: %q", got.Output)
	}
}

func TestReadLinesOnDirectoryIsRefused(t *testing.T) {
	env, root, _, _ := newEnv(t)
	if err := os.MkdirAll(root+"/d", 0o755); err != nil {
		t.Fatal(err)
	}
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "d", "lines": "1-2"}), "is a directory")
}

const goSymbolsSrc = `package p

type Server struct {
	addr string
}

func (s *Server) Run() error {
	return nil
}

func Run() {}
`

func TestReadSymbolGoTypeAndFunction(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "s.go", goSymbolsSrc)
	r := NewRead()

	typ := mustOK(t, call(t, r, env, map[string]any{"path": "s.go", "symbol": "Server"}))
	if !strings.HasPrefix(typ.Output, "Server (type), lines 3-5:\n") {
		t.Fatalf("type header: %q", typ.Output)
	}
	if !strings.Contains(typ.Output, "     3  type Server struct {\n     4  \taddr string\n     5  }") {
		t.Fatalf("type body: %q", typ.Output)
	}
	if strings.Contains(typ.Output, "func") {
		t.Fatalf("symbol read leaked other code: %q", typ.Output)
	}
	if !strings.HasSuffix(typ.Output, "\nhash:"+shortHash("type Server struct {\n\taddr string\n}")) {
		t.Fatalf("symbol footer hash: %q", typ.Output)
	}
}

func TestReadSymbolAmbiguousListsSpans(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "s.go", goSymbolsSrc)
	// "Run" is both a method and a function: the regex cannot tell them apart,
	// so the read must say so and list both spans.
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "s.go", "symbol": "Run"}), "2 definitions match")
}

func TestReadSymbolMethodSpanWithoutAmbiguity(t *testing.T) {
	env, root, _, _ := newEnv(t)
	src := "package p\n\nfunc Only() int {\n\treturn 1\n}\n\nfunc Other() {}\n"
	write(t, root, "o.go", src)
	got := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "o.go", "symbol": "Only"}))
	if !strings.HasPrefix(got.Output, "Only (func), lines 3-5:\n") {
		t.Fatalf("func span: %q", got.Output)
	}
	if strings.Contains(got.Output, "Other") {
		t.Fatalf("span ran past the function: %q", got.Output)
	}
	one := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "o.go", "symbol": "Other"}))
	if !strings.HasPrefix(one.Output, "Other (func), lines 7-7:\n") {
		t.Fatalf("one-line func span: %q", one.Output)
	}
}

func TestReadSymbolPythonIndentedBlock(t *testing.T) {
	env, root, _, _ := newEnv(t)
	src := "class A:\n    def go(self):\n        return 1\n\n    def other(self):\n        pass\nx = 1\n"
	write(t, root, "a.py", src)
	got := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "a.py", "symbol": "go"}))
	if !strings.HasPrefix(got.Output, "go (def), lines 2-3:\n") {
		t.Fatalf("python span: %q", got.Output)
	}
	if strings.Contains(got.Output, "other") {
		t.Fatalf("python span ran into the next method: %q", got.Output)
	}
}

func TestReadSymbolJavaScriptMethod(t *testing.T) {
	env, root, _, _ := newEnv(t)
	src := "class Box {\n  render(a) {\n    return \"}\"; // }\n  }\n}\n"
	write(t, root, "b.js", src)
	got := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "b.js", "symbol": "render"}))
	if !strings.HasPrefix(got.Output, "render (method), lines 2-4:\n") {
		t.Fatalf("js method span (braces inside a string must not count): %q", got.Output)
	}
}

func TestReadSymbolNotFound(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "s.go", goSymbolsSrc)
	mustErr(t, call(t, NewRead(), env, map[string]any{"path": "s.go", "symbol": "Missing"}), `No symbol named "Missing"`)
}

func TestReadSymbolUsesCodeMapWhenPresent(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "h.go", "package p\n\nfunc Helper() {\n\t_ = 1\n}\n")
	m, err := repomap.Open(root, repomap.Options{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	// Shift the file down after the map was built. The regex would now find
	// Helper at line 5; the map still says line 3, so a span of 3-5 proves the
	// map answered.
	write(t, root, "h.go", "// one\n// two\npackage p\n\nfunc Helper() {\n\t_ = 1\n}\n")
	env.Services = map[string]any{"repomap": m}
	got := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "h.go", "symbol": "Helper"}))
	if !strings.HasPrefix(got.Output, "Helper (") || !strings.Contains(got.Output, "lines 3-5:") {
		t.Fatalf("expected the code map span (lines 3-5), got: %q", got.Output)
	}
}

func TestReadSymbolFallsBackWhenMapIsAbsent(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "h.go", "package p\n\nfunc Helper() {\n\t_ = 1\n}\n")
	env.Services = map[string]any{"repomap": (*repomap.Map)(nil)}
	got := mustOK(t, call(t, NewRead(), env, map[string]any{"path": "h.go", "symbol": "Helper"}))
	if !strings.HasPrefix(got.Output, "Helper (func), lines 3-5:") {
		t.Fatalf("regex fallback: %q", got.Output)
	}
	// A registry without the service at all behaves the same way.
	env.Services = nil
	if _, ok := tools.Service[*repomap.Map](env, "repomap"); ok {
		t.Fatal("nil services should not yield a map")
	}
	mustOK(t, call(t, NewRead(), env, map[string]any{"path": "h.go", "symbol": "Helper"}))
}

func TestReadRangeHashRoundTripsIntoEdit(t *testing.T) {
	env, root, _, _ := newEnv(t)
	abs := write(t, root, "r.txt", "keep\nold one\nold two\nend\n")
	r := NewRead()
	shown := mustOK(t, call(t, r, env, map[string]any{"path": "r.txt", "lines": "2-3"}))
	hash := strings.TrimPrefix(shown.Output[strings.LastIndex(shown.Output, "\nhash:"):], "\nhash:")
	mustOK(t, call(t, NewEditFile(nil), env, map[string]any{
		"path": "r.txt", "old_string": "old one\nold two", "new_string": "new one\nnew two",
		"ifHash": hash,
	}))
	if readFile(t, abs) != "keep\nnew one\nnew two\nend\n" {
		t.Fatalf("edit with the read hash did not apply: %q", readFile(t, abs))
	}
}
