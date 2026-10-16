package fs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// glob and list_dir put the files the code map ranks highest first. Without a
// map they keep the old order (newest first for glob, by name for list_dir).
func TestGlobAndListDirOrderByMapRank(t *testing.T) {
	env, root, _, _ := newEnv(t)
	// hub.go is called by both others, so it ranks highest in the map.
	write(t, root, "hub.go", "package p\n\nfunc Hub() int { return 1 }\n")
	write(t, root, "a.go", "package p\n\nfunc A() int { return Hub() }\n")
	write(t, root, "b.go", "package p\n\nfunc B() int { return Hub() + A() }\n")
	// Make hub.go the oldest file, so mtime alone would put it last.
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "hub.go"), old, old); err != nil {
		t.Fatal(err)
	}

	plain := mustOK(t, call(t, NewGlob(), env, map[string]any{"pattern": "*.go"}))
	if strings.HasPrefix(plain.Output, "hub.go") {
		t.Fatalf("without a map hub.go is the oldest and should not lead: %q", plain.Output)
	}

	buildMap(t, env, root)
	ranked := mustOK(t, call(t, NewGlob(), env, map[string]any{"pattern": "*.go"}))
	if !strings.HasPrefix(ranked.Output, "hub.go") {
		t.Fatalf("with a map hub.go should lead the glob: %q", ranked.Output)
	}

	l := mustOK(t, call(t, NewListDir(), env, map[string]any{}))
	if !strings.Contains(l.Output, "hub.go") {
		t.Fatalf("list_dir lost hub.go: %q", l.Output)
	}
	iHub := strings.Index(l.Output, "hub.go")
	iA := strings.Index(l.Output, "a.go")
	if iHub < 0 || iA < 0 || iHub > iA {
		t.Fatalf("list_dir should put the ranked hub.go before a.go:\n%s", l.Output)
	}
}
