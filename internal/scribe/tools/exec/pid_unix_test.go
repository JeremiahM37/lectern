//go:build !windows

package exec

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// pidAlive reports whether pid is running. A zombie counts as dead: it has
// exited and only waits to be reaped.
func pidAlive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	stat, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		// No procfs (macOS): kill(0) succeeded, so treat it as alive.
		return true
	}
	// Format: pid (comm) state ...; comm may contain spaces and parens.
	s := string(stat)
	if i := strings.LastIndexByte(s, ')'); i >= 0 && i+2 < len(s) {
		return s[i+2] != 'Z' && s[i+2] != 'X'
	}
	return true
}
