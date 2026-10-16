package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An auto-fix edit to a secret file or a CI definition must be refused by the
// write rules, not applied. The fixer has no tab, so it has no host to ask.
func TestSourceFixWritesCheckedAgainstRules(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{".env", ".github/workflows/ci.yml"} {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("A=1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m := NewManager(dir)
	for _, name := range []string{".env", ".github/workflows/ci.yml"} {
		uri := "file://" + filepath.ToSlash(filepath.Join(dir, name))
		edit := WorkspaceEdit{Changes: map[string][]TextEdit{uri: {{NewText: "B=2\n"}}}}
		err := CheckEditRules(context.Background(), m.writeEnv(), edit)
		if err == nil {
			t.Fatalf("%s: auto-fix write was not refused", name)
		}
		if got, _ := os.ReadFile(filepath.Join(dir, name)); strings.Contains(string(got), "B=2") {
			t.Fatalf("%s was changed despite the refusal", name)
		}
	}
}
