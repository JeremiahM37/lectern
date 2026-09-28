//go:build !unix

package helpers

// The worktree helpers need flock, process groups and descriptor passing.
// Elsewhere they refuse, in the shape their callers read.

import "io"

func init() {
	for _, name := range []string{"worktree", "worktree-cancel", "worktree-preflight", "worktree-group",
		"worktree-gated", "worktree-monitor", "worktree-setup"} {
		Register(name, func(_ []string, _ io.Reader, stdout, _ io.Writer) int {
			io.WriteString(stdout, pyDumps(newObj("error", "Owned worktrees are not supported on this platform"))+"\n")
			return 1
		})
	}
}
