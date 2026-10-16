package fs

import (
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestCheckForbiddenAppliesSessionPatterns(t *testing.T) {
	dir := t.TempDir()
	env := &tools.Env{Workdir: dir, Services: map[string]any{"forbidden": []string{"*.env"}}}
	if err := CheckForbidden(env, filepath.Join(dir, "prod.env")); err == nil {
		t.Fatal("a forbidden file was allowed")
	}
	if err := CheckForbidden(env, filepath.Join(dir, "main.go")); err != nil {
		t.Fatalf("an allowed file was refused: %v", err)
	}
}
