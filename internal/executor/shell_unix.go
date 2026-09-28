//go:build !windows

package executor

// localShell runs this machine's command lines.
func localShell() (string, error) { return "bash", nil }

// nativePath is the path Go's file functions take for one a command line
// used; on Unix they are the same.
func nativePath(p string) string { return p }
