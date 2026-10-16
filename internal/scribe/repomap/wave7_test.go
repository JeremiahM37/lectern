package repomap

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

// hasEdge reports whether the render graph has an edge from file a to file b.
func hasEdge(t *testing.T, m *Map, a, b string) bool {
	t.Helper()
	ai, okA := m.nodeIdx[a]
	bi, okB := m.nodeIdx[b]
	if !okA || !okB {
		t.Fatalf("missing node %s or %s", a, b)
	}
	for k := range m.eFrom {
		if int(m.eFrom[k]) == ai && int(m.eTo[k]) == bi {
			return true
		}
	}
	return false
}

func TestScribeIgnoreExcludesPathsInGitRepo(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"main.go":              "package main\n\nfunc Main() {}\n",
		".scribeignore":        "gen/\n*.pb.go\n",
		".soulforgeignore":     "soul/\n",
		"gen/gen.go":           "package gen\n\nfunc Gen() {}\n",
		"api/api.pb.go":        "package api\n\nfunc Pb() {}\n",
		"soul/soul.go":         "package soul\n\nfunc Soul() {}\n",
		"api/keep.go":          "package api\n\nfunc Keep() {}\n",
		"nested/.scribeignore": "!keep.pb.go\n",
		"nested/keep.pb.go":    "package nested\n\nfunc KeepPb() {}\n",
	})
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "add", "-A")
	gitRun(t, root, "commit", "-q", "-m", "init")
	m := buildMap(t, root, Options{InMemory: true})
	have := map[string]bool{}
	for _, p := range m.paths {
		have[p] = true
	}
	for _, gone := range []string{"gen/gen.go", "api/api.pb.go"} {
		if have[gone] {
			t.Errorf("%s should be excluded by .scribeignore", gone)
		}
	}
	for _, kept := range []string{"main.go", "api/keep.go", "soul/soul.go", "nested/keep.pb.go"} {
		if !have[kept] {
			t.Errorf("%s should be indexed; .soulforgeignore is not read and the nested negation applies", kept)
		}
	}
}

func TestScribeIgnoreInFallbackWalk(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"a.go":              "package a\n\nfunc A() {}\n",
		".scribeignore":     "drafts/\n",
		"sub/b.go":          "package sub\n\nfunc B() {}\n",
		"sub/.gitignore":    "tmp.go\n",
		"sub/.scribeignore": "secret.go\n",
		"sub/secret.go":     "package sub\n\nfunc S() {}\n",
		"sub/tmp.go":        "package sub\n\nfunc T() {}\n",
		"drafts/d.go":       "package drafts\n\nfunc D() {}\n",
	})
	m := buildMap(t, root, Options{InMemory: true})
	have := map[string]bool{}
	for _, p := range m.paths {
		have[p] = true
	}
	for _, gone := range []string{"drafts/d.go", "sub/secret.go", "sub/tmp.go"} {
		if have[gone] {
			t.Errorf("%s should be excluded", gone)
		}
	}
	for _, kept := range []string{"a.go", "sub/b.go"} {
		if !have[kept] {
			t.Errorf("%s should be indexed", kept)
		}
	}
}

func TestCalleesFollowTheSameNameRule(t *testing.T) {
	src := `package p

func Outer() int {
	return Inner() + Helper()
}

func Inner() int { return 1 }

func Helper() int { return 2 }

func Common() int { return 3 }

func Pick() int { return Common() }
`
	fi := parseSource("p/p.go", []byte(src))
	m := &Map{files: map[string]*fileInfo{"p/p.go": fi}}
	m.recompute(m.files, nil)

	got := m.Callees("p/p.go", 3)
	var names []string
	for _, s := range got {
		names = append(names, s.Name)
	}
	// Ordered by position: Inner is declared before Helper.
	if strings.Join(names, ",") != "Inner,Helper" {
		t.Fatalf("callees of Outer = %v, want Inner,Helper", names)
	}
	if named := m.CalleesNamed("Outer"); len(named) != 2 {
		t.Errorf("CalleesNamed(Outer) = %d, want 2", len(named))
	}
	if none := m.Callees("p/p.go", 99); len(none) != 0 {
		t.Errorf("a line with no function returned callees: %v", none)
	}
}

