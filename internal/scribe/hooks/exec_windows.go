//go:build windows

package hooks

import "os/exec"

// shellCommand returns the command that runs a hook string.
func shellCommand(command string) (string, []string) { return "cmd.exe", []string{"/C", command} }

func setProcGroup(cmd *exec.Cmd) {}

func killTree(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
