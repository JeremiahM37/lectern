//go:build windows

package exec

// pidAlive is not used on Windows: the process-tree tests skip there.
func pidAlive(pid int) bool { return false }
