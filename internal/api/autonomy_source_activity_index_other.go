//go:build !linux

package api

import "errors"

// Workshop execution is Linux-only; do not claim a clean tree when this
// platform lacks the nonblocking, no-follow inspection implementation.
func autoReadActivityIndex(path string) ([]byte, error) {
	return nil, errors.New("activity index inspection unsupported on this platform")
}
