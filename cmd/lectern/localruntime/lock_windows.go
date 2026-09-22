//go:build windows

package localruntime

import (
	"errors"
	"os"
	"os/exec"
)

// The local runtime drives tmux and holds a POSIX file lock, neither of which
// exists on Windows. The client commands (mcp, post, sessions, tasks) work
// against a remote Lectern; the runtime itself does not run here.
var errUnsupported = errors.New("the local runtime needs tmux and is not available on Windows; point LECTERN_API at a Lectern server (or use WSL)")

func tryLock(*os.File) (bool, error) { return false, errUnsupported }
func unlock(*os.File) error          { return nil }
func detach(*exec.Cmd)               {}
func keepPrivate(int)                {}
func runtimeSupported() error        { return errUnsupported }
