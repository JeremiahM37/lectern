package fs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestIgnoreMatchingTable(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		rules   string
		path    string
		isDir   bool
		ignored bool
	}{
		{"star matches at any depth", "", "*.log", "a.log", false, true},
		{"star in subdir", "", "*.log", "x/y/a.log", false, true},
		{"star not partial name", "", "*.log", "a.logx", false, false},
		{"dir-only matches dir", "", "build/", "build", true, true},
		{"dir-only ignores file", "", "build/", "build", false, false},
		{"contents of ignored dir", "", "build/", "build/x.o", false, true},
		{"anchored root match", "", "/root.txt", "root.txt", false, true},
		{"anchored root not nested", "", "/root.txt", "sub/root.txt", false, false},
		{"slash pattern anchored", "", "doc/*.md", "doc/a.md", false, true},
		{"slash pattern not elsewhere", "", "doc/*.md", "x/doc/a.md", false, false},
		{"star does not cross slash", "", "doc/*.md", "doc/sub/a.md", false, false},
		{"double star leading", "", "**/foo", "foo", false, true},
		{"double star leading nested", "", "**/foo", "a/b/foo", false, true},
		{"double star middle", "", "a/**/b", "a/b", false, true},
		{"double star middle deep", "", "a/**/b", "a/x/y/b", false, true},
		{"double star middle not bare", "", "a/**/b", "b", false, false},
		{"trailing double star", "", "a/**", "a/x", false, true},
		{"trailing double star not dir itself", "", "a/**", "a", true, false},
		{"negation re-includes", "", "*.log\n!keep.log", "keep.log", false, false},
		{"negation other still ignored", "", "*.log\n!keep.log", "other.log", false, true},
		{"comment and blank", "", "# note\n\n*.tmp", "x.tmp", false, true},
		{"escaped hash", "", `\#x`, "#x", false, true},
		{"question mark", "", "file?.txt", "file1.txt", false, true},
		{"bracket class", "", "[ab].txt", "a.txt", false, true},
		{"bracket class miss", "", "[ab].txt", "c.txt", false, false},
		{"bare name matches dir", "", "foo", "x/foo", true, true},
		{"nested base applies below", "sub", "*.tmp", "sub/a.tmp", false, true},
		{"nested base not above", "sub", "*.tmp", "a.tmp", false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ig := (*Ignore)(nil).Extend(c.base, []byte(c.rules))
			if got := ig.Ignored(c.path, c.isDir); got != c.ignored {
				t.Fatalf("Ignored(%q, dir=%v) = %v, want %v", c.path, c.isDir, got, c.ignored)
			}
		})
	}
}

func TestIgnoreNestedOverridesRoot(t *testing.T) {
	root := (*Ignore)(nil).Extend("", []byte("*.tmp\n"))
	sub := root.Extend("sub", []byte("!keep.tmp\n"))
	if !sub.Ignored("other.tmp", false) {
		t.Fatal("root rule should still ignore other.tmp")
	}
	if sub.Ignored("sub/keep.tmp", false) {
		t.Fatal("nested negation should re-include sub/keep.tmp")
	}
	if !root.Ignored("sub/keep.tmp", false) {
		t.Fatal("root alone must not see the nested negation")
	}
}

func TestListDirDirsFirstSizesAndGitignore(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "b.txt", "12345")
	write(t, root, "src/main.go", "package main\n")
	write(t, root, "src/util/u.go", "x")
	write(t, root, "ignored/skip.txt", "x")
	write(t, root, "app.log", "x")
	write(t, root, ".gitignore", "ignored/\n*.log\n")
	write(t, root, "node_modules/pkg/i.js", "x")
	r := mustOK(t, call(t, NewListDir(), env, map[string]any{}))
	if !strings.HasPrefix(r.Output, "./\n  src/\n") {
		t.Fatalf("dirs should come first:\n%s", r.Output)
	}
	if strings.Contains(r.Output, "ignored") || strings.Contains(r.Output, "app.log") || strings.Contains(r.Output, "node_modules") {
		t.Fatalf("ignored entries listed:\n%s", r.Output)
	}
	if !strings.Contains(r.Output, "  b.txt  5B") {
		t.Fatalf("missing size: %s", r.Output)
	}
	r = mustOK(t, call(t, NewListDir(), env, map[string]any{"path": "src", "depth": 2}))
	if !strings.Contains(r.Output, "  util/\n    u.go") {
		t.Fatalf("depth 2 nesting:\n%s", r.Output)
	}
	r = mustOK(t, call(t, NewListDir(), env, map[string]any{"path": "src", "depth": 1}))
	if strings.Contains(r.Output, "u.go") {
		t.Fatalf("depth 1 should not descend:\n%s", r.Output)
	}
	mustErr(t, call(t, NewListDir(), env, map[string]any{"path": "b.txt"}), "not a directory")
}

