//go:build linux

package api

import (
	"os"
	"syscall"
	"testing"
)

func TestCapabilityHelperUsesActualNamespaceOwnership(t *testing.T) {
	// Host root is unmapped in the full-suite user namespace. Discovery must
	// refuse that identity rather than weaken its installed-helper ownership.
	st, err := os.Lstat("/usr/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	expected := "unavailable"
	if st.Sys().(*syscall.Stat_t).Uid == 0 {
		expected = "installed"
	}
	if got := autoCapabilityInstallation("/usr/bin/true")["status"]; got != expected {
		t.Fatalf("helper ownership: got %v, want %s", got, expected)
	}
}

func TestBrowserMetadataRejectsRootWritableFiles(t *testing.T) {
	// In isolated test namespaces this process is root; ordinary host runs retain
	// the independent ownership refusal rather than assuming UID zero.
	if os.Geteuid() != 0 {
		t.Skip("requires namespace root")
	}
	path := t.TempDir() + "/selector.json"
	if err := os.WriteFile(path, []byte(`{}`), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := autoReadRootImmutable(path, 16); err == nil {
		t.Fatal("accepted writable browser metadata")
	}
	if err := os.Chmod(path, 0444); err != nil {
		t.Fatal(err)
	}
	if _, err := autoReadRootImmutable(path, 16); err != nil {
		t.Fatal(err)
	}
	if _, err := autoReadRootImmutable(path, 1); err == nil {
		t.Fatal("accepted oversized metadata")
	}
}
