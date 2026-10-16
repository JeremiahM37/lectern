package fs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// A project's .aiignore lists forbidden paths in gitignore style: the file
// tools refuse them as they refuse the built-in forbidden names.
func TestAiignoreMakesPathsForbidden(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".aiignore"), []byte("# notes\n\nprivate/\n/vault.txt\n!keep.txt\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := &tools.Env{Workdir: root}
	pats, err := Patterns(env)
	if err != nil {
		t.Fatal(err)
	}
	rules, err := compileForbidden(pats)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"private", "vault.txt", "sub/private"} {
		if matchForbidden(rules, filepath.Join(root, p)) == nil {
			t.Errorf("%s should be forbidden by .aiignore", p)
		}
	}
	if matchForbidden(rules, filepath.Join(root, "keep.txt")) != nil {
		t.Error("a negated line must not re-open a path")
	}
	if matchForbidden(rules, filepath.Join(root, "main.go")) != nil {
		t.Error("an unlisted path must stay allowed")
	}
}
