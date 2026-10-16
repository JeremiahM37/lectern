package repomap

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const codecSrc = `// Package store keeps things. It is the second sentence and is dropped.
package store

import (
	"fmt"
	"strings"
)

// Store is a keyed store.
type Store struct{ items map[string]string }

// Put saves a value under a key and reports it.
func (s *Store) Put(key, value string) {
	fmt.Println(strings.ToUpper(key), value)
}

func helper() int { return 42 }
`

func TestCodecRoundTrip(t *testing.T) {
	fi := parseSource("internal/store/store.go", []byte(codecSrc))
	if fi == nil {
		t.Fatal("parseSource returned nil")
	}
	fi.Mtime, fi.Hash = 123, "abc"
	row, err := encodeInfo(fi)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeInfo(row)
	if err != nil {
		t.Fatalf("decodeInfo: %v", err)
	}
	// Symbol offsets and the per-load Mtime and Hash are not persisted.
	want := *fi
	want.Symbols = append([]Symbol(nil), fi.Symbols...)
	for i := range want.Symbols {
		want.Symbols[i].start, want.Symbols[i].end = 0, 0
	}
	want.Mtime, want.Hash = 0, ""
	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("round trip changed the extraction:\n got %+v\nwant %+v", *got, want)
	}
	if got.Doc != "Package store keeps things." {
		t.Errorf("Doc = %q", got.Doc)
	}
	var put *Symbol
	for i := range got.Symbols {
		if got.Symbols[i].Name == "Put" {
			put = &got.Symbols[i]
		}
	}
	if put == nil || !strings.Contains(put.calls, "Println") {
		t.Fatalf("Put calls = %v, want fmt.Println among them", put)
	}
}

func TestDecodeRejectsDamagedRows(t *testing.T) {
	fi := parseSource("internal/store/store.go", []byte(codecSrc))
	row, err := encodeInfo(fi)
	if err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(row); n++ {
		if _, err := decodeInfo(row[:n]); err == nil {
			t.Fatalf("truncated to %d of %d bytes: decode succeeded", n, len(row))
		}
	}
	if _, err := decodeInfo(append(append([]byte(nil), row...), 0)); err == nil {
		t.Error("trailing byte: decode succeeded")
	}
	bad := append([]byte(nil), row...)
	bad[0] = codecVersion + 1
	if _, err := decodeInfo(bad); err == nil {
		t.Error("unknown format version: decode succeeded")
	}
	// A count larger than the input must fail before allocating.
	if _, err := decodeInfo([]byte{codecVersion, 0xff, 0xff, 0xff, 0xff, 0x0f}); err == nil {
		t.Error("oversized count: decode succeeded")
	}
}

func TestCallersAreNameBasedAndBounded(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"go.mod":         "module example.com/calls\n\ngo 1.25\n",
		"app/handler.go": "package app\n\nfunc Handle() {\n\tRunJob()\n}\n\nfunc Close() {\n\tRunJob()\n}\n",
		"app/job.go":     "package app\n\nfunc RunJob() {}\n",
		"app/shared.go":  "package app\n\nfunc Close() {}\n\nfunc Wrap() {\n\tClose()\n}\n",
		"other/a.go":     "package other\n\nfunc Ping() {}\n",
		"other/b.go":     "package other\n\nfunc Ping() {}\n",
		"other/c.go":     "package other\n\nfunc Ping() {}\n\nfunc Use() {\n\tPing()\n}\n",
		"other/d.go":     "package other\n\nfunc Ping() {}\n",
	})
	m := buildMap(t, root, Options{InMemory: true})

	got := map[string]bool{}
	for _, s := range m.Callers("RunJob") {
		got[s.Name+"@"+s.File] = true
	}
	if !got["Handle@app/handler.go"] || !got["Close@app/handler.go"] || len(got) != 2 {
		t.Errorf("Callers(RunJob) = %v", got)
	}
	// Ping has four definitions, over maxCallerDefs: it must not be linked.
	if n := len(m.Callers("Ping")); n != 0 {
		t.Errorf("Callers(Ping) = %d, want 0 for an ambiguous name", n)
	}
	// Close is defined twice, within the limit, so Wrap calls it.
	if n := len(m.Callers("Close")); n != 1 {
		t.Errorf("Callers(Close) = %d, want 1", n)
	}

	out := m.Render(2500, nil)
	if !strings.Contains(out, "<-2") {
		t.Errorf("RunJob's caller badge missing from:\n%s", out)
	}
}

// fakeSummarizer returns a canned summary per file and records the batches.
type fakeSummarizer struct {
	calls   int
	seen    []string
	fail    bool
	perFile int
}

