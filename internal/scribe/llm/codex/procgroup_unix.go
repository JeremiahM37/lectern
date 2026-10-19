//go:build unix

package codex

import (
	"os/exec"
	"syscall"
)

// setProcessGroup puts codex in its own process group, and makes a cancel kill
// the whole group. Without this a cancel kills only the codex CLI and leaves
// any helper processes it started running.
func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// A negative pid addresses the group whose id is the child's pid.
		if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
