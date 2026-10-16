//go:build !windows

package exec

import (
	"os/exec"
	"sync"
	"syscall"
)

var (
	bashOnce sync.Once
	bashPath string
)

// shellArgv returns the argv that runs command through the platform shell:
// bash -lc when bash is installed, otherwise sh -c.
func shellArgv(command string) []string {
	bashOnce.Do(func() {
		if p, err := exec.LookPath("bash"); err == nil {
			bashPath = p
		}
	})
	if bashPath != "" {
		return []string{bashPath, "-lc", command}
	}
	return []string{"sh", "-c", command}
}

// configureProcess puts the child in its own process group so the whole
// tree can be signalled at once.
func configureProcess(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup sends sig to every process in the group led by pid.
func signalGroup(pid int, sig syscall.Signal) {
	if pid <= 0 {
		return
	}
	_ = syscall.Kill(-pid, sig)
}

// terminateGroup asks the group to stop (SIGTERM).
func terminateGroup(pid int) { signalGroup(pid, syscall.SIGTERM) }

// killGroup force-kills the group (SIGKILL).
func killGroup(pid int) { signalGroup(pid, syscall.SIGKILL) }
