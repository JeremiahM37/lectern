package repomap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// Generation moves when the graph changes, and stays put when nothing does.
func TestGenerationMovesOnUpdate(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc A() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, err := Open(root, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if err := m.Build(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	g0 := m.Generation()
	if g0 == 0 {
		t.Fatal("a build should advance the generation")
	}
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc A() {}\nfunc B() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := m.Update([]string{"a.go"}); err != nil {
		t.Fatal(err)
	}
	if g1 := m.Generation(); g1 == g0 {
		t.Fatal("an update should advance the generation")
	}
	if g := m.Generation(); g != m.Generation() {
		t.Fatal("reading the generation must not change it")
	}
}
