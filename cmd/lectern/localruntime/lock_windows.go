//go:build windows

package localruntime

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"syscall"

	"golang.org/x/sys/windows"
)

// On Windows the runtime keeps its sessions in lectern's own PTY host
// (docs/ptyhost.md), so it runs here as it does on Unix. The singleton lock is
// a LockFileEx byte-range lock, and the engine inherits the locked handle
// rather than a descriptor number.

func tryLock(f *os.File) (bool, error) {
	var ol windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return false, err
}

func unlock(f *os.File) error {
	var ol windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ol)
}

// detach starts the engine with no console and in its own process group, so
// closing the console that ran `lectern up` ends neither it nor its agents.
func detach(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP
	cmd.SysProcAttr.HideWindow = true
}

// keepPrivate stops agent subprocesses inheriting the lock handle.
func keepPrivate(fd int) {
	_ = windows.SetHandleInformation(windows.Handle(fd), windows.HANDLE_FLAG_INHERIT, 0)
}

func runtimeSupported() error { return nil }

// passFiles hands the locked lock file and the token pipe to the engine as
// inherited handles, named by their values.
func passFiles(cmd *exec.Cmd, lock, token *os.File) (lockArg, tokenArg string) {
	handles := []syscall.Handle{syscall.Handle(lock.Fd()), syscall.Handle(token.Fd())}
	for _, h := range handles {
		_ = windows.SetHandleInformation(windows.Handle(h), windows.HANDLE_FLAG_INHERIT, windows.HANDLE_FLAG_INHERIT)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{AdditionalInheritedHandles: handles}
	detach(cmd)
	return strconv.FormatUint(uint64(handles[0]), 10), strconv.FormatUint(uint64(handles[1]), 10)
}
