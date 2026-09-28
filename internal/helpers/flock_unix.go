//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package helpers

import (
	"os"
	"syscall"
)

// lockFile is fcntl.flock(f, LOCK_EX); the lock ends when f is closed.
func lockFile(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}