func TestCalleesSkipAmbiguousNames(t *testing.T) {
	// Close is defined four times, so a call to it is left unresolved.
	var sb strings.Builder
	sb.WriteString("package p\n\nfunc Run() {\n\tClose()\n\tKeep()\n}\n\nfunc Keep() {}\n")
	for i := 0; i < 4; i++ {
		fmt.Fprintf(&sb, "\ntype T%d struct{}\n\nfunc (T%d) Close() {}\n", i, i)
	}
	fi := parseSource("p/p.go", []byte(sb.String()))
	m := &Map{files: map[string]*fileInfo{"p/p.go": fi}}
	m.recompute(m.files, nil)
	var names []string
	for _, s := range m.Callees("p/p.go", 3) {
		names = append(names, s.Name)
	}
	if strings.Join(names, ",") != "Keep" {
		t.Fatalf("callees = %v, want only Keep", names)
	}
}

func TestBarrelIsDetectedAndPassesThrough(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"src/a.ts":     "export function alpha() {\n  return 1\n}\n",
		"src/b.ts":     "export function beta() {\n  return 2\n}\n",
		"src/index.ts": "export * from './a'\nexport { beta } from './b'\n",
		"src/use.ts":   "import './index'\nexport const value = 3\n",
	})
	m := buildMap(t, root, Options{InMemory: true})
	if !m.files["src/index.ts"].Barrel {
		t.Fatal("index.ts re-exports two modules and defines nothing: it is a barrel")
	}
	if m.files["src/a.ts"].Barrel {
		t.Error("a.ts defines its own symbols and is not a barrel")
	}
	got := m.reexports["src/index.ts"]
	if strings.Join(got, ",") != "src/a.ts,src/b.ts" {
		t.Fatalf("re-export sources = %v", got)
	}
	if !strings.Contains(m.Render(2500, nil), "← src/a.ts, src/b.ts") {
		t.Error("the map should list a barrel's re-export sources")
	}
	// use.ts imports only the barrel, so the edge to a.ts exists only through
	// the passthrough.
	if !hasEdge(t, m, "src/use.ts", "src/a.ts") {
		t.Error("an edge into a barrel should pass through to its sources")
	}
	if !hasEdge(t, m, "src/use.ts", "src/index.ts") {
		t.Error("the import of the barrel itself must remain")
	}
}

func TestReexportIsNotCountedAsAnImportTwice(t *testing.T) {
	fi := parseSource("src/index.ts", []byte("export * from './a'\nexport {x} from './a'\n"))
	n := 0
	for _, im := range fi.Imports {
		if im.Spec == "./a" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("./a recorded %d times, want once", n)
	}
	if !fi.Barrel {
		t.Error("a file of only re-exports is a barrel")
	}
}

func TestDocSummariesForNonGoLanguages(t *testing.T) {
	ts := "/**\n * Adds two numbers.\n * @param a first\n */\nexport function add(a: number, b: number) {\n  return a + b\n}\n"
	fi := parseSource("src/add.ts", []byte(ts))
	if len(fi.Symbols) != 1 || fi.Symbols[0].Summary != "Adds two numbers." {
		t.Fatalf("TS JSDoc summary = %+v", fi.Symbols)
	}

	py := "def public(x):\n    \"\"\"Return the doubled value. More detail follows.\"\"\"\n    return x * 2\n\n\ndef _private():\n    pass\n"
	fi = parseSource("pkg/mod.py", []byte(py))
	for _, s := range fi.Symbols {
		switch s.Name {
		case "public":
			if s.Summary != "" && s.Summary != "Return the doubled value." {
				t.Errorf("python docstring summary = %q", s.Summary)
			}
		case "_private":
			if s.Summary != "" {
				t.Errorf("a private symbol takes no summary, got %q", s.Summary)
			}
		}
	}

	rs := "/// Parses the input.\n#[derive(Debug)]\npub fn parse(s: &str) -> usize {\n    s.len()\n}\n"
	fi = parseSource("src/lib.rs", []byte(rs))
	if len(fi.Symbols) != 1 || fi.Symbols[0].Summary != "Parses the input." {
		t.Fatalf("Rust doc summary across an attribute = %+v", fi.Symbols)
	}
}

func TestDocSummaryShowsInTheMap(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"lib/util.ts": "/** Formats a date for display. */\nexport function fmt(d: Date) {\n  return d.toISOString()\n}\n",
	})
	m := buildMap(t, root, Options{InMemory: true})
	if out := m.Render(2500, nil); !strings.Contains(out, "fmt") || !strings.Contains(out, "Formats a date for display.") {
		t.Errorf("the summary should appear on the symbol line:\n%s", out)
	}
}

