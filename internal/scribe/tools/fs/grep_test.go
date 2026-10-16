package fs

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func setupGrepTree(t *testing.T) (*tools.Env, string) {
	t.Helper()
	env, root, _, _ := newEnv(t)
	write(t, root, "main.go", "package main\n\nfunc Hello() {}\n\nfunc main() { Hello() }\n")
	write(t, root, "pkg/lib.go", "package pkg\n\n// hello is here\nvar x = 1\n")
	write(t, root, "notes.txt", "nothing relevant\n")
	write(t, root, ".gitignore", "generated/\n")
	write(t, root, "generated/g.go", "func Hello() {}\n")
	write(t, root, ".env", "Hello=secret\n")
	write(t, root, "bin.dat", "Hello\x00binary")
	return env, root
}

// grepTools returns the Go-walker and (if installed) ripgrep variants.
func grepTools() map[string]tools.Tool {
	m := map[string]tools.Tool{"go": newGrep(true)}
	if _, err := exec.LookPath("rg"); err == nil {
		m["rg"] = newGrep(false)
	}
	return m
}

func TestGrepContentBothBackends(t *testing.T) {
	env, _ := setupGrepTree(t)
	for name, g := range grepTools() {
		t.Run(name, func(t *testing.T) {
			r := mustOK(t, call(t, g, env, map[string]any{"pattern": "Hello"}))
			for _, want := range []string{"main.go:3:func Hello() {}", "main.go:5:func main() { Hello() }"} {
				if !strings.Contains(r.Output, want) {
					t.Fatalf("missing %q in:\n%s", want, r.Output)
				}
			}
			if strings.Contains(r.Output, "pkg/lib.go") {
				t.Fatalf("case-sensitive search matched lowercase text:\n%s", r.Output)
			}
			if strings.Contains(r.Output, "generated") || strings.Contains(r.Output, ".env") || strings.Contains(r.Output, "bin.dat") {
				t.Fatalf("ignored, secret or binary file searched:\n%s", r.Output)
			}
			if r.Summary != "2 matches in 1 files" {
				t.Fatalf("summary %q", r.Summary)
			}
		})
	}
}

func TestGrepCaseInsensitiveContextAndSeparators(t *testing.T) {
	env, _ := setupGrepTree(t)
	for name, g := range grepTools() {
		t.Run(name, func(t *testing.T) {
			r := mustOK(t, call(t, g, env, map[string]any{
				"pattern": "hello", "case_insensitive": true, "glob": "*.go", "context": 1, "path": "pkg"}))
			if !strings.Contains(r.Output, "pkg/lib.go:3:// hello is here") || !strings.Contains(r.Output, "pkg/lib.go-4-") {
				t.Fatalf("context output:\n%s", r.Output)
			}
			if strings.Contains(r.Output, "main.go") {
				t.Fatalf("path scope ignored:\n%s", r.Output)
			}
		})
	}
}

func TestGrepFilesAndCountModes(t *testing.T) {
	env, _ := setupGrepTree(t)
	for name, g := range grepTools() {
		t.Run(name, func(t *testing.T) {
			r := mustOK(t, call(t, g, env, map[string]any{"pattern": "Hello", "output_mode": "files"}))
			if r.Output != "main.go" {
				t.Fatalf("files mode: %q", r.Output)
			}
			r = mustOK(t, call(t, g, env, map[string]any{"pattern": "Hello", "output_mode": "count"}))
			if r.Output != "main.go:2" {
				t.Fatalf("count mode: %q", r.Output)
			}
		})
	}
}

func TestGrepMaxResultsAndNoMatch(t *testing.T) {
	env, root := setupGrepTree(t)
	var sb strings.Builder
	for i := 0; i < 10; i++ {
		sb.WriteString("hit\n")
	}
	write(t, root, "many.txt", sb.String())
	for name, g := range grepTools() {
		t.Run(name, func(t *testing.T) {
			r := mustOK(t, call(t, g, env, map[string]any{"pattern": "hit", "max_results": 3}))
			if strings.Count(r.Output, "many.txt:") != 3 || !strings.Contains(r.Output, "Stopped at max_results=3") {
				t.Fatalf("max_results:\n%s", r.Output)
			}
			r = mustOK(t, call(t, g, env, map[string]any{"pattern": "absent_zzz"}))
			if r.Output != "No matches." {
				t.Fatalf("no match: %q", r.Output)
			}
		})
	}
}

func TestGrepSingleFileAndErrors(t *testing.T) {
	env, _ := setupGrepTree(t)
	for name, g := range grepTools() {
		t.Run(name, func(t *testing.T) {
			r := mustOK(t, call(t, g, env, map[string]any{"pattern": "Hello", "path": "main.go", "output_mode": "count"}))
			if r.Output != "main.go:2" {
				t.Fatalf("single file: %q", r.Output)
			}
			mustErr(t, call(t, g, env, map[string]any{"pattern": "("}), "invalid pattern")
			mustErr(t, call(t, g, env, map[string]any{"pattern": "x", "output_mode": "bogus"}), "output_mode")
			mustErr(t, call(t, g, env, map[string]any{}), "pattern is required")
		})
	}
}

func TestGrepHiddenFilesSkippedByGoWalker(t *testing.T) {
	env, root := setupGrepTree(t)
	write(t, root, ".hidden/x.go", "Hello\n")
	r := mustOK(t, call(t, newGrep(true), env, map[string]any{"pattern": "Hello", "output_mode": "files"}))
	if strings.Contains(r.Output, ".hidden") {
		t.Fatalf("hidden dir searched: %s", r.Output)
	}
}

func TestRgRelPath(t *testing.T) {
	wd := filepath.Join(string(filepath.Separator), "proj")
	if got := rgRel(wd, filepath.Join(wd, "a", "b.go")); got != "a/b.go" {
		t.Fatalf("abs: %q", got)
	}
	if got := rgRel(wd, "./a/b.go"); got != "a/b.go" {
		t.Fatalf("rel: %q", got)
	}
}

func TestSearchWorksWithoutRgOnPath(t *testing.T) {
	// The Go walker must not depend on rg: run it with PATH emptied.
	env, _ := setupGrepTree(t)
	t.Setenv("PATH", t.TempDir())
	r := mustOK(t, call(t, NewGrep(), env, map[string]any{"pattern": "func main", "output_mode": "files"}))
	if r.Output != "main.go" {
		t.Fatalf("fallback: %q", r.Output)
	}
}
