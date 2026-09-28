package ptyhost

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// SocketEnv overrides where the host listens (tests, several instances).
const SocketEnv = "LECTERN_PTYHOST_SOCKET"

// SocketPath is where this user's PTY host listens on this machine.
//
// The directory is private to the user: the socket is a raw input channel
// into every session, with the user's own permissions behind it.
func SocketPath() (string, error) {
	if p := os.Getenv(SocketEnv); p != "" {
		return filepath.Abs(p)
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