func (f *fakeSummarizer) Summarize(_ context.Context, files []SummaryRequest) ([]string, error) {
	f.calls++
	out := make([]string, len(files))
	for i, r := range files {
		f.seen = append(f.seen, r.Path)
		out[i] = "summary of " + r.Path
	}
	if f.fail {
		return nil, errors.New("model unavailable")
	}
	return out, nil
}

func TestSummariesAreOptionalCachedAndStale(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"go.mod":        "module example.com/sum\n\ngo 1.25\n",
		"pkg/one.go":    "package pkg\n\nfunc One() int { return 1 }\n",
		"pkg/two.go":    "package pkg\n\nfunc Two() int { return 2 }\n",
		"pkg/empty.go":  "package pkg\n",
		"pkg/doc.go":    "// Package pkg is documented.\npackage pkg\n\nfunc Three() {}\n",
		"pkg/comment.x": "ignored",
	})
	m := buildMap(t, root, Options{})

	// No summarizer: nothing happens, and no call is made.
	if n, err := m.SummarizeMissing(context.Background(), 50); err != nil || n != 0 {
		t.Fatalf("without a Summarizer: n=%d err=%v", n, err)
	}
	if out := m.Render(2500, nil); strings.Contains(out, "  ~ summary of") {
		t.Fatal("summary shown without a Summarizer")
	}
	if out := m.Render(2500, nil); !strings.Contains(out, "  ~ Package pkg is documented.") {
		t.Errorf("doc comment header missing:\n%s", out)
	}

	fs := &fakeSummarizer{}
	m.SetSummarizer(fs)
	n, err := m.SummarizeMissing(context.Background(), 50)
	if err != nil {
		t.Fatal(err)
	}
	// empty.go has no symbols and gets no summary.
	if n != 3 || len(fs.seen) != 3 {
		t.Fatalf("summarized %d files (seen %v), want 3", n, fs.seen)
	}
	if out := m.Render(2500, nil); !strings.Contains(out, "  ~ summary of pkg/one.go") {
		t.Errorf("summary header missing:\n%s", out)
	}
	// Nothing new to do the second time.
	if n, _ := m.SummarizeMissing(context.Background(), 50); n != 0 || fs.calls != 1 {
		t.Errorf("second pass: n=%d calls=%d, want 0 and 1", n, fs.calls)
	}

	// The summary persists across a reopen of the on-disk cache.
	m.Close()
	m2 := buildMap(t, root, Options{})
	if out := m2.Render(2500, nil); !strings.Contains(out, "  ~ summary of pkg/two.go") {
		t.Errorf("summary lost on reopen:\n%s", out)
	}

	// Editing a file makes its summary stale: the header falls back, and only
	// that file is summarized again.
	writeFixture(t, root, map[string]string{"pkg/one.go": "package pkg\n\nfunc One() int { return 11 }\n"})
	if err := m2.Update([]string{"pkg/one.go"}); err != nil {
		t.Fatal(err)
	}
	if out := m2.Render(2500, nil); strings.Contains(out, "summary of pkg/one.go") {
		t.Errorf("stale summary still shown:\n%s", out)
	}
	fs2 := &fakeSummarizer{}
	m2.SetSummarizer(fs2)
	if n, err := m2.SummarizeMissing(context.Background(), 50); err != nil || n != 1 {
		t.Fatalf("after edit: n=%d err=%v, want 1", n, err)
	}
	if len(fs2.seen) != 1 || fs2.seen[0] != "pkg/one.go" {
		t.Errorf("re-summarized %v, want only pkg/one.go", fs2.seen)
	}

	// A failing model keeps what it has and reports the error.
	bad := &fakeSummarizer{fail: true}
	m2.SetSummarizer(bad)
	writeFixture(t, root, map[string]string{"pkg/two.go": "package pkg\n\nfunc Two() int { return 22 }\n"})
	if err := m2.Update([]string{"pkg/two.go"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m2.SummarizeMissing(context.Background(), 50); err == nil {
		t.Error("expected the model error")
	}
	if out := m2.Render(2500, nil); !strings.Contains(out, "summary of pkg/one.go") {
		t.Errorf("current summary dropped after a model failure:\n%s", out)
	}
}

func TestFirstSentence(t *testing.T) {
	cases := map[string]string{
		"Package a does b. More.\n": "Package a does b.",
		"  Multi\nline\n doc  ":     "Multi line doc",
		"No period here":            "No period here",
		strings.Repeat("x ", 100):   strings.Repeat("x ", 60)[:117] + "...",
	}
	for in, want := range cases {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}
