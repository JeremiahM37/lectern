//go:build windows

package codex

import "os"

// processGone on windows asks the OS directly. Signal 0 does not exist there,
// and the fake is skipped on windows anyway, so this only has to compile.
func processGone(pid int) bool {
	p, err := os.FindProcess(pid)
	if err != nil {
		return true
	}
	_ = p.Release()
	return false
}
