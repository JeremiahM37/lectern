//go:build unix

package helpers

import (
	"os"
	"syscall"
)

// openNoFollow is os.open(path, O_RDONLY|O_NOFOLLOW|O_NONBLOCK).
func openNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
}

// openCreateNoFollow is os.open(path, O_CREAT|O_RDWR|O_NOFOLLOW, 0o600).
func openCreateNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0o600)
}
