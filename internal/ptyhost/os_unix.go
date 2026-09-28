//go:build !windows

package ptyhost

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// defaultDir is $XDG_RUNTIME_DIR/lectern when there is one (per user, on a
// tmpfs, cleaned at logout), else a per-uid directory in the temp dir, as
// tmux does.
func defaultDir() (string, error) {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" {
		return filepath.Join(d, "lectern"), nil
	}
	return filepath.Join(os.TempDir(), "lectern-"+strconv.Itoa(os.Getuid())), nil
}

func checkOwner(dir string, fi os.FileInfo) error {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("%s belongs to another user; refusing to use it", dir)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		if err := os.Chmod(dir, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// tryLock takes an exclusive lock without waiting; held=false means another
// host has it.
func tryLock(f *os.File) (bool, error) {
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

// startDetached starts the host in its own session, away from the terminal
// and the process group of whoever started it, so neither a closed terminal
// nor a signal to that group reaches the agents.
func startDetached(cmd *exec.Cmd) (*os.Process, error) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}

// childAttr makes a session's program the leader of a new session with the
// PTY as its controlling terminal, so job control, Ctrl-C and the foreground
// process group all work as in any terminal.
func childAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
}

// defaultShell runs a single command line. tmux uses the user's $SHELL, but
// every command line Lectern builds is POSIX sh, and a fish or nu login shell
// would misread it, so this is always sh.
func defaultShell() []string { return []string{"/bin/sh", "-c"} }
