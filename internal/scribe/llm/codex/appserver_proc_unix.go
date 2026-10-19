//go:build !windows

package codex

import (
	"os/exec"
	"syscall"
)

// setProcGroup starts the child in its own process group. A codex launcher
// may run the real binary as a child; signalling the group reaches both, so
// nothing is left running after Lectern has let go of it.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// terminateGroup asks the child's process group to exit (SIGTERM).
func terminateGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// killGroup stops the child's process group at once (SIGKILL). If the group
// cannot be signalled, the leader itself is killed.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
