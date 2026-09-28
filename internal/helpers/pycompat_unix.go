//go:build unix

package helpers

import (
	"os"
	"os/exec"
	"syscall"
)

// setSession is start_new_session=True.
func setSession(cmd *exec.Cmd, on bool) {
	if on {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
}

const oNoFollow = syscall.O_NOFOLLOW

// lockExclusive is fcntl.flock(f, LOCK_EX).
func lockExclusive(f *os.File) error {
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			return err
		}
	}
}

// pyAccessX is os.access(p, os.X_OK).
func pyAccessX(p string) bool { return syscall.Access(p, 1) == nil }

// oNonblock lets an open of a FIFO return at once, so it can be refused as
// not a regular file rather than hang.
const oNonblock = syscall.O_NONBLOCK