func TestListDirNestedGitignore(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, "pkg/.gitignore", "gen.go\n")
	write(t, root, "pkg/gen.go", "x")
	write(t, root, "pkg/real.go", "x")
	r := mustOK(t, call(t, NewListDir(), env, map[string]any{"path": "pkg", "depth": 1}))
	if strings.Contains(r.Output, "gen.go") || !strings.Contains(r.Output, "real.go") {
		t.Fatalf("nested .gitignore not applied:\n%s", r.Output)
	}
}

func TestGlobRecursiveNewestFirst(t *testing.T) {
	env, root, _, _ := newEnv(t)
	old := write(t, root, "a/old_test.go", "x")
	write(t, root, "b/new_test.go", "x")
	write(t, root, "b/plain.go", "x")
	past := time.Now().Add(-48 * time.Hour)
	os.Chtimes(old, past, past)
	r := mustOK(t, call(t, NewGlob(), env, map[string]any{"pattern": "**/*_test.go"}))
	lines := strings.Split(r.Output, "\n")
	if len(lines) != 2 || lines[0] != "b/new_test.go" || lines[1] != "a/old_test.go" {
		t.Fatalf("order: %v", lines)
	}
	r = mustOK(t, call(t, NewGlob(), env, map[string]any{"pattern": "*.go", "path": "b"}))
	if strings.Count(r.Output, "\n") != 1 {
		t.Fatalf("path-scoped glob: %q", r.Output)
	}
	r = mustOK(t, call(t, NewGlob(), env, map[string]any{"pattern": "*.rs"}))
	if r.Output != "No files found." {
		t.Fatalf("empty glob: %q", r.Output)
	}
}

func TestGlobRespectsGitignoreAndCaps(t *testing.T) {
	env, root, _, _ := newEnv(t)
	write(t, root, ".gitignore", "vendor/\n")
	write(t, root, "vendor/x.go", "x")
	write(t, root, "keep.go", "x")
	r := mustOK(t, call(t, NewGlob(), env, map[string]any{"pattern": "**/*.go"}))
	if strings.Contains(r.Output, "vendor") || r.Output != "keep.go" {
		t.Fatalf("gitignore not applied: %q", r.Output)
	}
	for i := 0; i < maxGlobResults+5; i++ {
		write(t, root, filepath.Join("many", "f"+strings.Repeat("x", i%7)+string(rune('a'+i%26))+itoa(i)+".txt"), "x")
	}
	r = mustOK(t, call(t, NewGlob(), env, map[string]any{"pattern": "many/*.txt"}))
	if !strings.Contains(r.Output, "Showing 500 of 505") {
		t.Fatalf("cap note missing: %q", r.Output[len(r.Output)-120:])
	}
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

func TestChainIgnoreLoadsAncestors(t *testing.T) {
	_, root, _, _ := newEnv(t)
	write(t, root, ".gitignore", "*.gen\n")
	write(t, root, "a/b/.gitignore", "!keep.gen\n")
	ig := chainIgnore(root, filepath.Join(root, "a", "b"))
	if ig.Ignored("a/b/x.gen", false) != true || ig.Ignored("a/b/keep.gen", false) {
		t.Fatal("chain did not load a/b/.gitignore over root rules")
	}
	if chainIgnore(root, t.TempDir()) != nil {
		t.Fatal("dirs outside the workdir should get no rules")
	}
}

func TestWalkStopsOnContextCancel(t *testing.T) {
	_, root, _, _ := newEnv(t)
	write(t, root, "a.txt", "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if walkDir(ctx, root, root, nil, 1, -1, func(string, string, os.FileInfo) bool { return true }) {
		t.Fatal("walk should report it was stopped")
	}
}
