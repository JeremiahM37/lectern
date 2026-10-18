//go:build !unix

package proxy

import (
	"errors"
	"os/exec"
)

// On platforms without the unix process model the sidecar can not confirm a
// pid, so it never signals one. processAlive reports true, which makes a
// record from another Scribe block a start rather than be overwritten.

var errNoProcessControl = errors.New("process control is not supported on this platform")

func detach(cmd *exec.Cmd) {}

func startTicks(pid int) (uint64, error) { return 0, errNoProcessControl }

func (r sidecarRecord) identifies() bool { return false }

func processAlive(pid int) bool { return pid > 0 }

func terminatePID(pid int) error { return errNoProcessControl }

func killPID(pid int) error { return errNoProcessControl }
