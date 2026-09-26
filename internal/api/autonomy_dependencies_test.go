package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDependencyCatalogRequiresMatchingVerifiedManifest(t *testing.T) {
	root := t.TempDir()
	key := strings.Repeat("a", 64)
	dir := filepath.Join(root, key)
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "manifest.json")
	write := func(body string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"key":"` + key + `","module":"example.org/project","checksum_verified":false}`)
	if len(autoDependencyCatalog(root)) != 0 {
		t.Fatal("unverified advertised")
	}
	write(`{"key":"wrong","module":"example.org/project","checksum_verified":true}`)
	if len(autoDependencyCatalog(root)) != 0 {
		t.Fatal("wrong source advertised")
	}
	write(`{"key":"` + key + `","module":"example.org/project","checksum_verified":true,"project_id":29}`)
	rows := autoDependencyCatalog(root)
	if len(rows) != 1 || rows[0].ProjectID != 29 {
		t.Fatalf("missing verified bundle: %+v", rows)
	}
	if err := os.Rename(path, path+".original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path+".original", path); err != nil {
		t.Fatal(err)
	}
	if len(autoDependencyCatalog(root)) != 0 {
		t.Fatal("symlink manifest advertised")
	}
}
