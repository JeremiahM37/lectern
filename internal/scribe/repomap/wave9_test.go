package repomap

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// wave9Fixture is a small Go repository with the cases the wave 9 queries
// need: a method and a function that match one name, an exported function
// nothing uses, an export only a test uses, an external import, and two
// identical functions in different files.
var wave9Fixture = map[string]string{
	"svc/auth/token.go": `package auth

type TokenStore struct{ items int }

func (t *TokenStore) Refresh() error { return nil }

func RefreshToken() int { return 1 }

func ExportedUnused() int { return 0 }
`,
	"svc/auth/fake.go": `package auth

func FakeClock() int { return 3 }
`,
	"svc/auth/token_test.go": `package auth

import "testing"

func TestRefresh(t *testing.T) {
	s := &TokenStore{}
	_ = s.Refresh()
	_ = FakeClock()
}
`,
	"svc/api/handler.go": `package api

import (
	"fmt"

	"example.com/ext/lib"
)

func Handle() string {
	fmt.Sprint(RefreshToken())
	return lib.Name()
}
`,
	"svc/a/dup.go": "package a\n\nfunc Clamp(x int) int {\n\tif x < 0 {\n\t\treturn 0\n\t}\n\treturn x\n}\n",
	"svc/b/dup.go": "package b\n\nfunc Clamp(x int) int {\n\tif x < 0 {\n\t\treturn 0\n\t}\n\treturn x\n}\n",
	"svc/many/items.go": `package many

func Item1() {}
func Item2() {}
func Item3() {}
func Item4() {}
func Item5() {}
func Item6() {}
`,
}

func wave9Map(t *testing.T) (*Map, string) {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, wave9Fixture)
	return buildMap(t, root, Options{InMemory: true}), root
}

func wave9Registry(m *Map) *tools.Registry {
	reg := tools.NewRegistry()
	Register(reg, m)
	return reg
}

func callTool(t *testing.T, reg *tools.Registry, name, args string) tools.Result {
	t.Helper()
	tl, ok := reg.Get(name)
	if !ok {
		t.Fatalf("tool %s not registered", name)
	}
	return tl.Run(context.Background(), &tools.Env{}, []byte(args))
}

