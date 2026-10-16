//go:build unix

package lsp

import (
	"os/exec"
	"syscall"
)

// setProcGroup starts a server in its own process group, so killTree can
// reach the children a server spawns (tsserver forks, gopls workers). Without
// it a wrapper script killed by PID leaves its real server running.
func setProcGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killTree kills the server's whole process group, then the server itself.
// A group that has already gone is not an error.
func killTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	_ = cmd.Process.Kill()
}
