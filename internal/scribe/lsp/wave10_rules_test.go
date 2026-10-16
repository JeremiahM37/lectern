package lsp

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// A refactor's workspace edit is checked against the write rules before any
// file changes, so an edit that reaches a .env file changes nothing.
func TestRefactorEditRefusesSecretFilesBeforeWriting(t *testing.T) {
	dir := t.TempDir()
	env := &tools.Env{Workdir: dir, Mode: "auto"}
	good := filepath.Join(dir, "a.go")
	secret := filepath.Join(dir, ".env")
	for _, p := range []string{good, secret} {
		if err := os.WriteFile(p, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	edit := WorkspaceEdit{Changes: map[string][]TextEdit{
		pathToURI(good):   {{Range: rng(0, 0, 0, 1), NewText: "y"}},
		pathToURI(secret): {{Range: rng(0, 0, 0, 1), NewText: "SECRET=1"}},
	}}

	if err := CheckEditRules(context.Background(), env, edit); err == nil || !strings.Contains(err.Error(), "secret file") {
		t.Fatalf("an edit to .env must be refused, got %v", err)
	}
	if _, err := applyChecked(context.Background(), env, edit, nil); err == nil {
		t.Fatal("applyChecked must refuse the whole edit")
	}
	if b, _ := os.ReadFile(good); string(b) != "x\n" {
		t.Fatalf("no file may change when one is refused, a.go = %q", b)
	}

	ok := WorkspaceEdit{Changes: map[string][]TextEdit{pathToURI(good): {{Range: rng(0, 0, 0, 1), NewText: "z"}}}}
	if err := CheckEditRules(context.Background(), env, ok); err != nil {
		t.Fatalf("an ordinary source edit should pass: %v", err)
	}
}
