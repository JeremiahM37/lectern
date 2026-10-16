package symedit

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// symedit writes go through the same secret-file rule the fs edit tools use:
// a secret file is refused outright, even in auto mode.
func TestCheckWritableRefusesSecretFiles(t *testing.T) {
	dir := t.TempDir()
	trk := newTracker()
	env := &tools.Env{Workdir: dir, Files: trk, Mode: "auto"}
	secret := filepath.Join(dir, ".env")
	writeFile(t, secret, "A=1\n")
	trk.MarkRead(secret)
	err := CheckWritable(context.Background(), env, secret)
	if err == nil || !strings.Contains(err.Error(), "secret file") {
		t.Fatalf(".env must be refused as a secret file, got %v", err)
	}
}
