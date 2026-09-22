package sessions

import (
	"context"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspaceReservationsProtectOnlyRelatedPaths(t *testing.T) {
	m := &Manager{}
	setup, err := m.reserveWorkspacePaths(1, false, "/repos/source", "/work/slow")
	if err != nil {
		t.Fatal(err)
	}
	defer m.releaseWorkspacePaths(setup)
	for _, tc := range []struct {
		target int64
		dir    string
	}{{1, "/work/other"}, {1, "/work/slower"}, {2, "/work/slow"}} {
		cleanup, err := m.reserveWorkspacePaths(tc.target, true, tc.dir)
		if err != nil {
			t.Fatalf("unrelated cleanup blocked: %v", err)
		}
		m.releaseWorkspacePaths(cleanup)
	}
	for _, dir := range []string{"/work/slow", "/work/slow/child", "/work", "/repos/source"} {
		if _, err := m.reserveWorkspacePaths(1, true, dir); err == nil {
			t.Fatalf("cleanup raced setup at %s", dir)
		}
	}
	// Concurrent launches may share a workspace; removal must wait for all of them.
	shared, err := m.reserveWorkspacePaths(1, false, "/work/slow")
	if err != nil {
		t.Fatal(err)
	}
	m.releaseWorkspacePaths(setup)
	if _, err := m.reserveWorkspacePaths(1, true, "/work/slow"); err == nil {
		t.Fatal("shared launch lost protection")
	}
	m.releaseWorkspacePaths(shared)
	cleanup, err := m.reserveWorkspacePaths(1, true, "/work/slow")
	if err != nil {
		t.Fatal(err)
	}
	defer m.releaseWorkspacePaths(cleanup)
	if _, err := m.reserveWorkspacePaths(1, false, "/work/slow/child"); err == nil {
		t.Fatal("launch raced cleanup")
	}
	if _, err := m.reserveWorkspacePaths(1, true, "/work/slow"); err == nil {
		t.Fatal("duplicate cleanup raced")
	}
	other, err := m.reserveWorkspacePaths(1, false, "/repos/other")
	if err != nil {
		t.Fatal(err)
	}
	defer m.releaseWorkspacePaths(other)
	if err := m.extendWorkspacePaths(other, "/work/slow"); err == nil {
		t.Fatal("planned destination raced cleanup")
	}
}

func TestWorkspaceReservationsResolveTargetSymlinks(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	alias := filepath.Join(root, "alias")
	if err := os.Mkdir(real, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, alias); err != nil {
		t.Fatal(err)
	}
	ex := executor.NewLocal()
	source, err := canonicalWorkspaceSource(context.Background(), ex, alias)
	if err != nil || source != real {
		t.Fatalf("source alias: %q %v", source, err)
	}
	allocation, err := canonicalWorkspaceAllocation(context.Background(), ex, filepath.Join(alias, "future"))
	if err != nil || allocation != filepath.Join(real, "future") {
		t.Fatalf("allocation alias: %q %v", allocation, err)
	}
	m := &Manager{}
	use, err := m.reserveWorkspacePaths(1, false, alias, source)
	if err != nil {
		t.Fatal(err)
	}
	defer m.releaseWorkspacePaths(use)
	if _, err := m.reserveWorkspacePaths(1, true, real); err == nil {
		t.Fatal("alias escaped cleanup protection")
	}
}
