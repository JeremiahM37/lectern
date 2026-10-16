//go:build !windows

package hooks

import (
	"os/exec"
	"syscall"
)

// shellCommand returns the command that runs a hook string.
func shellCommand(command string) (string, []string) { return "/bin/sh", []string{"-c", command} }

// setProcGroup puts the child in its own process group so a timeout can
// kill everything the hook started, not just the shell.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree kills the child's whole process group.
func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		return cmd.Process.Kill()
	}
	return nil
}
