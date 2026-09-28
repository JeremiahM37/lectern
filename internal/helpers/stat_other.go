//go:build !linux && !darwin

package helpers

import "io/fs"

// statIDs is (st_dev, st_ino, st_ctime_ns); without a POSIX stat the
// change time stands in for ctime and there is no device or inode.
func statIDs(fi fs.FileInfo) (int64, int64, int64) {
	return 0, 0, fi.ModTime().UnixNano()
}
