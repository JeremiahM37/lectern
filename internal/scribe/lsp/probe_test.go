package lsp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestProbeUnsupportedFile(t *testing.T) {
	m := NewManager(t.TempDir())
	res := m.Probe(context.Background(), filepath.Join(t.TempDir(), "notes.unknownext"))
	if !res.Unsupported || res.Passed() {
		t.Fatalf("file with no server = %+v, want unsupported", res)
	}
}

func TestProbeFailsWhenServerMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("PATH", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	m := NewManager(dir)
	defer m.Shutdown()
	res := m.Probe(context.Background(), filepath.Join(dir, "main.go"))
	if res.InitErr == nil || res.Passed() {
		t.Fatalf("no gopls on PATH: probe = %+v, want an init error", res)
	}
}

func TestProbePassesAgainstServer(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n\nfunc F() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := NewManager(dir)
	abs := filepath.Join(dir, "a.go")
	startFakeLSP(t, m, abs, map[string]func(json.RawMessage) any{
		"textDocument/documentSymbol": func(json.RawMessage) any {
			return []map[string]any{{"name": "F", "kind": 12, "range": map[string]any{
				"start": map[string]int{"line": 2, "character": 0}, "end": map[string]int{"line": 2, "character": 11}},
				"selectionRange": map[string]any{
					"start": map[string]int{"line": 2, "character": 5}, "end": map[string]int{"line": 2, "character": 6}}}}
		},
	})
	res := m.Probe(context.Background(), abs)
	if !res.Passed() || res.Symbols != 1 || res.Language != "go" {
		t.Fatalf("probe = %+v, want one symbol and a pass", res)
	}
}

func TestSampleFileSkipsHiddenAndDependencyFolders(t *testing.T) {
	dir := t.TempDir()
	for _, p := range []string{"node_modules/x.ts", ".git/hooks/y.ts", "src/app.ts", "main.go"} {
		full := filepath.Join(dir, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, ok := SampleFile(dir, "typescript")
	if !ok || got != filepath.Join(dir, "src", "app.ts") {
		t.Fatalf("SampleFile = %q, %v; want src/app.ts", got, ok)
	}
	if _, ok := SampleFile(dir, "rust"); ok {
		t.Fatal("no rust file exists, so no sample")
	}
}
