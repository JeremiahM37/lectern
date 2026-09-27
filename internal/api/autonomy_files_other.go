//go:build !linux

package api

import (
	"errors"
	"os"
)

func autoReadRegular(string, int64) ([]byte, error) {
	return nil, errors.New("autonomous workshop requires Linux isolation")
}

func autoReadRootRegular(string, int64) ([]byte, error) {
	return nil, errors.New("autonomous workshop requires Linux isolation")
}

func autoOpenArtifact(string) (*os.File, error) {
	return nil, errors.New("autonomous workshop requires Linux isolation")
}

func autoReadRootImmutable(string, int64) ([]byte, error) {
	return nil, errors.New("autonomous workshop requires Linux isolation")
}