func TestFileCategoryAndKindNames(t *testing.T) {
	cases := map[string]string{
		"a/b_test.go":               "test",
		"src/components/Button.tsx": "component",
		"config.yaml":               "config",
		"src/types.ts":              "types",
		"web/app.css":               "style",
		"main.go":                   "",
	}
	for p, want := range cases {
		if got := FileCategory(p); got != want {
			t.Errorf("FileCategory(%q) = %q, want %q", p, got, want)
		}
	}
	for in, want := range map[string]string{"Functions": "function", "fn": "function", "Struct": "struct", "type-alias": "type"} {
		if got, ok := NormalizeKind(in); !ok || got != want {
			t.Errorf("NormalizeKind(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	if _, ok := NormalizeKind("tuple"); ok {
		t.Errorf("tuple should not be a known kind")
	}
}

func TestSplitWordsAndSyntheticSummary(t *testing.T) {
	if got := strings.Join(splitWords("HTTPServer2fix_now"), " "); got != "http server 2 fix now" {
		t.Errorf("splitWords = %q", got)
	}
	if got := SyntheticSummary("parse_http_request"); got != "Parse http request" {
		t.Errorf("SyntheticSummary = %q", got)
	}
	if got := SyntheticSummary("BuildServer"); got != "Build server" {
		t.Errorf("SyntheticSummary = %q", got)
	}
}

func TestFindSymbolKindAndTypeFilters(t *testing.T) {
	m, _ := wave9Map(t)
	reg := wave9Registry(m)

	res := callTool(t, reg, "find_symbol", `{"query":"Refresh","kind":"function"}`)
	if res.IsError || !strings.Contains(res.Output, "RefreshToken") || !strings.Contains(res.Output, "Refresh ") {
		t.Errorf("kind function should keep both the function and the method:\n%s", res.Output)
	}
	res = callTool(t, reg, "find_symbol", `{"query":"TokenStore","kind":"Structs"}`)
	if res.IsError || !strings.Contains(res.Output, "struct TokenStore") || strings.Contains(res.Output, "function RefreshToken") {
		t.Errorf("kind struct filter:\n%s", res.Output)
	}
	res = callTool(t, reg, "find_symbol", `{"query":"Refresh","type":"test"}`)
	if res.IsError || !strings.Contains(res.Output, "token_test.go [test]") || strings.Contains(res.Output, "token.go [") {
		t.Errorf("type test filter:\n%s", res.Output)
	}
	if res := callTool(t, reg, "find_symbol", `{"query":"x","kind":"tuple"}`); !res.IsError || !strings.Contains(res.Output, "Use one of") {
		t.Errorf("unknown kind should be an error that lists the kinds: %s", res.Output)
	}
	if res := callTool(t, reg, "find_symbol", `{"query":"x","type":"movies"}`); !res.IsError {
		t.Errorf("unknown type should be an error")
	}
}

func TestFindSymbolPerFileCap(t *testing.T) {
	m, _ := wave9Map(t)
	reg := wave9Registry(m)
	res := callTool(t, reg, "find_symbol", `{"query":"Item"}`)
	if res.IsError {
		t.Fatalf("find_symbol: %s", res.Output)
	}
	if !strings.Contains(res.Output, "... 2 more in this file") {
		t.Errorf("six matches in one file should show four and say two more:\n%s", res.Output)
	}
	if strings.Count(res.Output, "  function Item") != 4 {
		t.Errorf("want exactly four Item lines:\n%s", res.Output)
	}
}

func TestFindSymbolConceptSearch(t *testing.T) {
	m, _ := wave9Map(t)
	reg := wave9Registry(m)
	res := callTool(t, reg, "find_symbol", `{"query":"token refresh","concept":true}`)
	if res.IsError || !strings.Contains(res.Output, "Refresh") {
		t.Errorf("concept search should find the token refresh code:\n%s", res.Output)
	}
	res = callTool(t, reg, "find_symbol", `{"query":"auth"}`)
	if !strings.Contains(res.Output, "Closest by concept") {
		t.Errorf("a phrase with no name match should fall back to concept search:\n%s", res.Output)
	}
	ch, files := m.ConceptSearch("auth token", 10)
	if len(ch) == 0 || len(files) == 0 || files[0].Path == "" {
		t.Errorf("ConceptSearch returned nothing for a file-path phrase")
	}
}

func TestFindSymbolCallersFlag(t *testing.T) {
	m, _ := wave9Map(t)
	reg := wave9Registry(m)
	res := callTool(t, reg, "find_symbol", `{"query":"RefreshToken","callers":true}`)
	if !strings.Contains(res.Output, "called by Handle") {
		t.Errorf("callers flag should list Handle:\n%s", res.Output)
	}
}

func TestCodeMapActions(t *testing.T) {
	m, _ := wave9Map(t)
	reg := wave9Registry(m)
	cases := []struct{ args, want string }{
		{`{"action":"identifier_frequency"}`, "RefreshToken"},
		{`{"action":"unused_exports"}`, "ExportedUnused"},
		{`{"action":"unused_exports"}`, "Test-only exports (2)"},
		{`{"action":"dead-exports"}`, "FakeClock"},
		{`{"action":"packages"}`, "example.com/ext/lib"},
		{`{"action":"symbols_by_kind"}`, "struct"},
		{`{"action":"symbols_by_kind","kind":"struct"}`, "TokenStore"},
		{`{"action":"class_members","name":"TokenStore"}`, "func (t *TokenStore) Refresh"},
		{`{"action":"top_files"}`, "importers"},
		{`{"action":"file_profile","file":"svc/api/handler.go"}`, "Blast radius"},
		{`{"action":"file_profile","file":"svc/api/handler.go"}`, "Imports (2)"},
		{`{"action":"duplication"}`, "Clamp"},
		{`{"action":"call_graph","name":"Handle"}`, "Calls (1)"},
		{`{"action":"search_symbols","name":"Refresh*"}`, "RefreshToken"},
		{`{"action":"summaries"}`, "no model summaries"},
		{`{"action":"map"}`, "Code map:"},
		{`{}`, "Code map:"},
	}
	for _, c := range cases {
		res := callTool(t, reg, "code_map", c.args)
		if res.IsError || !strings.Contains(res.Output, c.want) {
			t.Errorf("code_map %s: want %q, got error=%v:\n%s", c.args, c.want, res.IsError, res.Output)
		}
	}
	bad := map[string]string{
		`{"action":"bogus"}`:                          "Use one of",
		`{"action":"class_members"}`:                  "name is required",
		`{"action":"file_profile"}`:                   "file is required",
		`{"action":"call_graph"}`:                     "name is required",
		`{"action":"search_symbols"}`:                 "name is required",
		`{"action":"symbols_by_kind","kind":"tuple"}`: "unknown kind",
	}
	for args, want := range bad {
		res := callTool(t, reg, "code_map", args)
		if !res.IsError || !strings.Contains(res.Output, want) {
			t.Errorf("code_map %s: want error containing %q, got %q", args, want, res.Output)
		}
	}
}

func TestUnusedExportsReport(t *testing.T) {
	m, _ := wave9Map(t)
	rep := m.UnusedExports()
	var unused []string
	for _, s := range rep.Unused {
		unused = append(unused, s.Name)
	}
	if !strings.Contains(strings.Join(unused, ","), "ExportedUnused") {
		t.Errorf("ExportedUnused missing from unused: %v", unused)
	}
	for _, s := range rep.Unused {
		if s.Name == "RefreshToken" || s.Name == "FakeClock" {
			t.Errorf("%s is used and must not be reported unused", s.Name)
		}
	}
	var testOnly []string
	for _, u := range rep.TestOnly {
		testOnly = append(testOnly, u.Symbol.Name)
	}
	if strings.Join(testOnly, ",") != "FakeClock,TokenStore" {
		t.Errorf("test-only exports = %v, want FakeClock and TokenStore", testOnly)
	}
}

func TestSearchPrefixUsesSortedNames(t *testing.T) {
	m, _ := wave9Map(t)
	hits := m.SearchPrefix("REFRESH", 10)
	if len(hits) < 2 {
		t.Fatalf("prefix search found %d, want at least Refresh and RefreshToken", len(hits))
	}
	if hits[0].Name != "Refresh" {
		t.Errorf("shortest name should come first, got %s", hits[0].Name)
	}
	if got := m.SearchPrefix("zzz", 10); len(got) != 0 {
		t.Errorf("no prefix match should return nothing, got %d", len(got))
	}
}

func TestSummaryModes(t *testing.T) {
	m, _ := wave9Map(t)
	if m.SetSummaryMode("bogus") {
		t.Errorf("unknown summary mode should be refused")
	}
	if got := m.SummaryMode(); got != SummaryAST {
		t.Errorf("default mode = %q", got)
	}
	if out := m.Render(4000, nil); strings.Contains(out, "Refresh token") {
		t.Errorf("ast mode must not add synthetic summaries")
	}
	if !m.SetSummaryMode("synthetic") {
		t.Fatal("synthetic should be accepted")
	}
	if out := m.Render(4000, nil); !strings.Contains(out, "Refresh token") {
		t.Errorf("synthetic mode should name an undocumented exported symbol:\n%s", out)
	}
	m.SetSummaryMode("off")
	if out := m.Render(4000, nil); strings.Contains(out, " - ") {
		t.Errorf("off mode should show no summaries")
	}
}

func TestClearEmptiesAndRebuilds(t *testing.T) {
	m, _ := wave9Map(t)
	if m.Stats().Files == 0 {
		t.Fatal("fixture did not index")
	}
	if err := m.Clear(); err != nil {
		t.Fatal(err)
	}
	if st := m.Stats(); st.Files != 0 || st.Symbols != 0 {
		t.Fatalf("after Clear: %+v", st)
	}
	if err := m.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if m.Stats().Files == 0 {
		t.Errorf("Build after Clear indexed nothing")
	}
}

func TestIdleWatchdogAbandonsStalledScan(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, wave9Fixture)
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	m, err := Open(root, Options{InMemory: true, Workers: 2, IdleTimeout: 50 * time.Millisecond,
		stall: func(string) { <-release }})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	done := make(chan error, 1)
	go func() { done <- m.Build(context.Background(), nil) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "stalled") {
			t.Fatalf("Build = %v, want a stalled-scan error", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Build did not give up on a stalled scan")
	}
}
