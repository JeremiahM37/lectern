//go:build windows

package codex

import "os/exec"

// Windows has no process groups to signal here, so the child is stopped
// directly. Both steps end the process; the difference only matters on unix.
func setProcGroup(cmd *exec.Cmd) {}

func terminateGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}

func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
