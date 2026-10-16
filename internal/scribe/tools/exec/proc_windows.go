//go:build windows

package exec

import (
	"os/exec"
	"strconv"
	"sync"
	"syscall"
)

var (
	shellOnce   sync.Once
	shellPrefix []string
)

// shellArgv returns the argv that runs command through PowerShell when it is
// available, otherwise cmd /c.
func shellArgv(command string) []string {
	shellOnce.Do(func() {
		for _, name := range []string{"pwsh", "powershell"} {
			if p, err := exec.LookPath(name); err == nil {
				shellPrefix = []string{p, "-NoProfile", "-NonInteractive", "-Command"}
				return
			}
		}
		shellPrefix = []string{"cmd", "/c"}
	})
	return append(append([]string(nil), shellPrefix...), command)
}

func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
}

// terminateGroup and killGroup both kill the whole tree on Windows: there is
// no graceful group signal, so taskkill /T is used.
func terminateGroup(pid int) { killGroup(pid) }

func killGroup(pid int) {
	if pid <= 0 {
		return
	}
	_ = exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(pid)).Run()
}
