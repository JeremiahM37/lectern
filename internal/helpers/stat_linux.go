package helpers

import (
	"io/fs"
	"syscall"
)

// statIDs is (st_dev, st_ino, st_ctime_ns).
func statIDs(fi fs.FileInfo) (int64, int64, int64) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, 0, 0
	}
	return int64(st.Dev), int64(st.Ino), st.Ctim.Sec*1e9 + st.Ctim.Nsec
}
