//go:build windows

package executor

import (
	"github.com/JeremiahM37/lectern/v2/internal/gitbash"
)

// localShell runs this machine's command lines with Git for Windows' bash:
// Lectern's command lines are POSIX shell (docs/ptyhost.md §6).
func localShell() (string, error) { return gitbash.Find() }

// nativePath turns a path a Git Bash command line used (/c/Users/me) into the
// one Go's file functions take (C:\Users\me).
func nativePath(p string) string { return gitbash.NativePath(p) }
