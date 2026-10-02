// Package ptytest gives a test its own PTY host socket. Under a test the
// default socket is refused (ptyhost.TestGuardEnv), so any test that starts a
// server or `lectern pty` with the pty backend names one of these.
package ptytest

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/ptyhost"
)

// Socket is a private socket path for one test. Its directory is short, as a
// Unix socket path must be, and whatever host ends up listening there is
// stopped, sessions and all, when the test ends.
func Socket(t testing.TB) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "lpty-")
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(dir, "s.sock")
	t.Cleanup(func() {
		Stop(socket)
		_ = os.RemoveAll(dir)
	})
	return socket
}

// Stop ends the host on socket, if one is listening, with its sessions.
func Stop(socket string) {
	c, err := ptyhost.Dial(socket)
	if err != nil {
		return
	}
	defer c.Close()
	_, _ = c.Do(ptyhost.Request{Op: "shutdown", Force: true})
}
