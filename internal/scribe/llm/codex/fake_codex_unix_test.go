//go:build !windows

package codex

import (
	"errors"
	"syscall"
)

// processGone reports whether pid no longer exists. A reaped child is gone at
// once; an unreaped one shows up as a zombie, which is still "present" here.
func processGone(pid int) bool {
	return errors.Is(syscall.Kill(pid, 0), syscall.ESRCH)
}