func TestSymbolTrigramSearchMatchesFullScan(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("package names\n\n")
	names := []string{"BuildServer", "buildIndex", "ServerConfig", "Serve", "bsrvLimit", "parseBuildFlags",
		"ReadServerFile", "serverTimeout", "Abbrev", "xbsrvx", "tokenize"}
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, "func Gen%dServer%d() {}\n\n", i, i%7)
	}
	for _, n := range names {
		fmt.Fprintf(&sb, "func %s() {}\n\n", n)
	}
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"names/names.go": sb.String()})
	m := buildMap(t, root, Options{InMemory: true})

	for _, q := range []string{"server", "build", "bsrv", "Serve", "config", "zzz", "ser", "parse build", "Abbrev"} {
		for _, limit := range []int{3, 20, 500} {
			got := m.SearchSymbols(q, limit)
			want := referenceSearch(m, q, limit)
			if fmt.Sprint(hitKeys(got)) != fmt.Sprint(hitKeys(want)) {
				t.Errorf("SearchSymbols(%q, %d):\n got %v\nwant %v", q, limit, hitKeys(got), hitKeys(want))
			}
		}
	}
}

// referenceSearch is the unindexed search: score every symbol, then sort.
func referenceSearch(m *Map, query string, limit int) []SymbolHit {
	q := strings.ToLower(strings.TrimSpace(query))
	m.mu.RLock()
	defer m.mu.RUnlock()
	var all []SymbolHit
	for _, s := range m.syms {
		score, ok := nameScore(s.Name, query, q)
		if !ok {
			continue
		}
		if s.Exported {
			score += 2
		}
		all = append(all, SymbolHit{Symbol: s, Score: score, Rank: m.fileRank(s.File) * (1 + logOne(m.refCount[s.Name]))})
	}
	return sortHits(all, limit)
}

func hitKeys(hs []SymbolHit) []string {
	var out []string
	for _, h := range hs {
		out = append(out, fmt.Sprintf("%s@%s:%d=%.0f", h.Name, h.File, h.Line, h.Score))
	}
	return out
}

func TestNearDuplicateFilesAndStructuralFunctions(t *testing.T) {
	body := func(v string) string {
		var sb strings.Builder
		sb.WriteString("package p\n\nfunc Compute(a, b int) int {\n")
		for i := 0; i < 24; i++ {
			fmt.Fprintf(&sb, "\tx%d := a*%d + b - %s\n", i, i+1, v)
		}
		sb.WriteString("\treturn x0 + x23\n}\n")
		return sb.String()
	}
	orig := body("3")
	other := "package q\n\nfunc Unrelated(s string) string {\n\treturn strings.ToUpper(s) + \"!\"\n}\n"
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"one/one.go":  orig,
		"two/two.go":  orig,
		"other/o.go":  other,
		"shape/s1.go": "package s\n\nfunc Scale(alpha int, beta int) int {\n\ttotal := alpha * 2\n\tfor i := 0; i < beta; i++ {\n\t\ttotal = total + i*3\n\t\ttotal = total - alpha\n\t}\n\treturn total + 7\n}\n",
		"shape/s2.go": "package s\n\nfunc Grow(a int, b int) int {\n\tsum := a * 5\n\tfor j := 0; j < b; j++ {\n\t\tsum = sum + j*9\n\t\tsum = sum - a\n\t}\n\treturn sum + 11\n}\n",
	})
	m := buildMap(t, root, Options{InMemory: true})
	rep := m.Clones()

	var sawPair bool
	for _, p := range rep.NearDup {
		pair := p.A + "|" + p.B
		if pair == "one/one.go|two/two.go" || pair == "two/two.go|one/one.go" {
			sawPair = true
			if p.Similarity < 0.99 {
				t.Errorf("identical files estimate %.2f similarity", p.Similarity)
			}
		}
		if strings.Contains(pair, "other/o.go") {
			t.Errorf("an unrelated file was paired: %+v", p)
		}
	}
	if !sawPair {
		t.Fatalf("identical files were not reported as near-duplicates: %+v", rep.NearDup)
	}

	var shapeFound bool
	for _, g := range rep.Shapes {
		names := map[string]bool{}
		for _, s := range g.Symbols {
			names[s.Name] = true
		}
		if names["Scale"] && names["Grow"] {
			shapeFound = true
		}
	}
	if !shapeFound {
		t.Errorf("Scale and Grow differ only in names and literals and should share a shape: %+v", rep.Shapes)
	}

	hints := strings.Join(m.CloneHints("one/one.go", 10), "\n")
	if !strings.Contains(hints, "near-duplicate of two/two.go") {
		t.Errorf("hints for one.go should name its copy:\n%s", hints)
	}
	if !strings.Contains(hints, "Compute is identical to Compute in two/two.go") {
		t.Errorf("hints should name the identical function:\n%s", hints)
	}
	if h := m.CloneHints("other/o.go", 10); len(h) != 0 {
		t.Errorf("an unrelated file should get no hints, got %v", h)
	}
}

