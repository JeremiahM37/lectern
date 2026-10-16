//go:build !unix

package app

import "errors"

// freeBytes is not measured on this platform.
func freeBytes(string) (int64, error) {
	return 0, errors.New("free space is not measured on this platform")
}
