//go:build linux

package api

import (
	"errors"
	"io"
	"os"
	"syscall"
)

func autoReadActivityIndex(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("index is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
	if err != nil || len(data) > 16<<20 {
		return nil, errors.New("index unavailable")
	}
	return data, nil
}
