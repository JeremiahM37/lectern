//go:build !unix

package helpers

import (
	"os"
	"os/exec"
)

// setSession has no equivalent here; the helpers that need a session are
// Unix-only.
func setSession(*exec.Cmd, bool) {}

// oNoFollow does not exist here; callers check for links themselves.
const oNoFollow = 0

// lockExclusive: no flock here, so edits are not serialised against other
// writers.
func lockExclusive(*os.File) error { return nil }

// pyAccessX is os.access(p, os.X_OK); Windows has no execute bit, so any
// existing file counts, as Python reports there.
func pyAccessX(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

const oNonblock = 0
