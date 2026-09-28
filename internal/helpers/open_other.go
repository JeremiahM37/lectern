//go:build !unix

package helpers

import "os"

// openNoFollow opens path read-only; there is no O_NOFOLLOW here.
func openNoFollow(path string) (*os.File, error) { return os.Open(path) }

// openCreateNoFollow creates path if needed, read-write.
func openCreateNoFollow(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
}
