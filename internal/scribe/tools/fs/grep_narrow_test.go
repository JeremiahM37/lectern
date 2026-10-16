package fs

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestRequiredLiteral(t *testing.T) {
	cases := map[string]string{
		"HandleRequest":      "HandleRequest",
		"func\\s+Foo":        "func",
		"foo.bar":            "foo",
		"a(bc)d":             "a",
		"ab?c":               "a",
		"x[abc]def":          "def",
		"foo|bar":            "",
		"\\.env":             ".env",
		"\\d+abc":            "abc",
		"(?i)Handle":         "Handle",
		"a{2}bcd":            "bcd",
		"\\pLhello":          "",
		"[[:alpha:]]literal": "literal",
		"é-unicode":          "-unicode",
		"ab":                 "ab",
		"(?:xyz|abc)":        "",
		"x{2,}longerrun":     "longerrun",
		"unterminated[abc":   "",
	}
	for pat, want := range cases {
		if got := requiredLiteral(pat); got != want {
			t.Errorf("requiredLiteral(%q) = %q, want %q", pat, got, want)
		}
	}
}

// fixtureTree writes a small repository with the cases narrowing must not
// get wrong: ignored and hidden paths, binary and non-ASCII content, a secret
// name, a file over the size cap, and files that do and do not match.
func fixtureTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		".gitignore":           "ignored/\n*.log\n",
		"main.go":              "package main\n\nfunc HandleRequest() {}\n// TODO: remove\n",
		"util/strings.go":      "package util\n\nvar handle_request = 1\nfunc Trim() {}\n",
		"util/Mixed.go":        "package util\n\n// HandleRequest in mixed case\n",
		"docs/guide.md":        "Use HandleRequest to serve. log.Printf is noisy.\n",
		"docs/plain.txt":       "nothing relevant in here\n",
		"ignored/skip.go":      "func HandleRequest() {}\n",
		"debug.log":            "HandleRequest logged\n",
		".hidden/secret.go":    "func HandleRequest() {}\n",
		".env":                 "HandleRequest=1\n",
		"bin/data.dat":         "HandleRequest\x00\x01\x02binary",
		"unicode/kelvin.txt":   "\u212Aeyword and handle_request\n",
		"fold/upper.txt":       "HANDLEREQUEST in capitals\n",
		"log/app.go":           "log.Printf(\"x\")\nfunc Foo() { x := 1; _ = x }\n",
		"nested/a/b/c/deep.go": "func deepFunc() {}\nvar TODO = 2\n",
	}
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	big := strings.Repeat("filler line of text\n", 20000) + "HandleRequest at the end\n"
	if err := os.WriteFile(filepath.Join(dir, "huge.txt"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func runGrepArgs(t *testing.T, env *tools.Env, args map[string]any, forceGo bool) string {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res := runGrep(context.Background(), env, raw, forceGo)
	if res.IsError {
		t.Fatalf("grep %v: %s", args, res.Output)
	}
	return res.Output
}

var enclosingNote = regexp.MustCompile(`  \[in [^\]]*\]`)

// sortedLines makes ripgrep output comparable: rg writes files in parallel
// order, so only the set of lines is meaningful across engines.
func sortedLines(s string) string {
	// The enclosing-symbol note appears only when a code map is present, and
	// the plain run has none, so it is not part of the narrowing contract.
	s = enclosingNote.ReplaceAllString(s, "")
	lines := strings.Split(s, "\n")
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// TestTrigramNarrowingMatchesUnnarrowed proves the narrowing is invisible: for
// every pattern, engine and output mode, grep with the code map's trigram
// index returns exactly what grep returns without it.
func TestTrigramNarrowingMatchesUnnarrowed(t *testing.T) {
	dir := fixtureTree(t)
	m, err := repomap.Open(dir, repomap.Options{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	plain := &tools.Env{Workdir: dir}
	narrowed := &tools.Env{Workdir: dir, Services: map[string]any{"repomap": m}}

	patterns := []string{
		"HandleRequest", "handle_request", "(?i)handlerequest", "TODO", "log\\.Printf",
		"func\\s+Foo", "deepFunc", "zzz-absent-literal", "foo|TODO", "\\.env",
		"Kelvin|keyword", "(?i)keyword", "(?i)handlerequest",
	}
	engines := []bool{true} // forceGo
	if _, err := exec.LookPath("rg"); err == nil {
		engines = append(engines, false)
	}
	narrowedSomewhere := false
	for _, pat := range patterns {
		if trigramSkip(narrowed, pat) != nil {
			narrowedSomewhere = true
		}
		for _, forceGo := range engines {
			for _, extra := range []map[string]any{
				{},
				{"glob": "*.go"},
				{"output_mode": "files"},
				{"output_mode": "count", "case_insensitive": true},
				{"path": "util"},
				{"path": "util/strings.go"},
			} {
				args := map[string]any{"pattern": pat}
				for k, v := range extra {
					args[k] = v
				}
				want := runGrepArgs(t, plain, args, forceGo)
				got := runGrepArgs(t, narrowed, args, forceGo)
				if sortedLines(got) != sortedLines(want) {
					t.Errorf("pattern %q args %v forceGo=%v:\nnarrowed:\n%s\nunnarrowed:\n%s", pat, args, forceGo, got, want)
				}
			}
		}
	}
	if !narrowedSomewhere {
		t.Fatal("no pattern was narrowed; the test proves nothing")
	}
}

// TestTrigramNarrowingFallsBackSilently checks that a missing index, a stale
// one, and a pattern without a usable literal all give the unnarrowed answer.
func TestTrigramNarrowingFallsBackSilently(t *testing.T) {
	dir := fixtureTree(t)
	plain := &tools.Env{Workdir: dir}
	args := map[string]any{"pattern": "HandleRequest", "output_mode": "files"}
	want := runGrepArgs(t, plain, args, true)

	// No code map at all.
	if trigramSkip(plain, "HandleRequest") != nil {
		t.Fatal("no service must mean no narrowing")
	}
	// A code map that was never built.
	m, err := repomap.Open(dir, repomap.Options{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	empty := &tools.Env{Workdir: dir, Services: map[string]any{"repomap": m}}
	if got := runGrepArgs(t, empty, args, true); got != want {
		t.Fatalf("unbuilt index changed the answer:\n%s\nwant\n%s", got, want)
	}
	// A pattern with no literal run.
	if got := runGrepArgs(t, empty, map[string]any{"pattern": "a|b", "output_mode": "files"}, true); got != runGrepArgs(t, plain, map[string]any{"pattern": "a|b", "output_mode": "files"}, true) {
		t.Fatal("alternation changed the answer")
	}
}
