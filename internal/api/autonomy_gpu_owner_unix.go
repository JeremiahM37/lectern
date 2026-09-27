//go:build unix

package api

import (
	"os"
	"syscall"
)

func autoGPUOwnedByRoot(info os.FileInfo) bool {
	st, ok := info.Sys().(*syscall.Stat_t)
	return ok && st.Uid == 0
}
