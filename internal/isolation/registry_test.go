package isolation

import (
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestProxyRegistryStartStop(t *testing.T) {
	reg := NewProxyRegistry(nil)
	reg.Dir = t.TempDir()

	path, err := reg.Start(42, []string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if !reg.Running(42) {
		t.Fatal("expected the proxy to be recorded as running")
	}
	if filepath.Dir(path) != reg.Dir {
		t.Errorf("socket should live under Dir, got %q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected the socket file to exist: %v", err)
	}
	// It must actually be accepting connections.
	conn, err := net.DialTimeout("unix", path, time.Second)
	if err != nil {
		t.Fatalf("could not dial the started proxy: %v", err)
	}
	conn.Close()

	// Starting again for the same id replaces it without leaking the old one.
	path2, err := reg.Start(42, []string{"example.com"})
	if err != nil {
		t.Fatal(err)
	}
	if path2 != path {
		t.Errorf("expected the same deterministic socket path, got %q vs %q", path2, path)
	}

	reg.Stop(42)
	if reg.Running(42) {
		t.Error("expected the proxy to be gone after Stop")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("expected the socket file to be removed, stat err=%v", err)
	}
	// Stopping something never started must not panic or error.
	reg.Stop(9999)
}

func TestProxyRegistryStopIsolatesByID(t *testing.T) {
	reg := NewProxyRegistry(nil)
	reg.Dir = t.TempDir()
	if _, err := reg.Start(1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Start(2, nil); err != nil {
		t.Fatal(err)
	}
	reg.Stop(1)
	if reg.Running(1) {
		t.Error("session 1 should be stopped")
	}
	if !reg.Running(2) {
		t.Error("stopping session 1 must not affect session 2")
	}
	reg.Stop(2)
}
