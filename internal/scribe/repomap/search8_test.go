package repomap

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeRepo writes files (slash paths) under a fresh temp dir and returns it.
func writeRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// stampOf returns the (mtime, size) pair the filter receives for rel.
func stampOf(t *testing.T, root, rel string) (int64, int64) {
	t.Helper()
	st, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return st.ModTime().UnixNano(), st.Size()
}

func searchMap(t *testing.T, root string) *Map {
	t.Helper()
	m, err := Open(root, Options{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if err := m.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestLiteralFilterRulesOutOnlyNonMatches(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"a.go":          "package a\n\nfunc HandleRequest(x int) {}\n",
		"b.txt":         "nothing relevant here at all\n",
		"c/d.py":        "def handle_request():\n    pass\n",
		"c/deep/e.md":   "HandleRequest is documented here\n",
		"f.bin":         "HandleRequest\x00\x01",
		"g.txt":         "HandleRequesté accented\n",
		".hidden/h.txt": "HandleRequest\n",
	})
	m := searchMap(t, root)
	skip, ok := m.LiteralFilter("HandleRequest")
	if !ok {
		t.Fatal("expected a filter for a 13-byte ASCII literal")
	}
	check := func(rel string, want bool) {
		t.Helper()
		mt, sz := stampOf(t, root, rel)
		if got := skip(rel, mt, sz); got != want {
			t.Errorf("skip(%s) = %v, want %v", rel, got, want)
		}
	}
	check("b.txt", true) // indexed, ASCII, no match
	check("a.go", false) // contains the literal
	check("c/deep/e.md", false)
	check("f.bin", false) // binary: never ruled out
	check("g.txt", false) // non-ASCII: never ruled out
	check("c/d.py", true) // lower-case snake form does not contain it
}

func TestLiteralFilterStaleStampIsNeverSkipped(t *testing.T) {
	root := writeRepo(t, map[string]string{"x.txt": "alpha beta gamma\n"})
	m := searchMap(t, root)
	skip, ok := m.LiteralFilter("delta")
	if !ok {
		t.Fatal("expected a filter")
	}
	mt, sz := stampOf(t, root, "x.txt")
	if !skip("x.txt", mt, sz) {
		t.Fatal("fresh file without the literal should be skipped")
	}
	// The file changes on disk after the build; the index is now stale.
	if err := os.WriteFile(filepath.Join(root, "x.txt"), []byte("alpha delta gamma\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(root, "x.txt"), later, later); err != nil {
		t.Fatal(err)
	}
	mt, sz = stampOf(t, root, "x.txt")
	if skip("x.txt", mt, sz) {
		t.Fatal("stale stamp must not be ruled out")
	}
}

func TestLiteralFilterUnavailableFallsBack(t *testing.T) {
	root := writeRepo(t, map[string]string{"x.txt": "alpha\n"})
	m, err := Open(root, Options{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	// Nothing built yet: the index is empty and must not narrow anything.
	if _, ok := m.LiteralFilter("alpha"); ok {
		t.Fatal("an empty index must not narrow")
	}
	if err := m.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, lit := range []string{"ab", "é", "a\u00e9b", "   "} {
		if _, ok := m.LiteralFilter(lit); ok {
			t.Errorf("literal %q must not narrow", lit)
		}
	}
}

// A trigram shared by many files still narrows: only files missing one of
// the literal's trigrams are skipped, however common the others are.
func TestLiteralFilterWithCommonTrigrams(t *testing.T) {
	files := map[string]string{"rare.txt": "abcdef xyzzy common\n"}
	for i := 0; i < 600; i++ {
		files[filepath.ToSlash(filepath.Join("many", string(rune('a'+i%26)), strings.Repeat("x", i%7)+string(rune('a'+i/26))+".txt"))] = "abcdef common\n"
	}
	root := writeRepo(t, files)
	m := searchMap(t, root)
	skip, ok := m.LiteralFilter("abcdef xyzzy")
	if !ok {
		t.Fatal("expected a filter")
	}
	mt, sz := stampOf(t, root, "rare.txt")
	if skip("rare.txt", mt, sz) {
		t.Fatal("the one file with the literal was skipped")
	}
	mt, sz = stampOf(t, root, "many/a/a.txt")
	if !skip("many/a/a.txt", mt, sz) {
		t.Fatal("a file without xyzzy should be skipped")
	}
}

// TestLiteralFilterIsSoundOverRandomFixture is the proof that narrowing never
// hides a match: for every file the filter rules out, the literal must be
// absent from its content.
func TestLiteralFilterIsSoundOverRandomFixture(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	const alphabet = "abcAB_ \n.(){}"
	files := map[string]string{}
	for i := 0; i < 80; i++ {
		var b strings.Builder
		n := 20 + rng.Intn(300)
		for j := 0; j < n; j++ {
			b.WriteByte(alphabet[rng.Intn(len(alphabet))])
		}
		if i%9 == 0 {
			b.WriteString("Key") // Kelvin sign: folds to k under (?i)
		}
		files[filepath.ToSlash(filepath.Join("r", string(rune('a'+i%5)), "f"+string(rune('a'+i%26))+string(rune('a'+i/26))+".txt"))] = b.String()
	}
	root := writeRepo(t, files)
	m := searchMap(t, root)

	ruledOut := 0
	for trial := 0; trial < 200; trial++ {
		src := files[pickKey(rng, files)]
		start := rng.Intn(len(src) - 4)
		lit := src[start : start+3+rng.Intn(4)]
		if strings.ContainsAny(lit, "\n") {
			continue
		}
		skip, ok := m.LiteralFilter(lit)
		if !ok {
			continue
		}
		for rel, body := range files {
			mt, sz := stampOf(t, root, rel)
			if skip(rel, mt, sz) {
				ruledOut++
				if strings.Contains(body, lit) {
					t.Fatalf("unsound: %q skipped for %q but contains it", rel, lit)
				}
			}
		}
	}
	if ruledOut == 0 {
		t.Fatal("fixture never narrowed anything; the test proves nothing")
	}
}

func pickKey(rng *rand.Rand, m map[string]string) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Map order is random; sort for a reproducible pick.
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys[rng.Intn(len(keys))]
}

func TestTestLinking(t *testing.T) {
	paths := []string{
		"pkg/foo.go", "pkg/foo_test.go",
		"web/src/button.tsx", "web/src/button.test.tsx",
		"src/util.py", "tests/test_util.py",
		"a/helper.py", "b/helper.py", "b/helper_test.py",
		"web/src/__tests__/card.ts", "web/src/card.ts",
		"lonely_test.go", // no subject file
		"pkg/notes.md",
	}
	got := linkTests(paths)
	want := map[string]string{
		"pkg/foo_test.go":           "pkg/foo.go",
		"web/src/button.test.tsx":   "web/src/button.tsx",
		"tests/test_util.py":        "src/util.py",
		"b/helper_test.py":          "b/helper.py",
		"web/src/__tests__/card.ts": "web/src/card.ts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("linkTests = %v\nwant %v", got, want)
	}
}

func TestTestLinksThroughMap(t *testing.T) {
	root := writeRepo(t, map[string]string{
		"pkg/foo.go":      "package pkg\n\nfunc Foo() {}\n",
		"pkg/foo_test.go": "package pkg\n\nfunc TestFoo() {}\n",
	})
	m := searchMap(t, root)
	if got := m.TestSubject("pkg/foo_test.go"); got != "pkg/foo.go" {
		t.Errorf("TestSubject = %q", got)
	}
	if got := m.TestsOf("pkg/foo.go"); !reflect.DeepEqual(got, []string{"pkg/foo_test.go"}) {
		t.Errorf("TestsOf = %v", got)
	}
}

func TestWatchReindexesExternalChange(t *testing.T) {
	root := writeRepo(t, map[string]string{"a.go": "package a\n\nfunc Alpha() {}\n"})
	m := searchMap(t, root)
	if len(m.Definition("Beta")) != 0 {
		t.Fatal("Beta should not exist yet")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		m.Watch(ctx, 20*time.Millisecond, 40*time.Millisecond)
		close(done)
	}()
	// Written outside the map's own update paths, as an editor or formatter would.
	p := filepath.Join(root, "a.go")
	if err := os.WriteFile(p, []byte("package a\n\nfunc Beta() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for len(m.Definition("Beta")) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("watcher did not pick up the external change")
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done
}
