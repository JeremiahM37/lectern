//go:build !linux

package api

import "errors"

func autoReadRegular(string, int64) ([]byte, error) {
	return nil, errors.New("autonomous workshop requires Linux isolation")
}
