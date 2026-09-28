//go:build !unix

package helpers

import "os/exec"

// setSession has no equivalent here; the helpers that need a session are
// Unix-only.
func setSession(*exec.Cmd, bool) {}

// oNoFollow does not exist here; callers check for links themselves.
const oNoFollow = 0
