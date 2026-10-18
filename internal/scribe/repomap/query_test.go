package repomap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// queryFixture is a small module: two packages, one importing the other, and
// a utility package nothing imports.
var queryFixture = map[string]string{
	"go.mod": "module example.com/qfix\n\ngo 1.25\n",
	"svc/auth/token.go": `package auth

type TokenStore struct{ items int }

func (t *TokenStore) Refresh() error { return nil }

func RefreshToken() int { return 1 }
`,
	"svc/auth/token_test.go": `package auth

import "testing"

func TestRefresh(t *testing.T) { _ = RefreshToken() }
`,
	"svc/api/handler.go": `package api

import "example.com/qfix/svc/auth"

func Handle() int { return auth.RefreshToken() }
`,
	"svc/util/strs.go": `package util

func Trim(s string) string { return s }
`,
	"notes.txt": "plain text that mentions RefreshToken once\n",
}

// queryMap builds the fixture map and a registry holding soul_query.
func queryMap(t *testing.T) (*Map, *tools.Registry) {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, queryFixture)
	m := buildMap(t, root, Options{InMemory: true})
	reg := tools.NewRegistry()
	Register(reg, m)
	return m, reg
}

// runSoulQuery calls soul_query with the given pipeline JSON.
func runSoulQuery(t *testing.T, reg *tools.Registry, pipeline string) tools.Result {
	t.Helper()
	return callTool(t, reg, "soul_query", `{"pipeline":`+pipeline+`}`)
}

func TestSoulQueryRegisteredAsReadOnly(t *testing.T) {
	_, reg := queryMap(t)
	tl, ok := reg.Get("soul_query")
	if !ok {
		t.Fatal("soul_query is not registered by Register")
	}
	ro, ok := tl.(interface{ ReadOnly() bool })
	if !ok || !ro.ReadOnly() {
		t.Fatal("soul_query must report itself read-only")
	}
	nilReg := tools.NewRegistry()
	registerQueryTool(nilReg, nil)
	res := callTool(t, nilReg, "soul_query", `{"pipeline":[{"op":"filter","ext":".go"}]}`)
	if !res.IsError || !strings.Contains(res.Output, "not available") {
		t.Fatalf("nil map should be reported, got %q", res.Output)
	}
}