func TestSharedFragmentsAreReported(t *testing.T) {
	var shared strings.Builder
	for i := 0; i < 60; i++ {
		fmt.Fprintf(&shared, "\tvalue%d = compute(input, %d)\n", i%5, i)
	}
	a := "package a\n\nfunc A(input int) {\n" + shared.String() + "}\n"
	b := "package b\n\nvar extra = 1\n\nfunc B(input int) {\n" + shared.String() + "}\n"
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"a/a.go": a, "b/b.go": b})
	m := buildMap(t, root, Options{InMemory: true})
	found := false
	for _, f := range m.Clones().Fragments {
		if (f.A == "a/a.go" && f.B == "b/b.go") || (f.A == "b/b.go" && f.B == "a/a.go") {
			found = f.Shared >= minFragShared
		}
	}
	if !found {
		t.Fatal("files sharing a 60-token block should share sampled fragments")
	}
}

func TestDerivedSignaturesSurviveTheCache(t *testing.T) {
	src := "/** Does a thing with a long enough body to carry a shape. */\nexport function thing(a: number) {\n" +
		strings.Repeat("  total = total + a * 2 - 1\n", 6) + "}\n"
	fi := parseSource("src/thing.ts", []byte(src))
	fi.Barrel = false
	fi.Clone, fi.Frags = fileSignatures(mask([]byte(src), &langFor("x.ts").lex, false))
	data, err := encodeInfo(fi)
	if err != nil {
		t.Fatal(err)
	}
	back, err := decodeInfo(data)
	if err != nil {
		t.Fatal(err)
	}
	if back.Symbols[0].Summary != fi.Symbols[0].Summary || back.Symbols[0].ShapeHash != fi.Symbols[0].ShapeHash {
		t.Errorf("symbol fields lost: %+v vs %+v", back.Symbols[0], fi.Symbols[0])
	}
	if (back.Clone == nil) != (fi.Clone == nil) || (fi.Clone != nil && *back.Clone != *fi.Clone) {
		t.Error("clone signature did not round-trip")
	}
	if len(back.Frags) != len(fi.Frags) {
		t.Errorf("fragments: %d back, %d before", len(back.Frags), len(fi.Frags))
	}
	if fi.Symbols[0].ShapeHash == "" {
		t.Error("a 30-token body should have a shape hash")
	}
}

func TestFindSymbolCanListCallees(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{
		"x/x.go": "package x\n\nfunc Top() {\n\tLeaf()\n}\n\nfunc Leaf() {}\n",
	})
	m := buildMap(t, root, Options{InMemory: true})
	res := runFindSymbol(m, []byte(`{"query":"Top","callees":true}`))
	if res.IsError || !strings.Contains(res.Output, "calls Leaf  x/x.go:") {
		t.Fatalf("find_symbol with callees:\n%s (err=%v)", res.Output, res.IsError)
	}
	plain := runFindSymbol(m, []byte(`{"query":"Top"}`))
	if strings.Contains(plain.Output, "calls ") {
		t.Error("callees are off by default")
	}
}

func TestCodeImpactReportsDuplication(t *testing.T) {
	root := t.TempDir()
	body := "\ttotal := 0\n" + strings.Repeat("\ttotal = total + 1\n", 12)
	writeFixture(t, root, map[string]string{
		"one/one.go": "package one\n\nfunc Work() {\n" + body + "}\n",
		"two/two.go": "package two\n\nfunc Work() {\n" + body + "}\n",
	})
	m := buildMap(t, root, Options{InMemory: true})
	res := runCodeImpact(m, []byte(`{"file":"one/one.go"}`))
	if res.IsError || !strings.Contains(res.Output, "Duplication:") {
		t.Fatalf("code_impact should report duplication:\n%s", res.Output)
	}
}

// logOne mirrors the rank term the search uses, kept separate so the reference
// search does not depend on the code it checks.
func logOne(n int) float64 {
	return math.Log1p(float64(n))
}
