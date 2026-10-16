package repomap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildRefusesHomeAndFilesystemRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	m, err := Open(home, Options{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Build(context.Background(), nil); err != ErrUnsafeRoot {
		t.Fatalf("home directory: Build = %v, want ErrUnsafeRoot", err)
	}
	if n := m.Stats().Files; n != 0 {
		t.Errorf("home directory was indexed: %d files", n)
	}

	fsRoot, err := Open(string(filepath.Separator), Options{InMemory: true})
	if err != nil {
		t.Fatal(err)
	}
	defer fsRoot.Close()
	if err := fsRoot.Build(context.Background(), nil); err != ErrUnsafeRoot {
		t.Fatalf("filesystem root: Build = %v, want ErrUnsafeRoot", err)
	}
}

func TestEnclosingSymbolIsInnermost(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	syms, ok := m.FileSymbols("internal/api/server.go")
	if !ok {
		t.Fatal("server.go not in the map")
	}
	var start Symbol
	for _, s := range syms {
		if s.Name == "Start" {
			start = s
		}
	}
	if start.Line == 0 {
		t.Fatal("Start not found")
	}
	got, ok := m.EnclosingSymbol("internal/api/server.go", start.Line+1)
	if !ok || got.Name != "Start" {
		t.Errorf("line inside Start: got %q, %v; want Start", got.Name, ok)
	}
	if _, ok := m.EnclosingSymbol("internal/api/server.go", 1); ok {
		t.Error("the package clause should have no enclosing symbol")
	}
}

func TestNewTagFollowsModificationTime(t *testing.T) {
	root := newFixtureRepo(t)
	old := time.Now().Add(-10 * 24 * time.Hour)
	util := filepath.Join(root, "internal/util/util.go")
	if err := os.Chtimes(util, old, old); err != nil {
		t.Fatal(err)
	}
	fresh := filepath.Join(root, "internal/worker/worker.go")
	if err := os.Chtimes(fresh, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	m := buildMap(t, root, Options{InMemory: true})
	out := m.Render(4000, nil)
	if strings.Contains(out, "internal/util/util.go [NEW]") {
		t.Errorf("a file untouched for 10 days is tagged [NEW]:\n%s", out)
	}
	if !strings.Contains(out, "internal/worker/worker.go") || !strings.Contains(out, "worker.go [NEW]") {
		t.Errorf("a file changed just now is not tagged [NEW]:\n%s", out)
	}
}

func TestRecheckReindexesChangedFiles(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	if n, err := m.Recheck(); err != nil || n != 0 {
		t.Fatalf("clean tree: Recheck = %d, %v; want 0, nil", n, err)
	}
	path := filepath.Join(root, "internal/util/util.go")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(src, []byte("\nfunc Fresh() int { return 1 }\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(path, later, later); err != nil {
		t.Fatal(err)
	}
	n, err := m.Recheck()
	if err != nil || n != 1 {
		t.Fatalf("after one edit: Recheck = %d, %v; want 1, nil", n, err)
	}
	if defs := m.Definition("Fresh"); len(defs) != 1 {
		t.Errorf("new function not indexed: %v", defs)
	}
}

func TestRenderedPathsRecordTheLastRender(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	if got := m.RenderedPaths(); len(got) != 0 {
		t.Fatalf("before any render: %v", got)
	}
	out := m.Render(2500, nil)
	got := m.RenderedPaths()
	if len(got) == 0 {
		t.Fatal("render recorded no paths")
	}
	for _, p := range got {
		if !strings.Contains(out, p) {
			t.Errorf("recorded path %s is not in the rendered map", p)
		}
	}
}

func TestBudgetScalesWithConversation(t *testing.T) {
	cases := map[int]int{
		0:      2500,
		1000:   2500,
		50000:  3500, // 1500 + 2500 * (1 - 0.2)
		100000: 3000, // floor of 60% of the span
		900000: 3000,
	}
	for tokens, want := range cases {
		if got := Budget(tokens); got != want {
			t.Errorf("Budget(%d) = %d, want %d", tokens, got, want)
		}
	}
}

func TestImpactListsWhatAFileImports(t *testing.T) {
	root := newFixtureRepo(t)
	m := buildMap(t, root, Options{InMemory: true})
	im, err := m.Impact("internal/api/server.go")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range im.Imports {
		if p == "internal/util/util.go" {
			found = true
		}
	}
	if !found {
		t.Errorf("server.go imports util, but Imports = %v", im.Imports)
	}
}

func TestRenderKeepsSmallBlocksAfterDirectoryCap(t *testing.T) {
	root := t.TempDir()
	files := map[string]string{"go.mod": "module example.com/many\n\ngo 1.25\n"}
	for i := 0; i < 7; i++ {
		files[filepath.ToSlash(filepath.Join("pkg", "p"+string(rune('a'+i))+".go"))] =
			"package pkg\n\nfunc Exported" + string(rune('A'+i)) + "() int { return 1 }\n"
	}
	files["other/o.go"] = "package other\n\nfunc Lonely() int { return 2 }\n"
	writeFixture(t, root, files)
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "add", ".")
	gitRun(t, root, "-c", "user.email=a@b", "-c", "user.name=t", "commit", "-qm", "init")
	m := buildMap(t, root, Options{InMemory: true})
	out := m.Render(2500, nil)
	if !strings.Contains(out, "other/o.go") {
		t.Errorf("a file in a second directory was dropped:\n%s", out)
	}
	for i := 0; i < 7; i++ {
		name := "pkg/p" + string(rune('a'+i)) + ".go"
		if !strings.Contains(out, name) {
			t.Errorf("%s dropped from a within-budget map:\n%s", name, out)
		}
	}
}

func TestGitRecencyRanksLatestChangesFirst(t *testing.T) {
	root := t.TempDir()
	writeFixture(t, root, map[string]string{"old.go": "package a\n", "new.go": "package a\n"})
	gitRun(t, root, "init", "-q")
	gitRun(t, root, "add", "old.go")
	gitRun(t, root, "-c", "user.email=a@b", "-c", "user.name=t", "commit", "-qm", "old")
	gitRun(t, root, "add", "new.go")
	gitRun(t, root, "-c", "user.email=a@b", "-c", "user.name=t", "commit", "-qm", "new")
	rank := gitRecency(context.Background(), root)
	if rank == nil {
		t.Fatal("no recency ranking from a repository with history")
	}
	if rank["new.go"] >= rank["old.go"] {
		t.Errorf("new.go (rank %d) should rank ahead of old.go (rank %d)", rank["new.go"], rank["old.go"])
	}
}

func TestWalkReportsNothingWhenComplete(t *testing.T) {
	root := newFixtureRepo(t)
	files, note := walkFiles(root)
	if note != "" {
		t.Errorf("complete walk reported %q", note)
	}
	if len(files) == 0 {
		t.Error("walk found no files")
	}
}