func TestSoulQueryWhichFilesImportAFile(t *testing.T) {
	_, reg := queryMap(t)
	res := runSoulQuery(t, reg, `[{"op":"find","query":"token.go"},{"op":"deps","direction":"imported_by"}]`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "  svc/api/handler.go") {
		t.Errorf("handler.go imports auth and should be listed:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "  svc/auth/token.go\n") {
		t.Errorf("the file itself should not be in its own importers:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, `1. find "token.go" -> `) || !strings.Contains(res.Output, "2. deps imported_by -> ") {
		t.Errorf("trace should name each stage with its file count:\n%s", res.Output)
	}
}

func TestSoulQueryDepsImportsGoesTheOtherWay(t *testing.T) {
	_, reg := queryMap(t)
	res := runSoulQuery(t, reg, `[{"op":"filter","pathContains":"api"},{"op":"deps","direction":"imports"}]`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "svc/auth/token.go") {
		t.Errorf("handler.go imports the auth package, so a token.go file should appear:\n%s", res.Output)
	}
}

func TestSoulQuerySearchIsCaseInsensitiveLiteral(t *testing.T) {
	_, reg := queryMap(t)
	res := runSoulQuery(t, reg, `[{"op":"search","pattern":"refreshTOKEN"}]`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	for _, want := range []string{"svc/auth/token.go", "svc/api/handler.go", "svc/auth/token_test.go"} {
		if !strings.Contains(res.Output, "  "+want+"\n") {
			t.Errorf("search should find %s:\n%s", want, res.Output)
		}
	}
	if strings.Contains(res.Output, "strs.go") {
		t.Errorf("strs.go does not contain the text:\n%s", res.Output)
	}
}

func TestSoulQuerySearchShortPatternStillWorks(t *testing.T) {
	_, reg := queryMap(t)
	res := runSoulQuery(t, reg, `[{"op":"search","pattern":"Tr"},{"op":"filter","pathContains":"util"}]`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "  svc/util/strs.go\n") {
		t.Errorf("two-letter search should still scan files:\n%s", res.Output)
	}
}

func TestSoulQueryFilterAndLimit(t *testing.T) {
	_, reg := queryMap(t)
	res := runSoulQuery(t, reg, `[{"op":"filter","ext":"go"},{"op":"filter","pathContains":"AUTH"}]`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if strings.Contains(res.Output, "notes.txt") || strings.Contains(res.Output, "handler.go") {
		t.Errorf("filters should keep only Go files under auth:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "svc/auth/token.go") {
		t.Errorf("token.go should survive both filters:\n%s", res.Output)
	}

	res = runSoulQuery(t, reg, `[{"op":"filter","ext":".go"},{"op":"limit","n":1}]`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if got := strings.Count(res.Output, "\n  svc/"); got != 1 {
		t.Errorf("limit 1 should keep one file, got %d:\n%s", got, res.Output)
	}
}

func TestSoulQueryFindCombinesSymbolsAndFileNames(t *testing.T) {
	_, reg := queryMap(t)
	res := runSoulQuery(t, reg, `[{"op":"find","query":"TokenStore"}]`)
	if res.IsError || !strings.Contains(res.Output, "  svc/auth/token.go\n") {
		t.Errorf("find by symbol name should return the defining file:\n%s", res.Output)
	}
	res = runSoulQuery(t, reg, `[{"op":"find","query":"handler"}]`)
	if res.IsError || !strings.Contains(res.Output, "  svc/api/handler.go\n") {
		t.Errorf("find by file name should return the file:\n%s", res.Output)
	}
}

func TestSoulQueryOutlineIsTerminal(t *testing.T) {
	_, reg := queryMap(t)
	res := runSoulQuery(t, reg, `[{"op":"find","query":"TokenStore"},{"op":"outline"}]`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "svc/auth/token.go\n") {
		t.Errorf("outline should head each file:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "    L5-5  func (t *TokenStore) Refresh") {
		t.Errorf("methods should be nested under their type:\n%s", res.Output)
	}
}

func TestSoulQueryReadShowsRanges(t *testing.T) {
	_, reg := queryMap(t)
	res := runSoulQuery(t, reg, `[{"op":"find","query":"Trim"},{"op":"read","ranges":[{"start":3,"end":3}]}]`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "     3  func Trim") {
		t.Errorf("read should print the requested line with its number:\n%s", res.Output)
	}
	if strings.Contains(res.Output, "     1  package util") {
		t.Errorf("only the requested range should be printed:\n%s", res.Output)
	}
}

func TestSoulQueryReadSkipsBinaryAndHugeFiles(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module example.com/bin\n\ngo 1.25\n",
		"a/x.go": "package a\n\nfunc X() {}\n",
	}
	writeFixture(t, root, files)
	big := strings.Repeat("// padding line of text to make this file larger than the read limit\n", 5000)
	if err := os.WriteFile(filepath.Join(root, "a", "huge.go"), []byte("package a\n"+big), 0o644); err != nil {
		t.Fatal(err)
	}
	m := buildMap(t, root, Options{InMemory: true})
	reg := tools.NewRegistry()
	Register(reg, m)
	res := runSoulQuery(t, reg, `[{"op":"filter","pathContains":"a/"},{"op":"read"}]`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if !strings.Contains(res.Output, "a/huge.go: larger than") {
		t.Errorf("huge file should be skipped with a note:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "     3  func X() {}") {
		t.Errorf("the small Go file should still be read:\n%s", res.Output)
	}
}

func TestRenderReadSkipsBinaryFiles(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"blob.dat": "ab\x00cd"})
	m := &Map{root: root}
	out := renderRead(context.Background(), m, []string{"blob.dat"}, nil)
	if !strings.Contains(out, "blob.dat: binary, not shown") {
		t.Errorf("binary file should be skipped with a note:\n%s", out)
	}
}

func TestSoulQueryOutputIsBounded(t *testing.T) {
	root := t.TempDir()
	long := "package wide\n" + strings.Repeat("var v = \"0123456789012345678901234567890123456789012345678901234567890123456789\"\n", 200)
	writeFixture(t, root, map[string]string{
		"go.mod":      "module example.com/wide\n\ngo 1.25\n",
		"wide/big.go": long,
	})
	m := buildMap(t, root, Options{InMemory: true})
	reg := tools.NewRegistry()
	Register(reg, m)
	res := runSoulQuery(t, reg, `[{"op":"find","query":"big"},{"op":"read"}]`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if len(res.Output) > maxQueryOutput+200 {
		t.Errorf("output is %d bytes, want at most about %d", len(res.Output), maxQueryOutput)
	}
	if !strings.Contains(res.Output, "output cut") {
		t.Errorf("a cut answer must say so:\n%s", res.Output[len(res.Output)-200:])
	}
}

func TestSoulQueryRecentUsesTheModifiedTime(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, queryFixture)
	old := time.Now().Add(-72 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "svc/util/strs.go"), old, old); err != nil {
		t.Fatal(err)
	}
	m := buildMap(t, root, Options{InMemory: true})
	reg := tools.NewRegistry()
	Register(reg, m)
	res := runSoulQuery(t, reg, `[{"op":"filter","recent":true}]`)
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Output)
	}
	if strings.Contains(res.Output, "strs.go") {
		t.Errorf("a file older than 48 hours is not recent:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "svc/auth/token.go") {
		t.Errorf("a file changed just now is recent:\n%s", res.Output)
	}
}

func TestSoulQueryRejectsBadPipelines(t *testing.T) {
	_, reg := queryMap(t)
	long := strings.TrimSuffix(strings.Repeat(`{"op":"filter","ext":".go"},`, 13), ",")
	cases := map[string]string{
		`[]`:                                    "pipeline is empty",
		`[` + long + `]`:                        "limit is 12",
		`[{"op":"bogus"}]`:                      "unknown op",
		`[{"op":"search"}]`:                     "pattern is required",
		`[{"op":"find"}]`:                       "query is required",
		`[{"op":"filter"}]`:                     "filter needs",
		`[{"op":"deps","direction":"imports"}]`: "deps needs files to start from",
		`[{"op":"find","query":"x"},{"op":"deps"}]`:      "direction must be",
		`[{"op":"limit","n":0}]`:                         "n must be between 1 and 200",
		`[{"op":"outline"},{"op":"filter","ext":".go"}]`: "must be the last stage",
		`[{"op":"read","ranges":[{"start":0,"end":2}]}]`: "range 0-2 is invalid",
	}
	for pipeline, want := range cases {
		res := runSoulQuery(t, reg, pipeline)
		if !res.IsError || !strings.Contains(res.Output, want) {
			t.Errorf("pipeline %.60s: want error containing %q, got error=%v:\n%s", pipeline, want, res.IsError, res.Output)
		}
	}
	res := callTool(t, reg, "soul_query", `{}`)
	if !res.IsError {
		t.Errorf("a call without pipeline should fail, got %q", res.Output)
	}
}

func TestSoulQueryStopsWhenCancelled(t *testing.T) {
	m, _ := queryMap(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := runQuery(ctx, m, []queryStage{{Op: opSearch, Pattern: "Refresh"}})
	if err == nil {
		t.Fatal("a cancelled context should stop the search")
	}
}

func TestSoulQueryTraceCountsEachStage(t *testing.T) {
	m, _ := queryMap(t)
	res, err := runQuery(context.Background(), m, []queryStage{
		{Op: opFilter, Ext: ".go"},
		{Op: opFilter, PathContains: "svc/api"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Trace) != 2 {
		t.Fatalf("want one trace line per stage, got %d: %v", len(res.Trace), res.Trace)
	}
	if !strings.HasSuffix(res.Trace[1], "-> 1 files") {
		t.Errorf("second stage should leave one file: %q", res.Trace[1])
	}
	if len(res.Files) != 1 || res.Files[0] != "svc/api/handler.go" {
		t.Errorf("working set = %v, want [svc/api/handler.go]", res.Files)
	}
}
