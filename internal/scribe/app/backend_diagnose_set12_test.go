package app

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/repomap"
)

// The lookup probe must pass on a real map: the name it picks comes from the
// map itself, and a search for it must return the same symbol in the same file.
func TestLookupProbeFindsItsOwnFunction(t *testing.T) {
	dir := t.TempDir()
	src := "package demo\n\nfunc ParseListWidget() int { return 1 }\n"
	if err := os.WriteFile(filepath.Join(dir, "demo.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := repomap.Open(dir, repomap.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Build(t.Context(), nil); err != nil {
		t.Fatal(err)
	}
	c := lookupProbe(m)
	if c.State != "ok" {
		t.Fatalf("lookup probe should pass on its own map, got %+v", c)
	}
}
