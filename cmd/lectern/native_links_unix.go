//go:build !windows

package main

import (
	"os"
	"syscall"
)

// detachedProcess starts a link action in its own session, so it outlives
// the tmux binding that started it.
func detachedProcess() *syscall.SysProcAttr { return &syscall.SysProcAttr{Setsid: true} }

func ownedByMe(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && int(st.Uid) == os.Getuid()
}

// processAlive reports whether a process with this id is still running.
func processAlive(pid int) bool {
	return pid > 0 && syscall.Kill(pid, 0) == nil
}
