//go:build linux

package api

import (
	"errors"
	"io"
	"os"
	"syscall"
)

func autoReadRegular(path string, max int64) ([]byte, error) {
	return autoReadRegularOwned(path, max, false, false)
}

func autoReadRootRegular(path string, max int64) ([]byte, error) {
	return autoReadRegularOwned(path, max, true, false)
}

func autoReadRootImmutable(path string, max int64) ([]byte, error) {
	return autoReadRegularOwned(path, max, true, true)
}

func autoReadRegularOwned(path string, max int64, rootOwned, readOnly bool) ([]byte, error) {
	f, e := autoOpenArtifact(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil {
		return nil, e
	}
	if !st.Mode().IsRegular() || st.Size() > max {
		return nil, errors.New("artifact is not a bounded regular file")
	}
	if rootOwned {
		owner, ok := st.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 0 || st.Mode().Perm()&0022 != 0 {
			return nil, errors.New("capability metadata must be root-owned and protected from other users' writes")
		}
	}
	if readOnly && st.Mode().Perm()&0222 != 0 {
		return nil, errors.New("browser metadata must be read-only")
	}
	data, e := io.ReadAll(io.LimitReader(f, max+1))
	if int64(len(data)) > max {
		return nil, errors.New("artifact exceeds size limit")
	}
	return data, e
}

func autoOpenArtifact(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(fd), path), nil
}
