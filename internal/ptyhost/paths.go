package ptyhost

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// SocketEnv overrides where the host listens (tests, several instances).
const SocketEnv = "LECTERN_PTYHOST_SOCKET"

// TestGuardEnv marks a process started by a test, directly or through any
// number of children. Under it the default socket is refused: a test names
// its own (SocketEnv), so it never starts, reaches or leaves behind a host on
// the user's real socket — where a running Lectern server would count its
// sessions as its own. The test runners set it; every Go test binary sets it
// for itself below, so a `go test` run by hand is guarded too.
const TestGuardEnv = "LECTERN_PTYHOST_TEST_GUARD"

// ErrDefaultSocketInTest is SocketPath's answer under TestGuardEnv when no
// socket was named.
var ErrDefaultSocketInTest = errors.New("refusing the default PTY host socket in a test: set " + SocketEnv + " to a private path")

func init() {
	if testing.Testing() {
		_ = os.Setenv(TestGuardEnv, "1")
	}
}

// SocketPath is where this user's PTY host listens on this machine.
//
// The directory is private to the user: the socket is a raw input channel
// into every session, with the user's own permissions behind it.
func SocketPath() (string, error) {
	if p := os.Getenv(SocketEnv); p != "" {
		return filepath.Abs(p)
	}
	if os.Getenv(TestGuardEnv) != "" {
		return "", ErrDefaultSocketInTest
	}
	dir, err := defaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "ptyhost.sock"), nil
}

// lockPath is the lock beside a socket. Its holder owns the socket's name.
func lockPath(socket string) string { return socket + ".lock" }

// logPath is where a detached host writes its log.
func logPath(socket string) string { return socket + ".log" }

// ensureDir makes the socket's directory private to this user, and refuses a
// directory someone else owns rather than trusting it.
func ensureDir(socket string) error {
	dir := filepath.Dir(socket)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	fi, err := os.Stat(dir)
	if err != nil {
		return err
	}
	if !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	if err := checkOwner(dir, fi); err != nil {
		return err
	}
	return nil
}

var errNotRunning = errors.New("the PTY host is not running")
