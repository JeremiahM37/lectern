//go:build !windows

package localruntime

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// tryLock takes the singleton lock without blocking. held=false means another
// runtime owns it; any other failure is returned.
func tryLock(f *os.File) (held bool, err error) {
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}
	return false, err
}

// unlock releases a lock taken by tryLock.
func unlock(f *os.File) error { return syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }

// detach starts the engine in its own session so it survives the launching
// shell.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }

// keepPrivate stops agent subprocesses inheriting the lock descriptor.
func keepPrivate(fd int) { syscall.CloseOnExec(fd) }

// runtimeSupported reports whether this platform can host the local runtime.
func runtimeSupported() error { return nil }
