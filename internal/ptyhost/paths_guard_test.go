package ptyhost

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// guardHelperEnv makes the test binary print the guard and exit, so the check
// runs the same way on every OS (Windows has no /bin/sh).
const guardHelperEnv = "LECTERN_GUARD_HELPER"

// printGuardIfHelper is called first by TestMain (host_test.go).
func printGuardIfHelper() {
	if os.Getenv(guardHelperEnv) == "1" {
		fmt.Print(os.Getenv(TestGuardEnv))
		os.Exit(0)
	}
}

// A test never reaches the user's PTY host: with no socket named, the
// default is refused, in this process and in every process it starts.
func TestTestsCannotUseTheDefaultSocket(t *testing.T) {
	t.Setenv(SocketEnv, "")
	if _, err := SocketPath(); !errors.Is(err, ErrDefaultSocketInTest) {
		t.Fatalf("default socket under test: %v", err)
	}
	child := exec.Command(os.Args[0])
	child.Env = append(os.Environ(), guardHelperEnv+"=1")
	out, err := child.Output()
	if err != nil || strings.TrimSpace(string(out)) != "1" {
		t.Fatalf("children do not inherit the guard: %q %v", out, err)
	}
	named := filepath.Join(t.TempDir(), "s.sock")
	want, _ := filepath.Abs(named)
	t.Setenv(SocketEnv, named)
	if p, err := SocketPath(); err != nil || p != want {
		t.Fatalf("a named socket: %q %v, want %q", p, err, want)
	}
}
