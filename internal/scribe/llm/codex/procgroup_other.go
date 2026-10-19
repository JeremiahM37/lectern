//go:build !unix

package codex

import "os/exec"

// setProcessGroup does nothing where process groups are not available. A cancel
// then kills only the codex process, which is the default for exec.CommandContext.
func setProcessGroup(cmd *exec.Cmd) {}
