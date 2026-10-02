package ptyhost

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

// A test never reaches the user's PTY host: with no socket named, the
// default is refused, in this process and in every process it starts.
func TestTestsCannotUseTheDefaultSocket(t *testing.T) {
	t.Setenv(SocketEnv, "")
	if _, err := SocketPath(); !errors.Is(err, ErrDefaultSocketInTest) {
		t.Fatalf("default socket under test: %v", err)
	}
	out, err := exec.Command("/bin/sh", "-c", "printf %s \"$"+TestGuardEnv+"\"").Output()
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("children do not inherit the guard: %q %v", out, err)
	}
	t.Setenv(SocketEnv, "/tmp/lectern-test-guard/s.sock")
	if p, err := SocketPath(); err != nil || p != "/tmp/lectern-test-guard/s.sock" {
		t.Fatalf("a named socket: %q %v", p, err)
	}
}
