//go:build unix

package helpers

import (
	"os/exec"
	"syscall"
)

// setSession is start_new_session=True.
func setSession(cmd *exec.Cmd, on bool) {
	if on {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	}
}

const oNoFollow = syscall.O_NOFOLLOW
