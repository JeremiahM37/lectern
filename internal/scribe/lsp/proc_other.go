//go:build !unix

package lsp

import "os/exec"

// setProcGroup is a no-op where process groups are not available.
func setProcGroup(cmd *exec.Cmd) {}

// killTree kills the server process. Children it spawned may outlive it on
// these platforms.
func killTree(cmd *exec.Cmd) {
	if cmd.Process != nil {
		_ = cmd.Process.Kill()
	}
}
