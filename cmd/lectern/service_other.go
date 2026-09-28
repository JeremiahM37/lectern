//go:build !windows

package main

import "errors"

func installWindowsLogon(string) (string, error) {
	return "", errors.New("a Windows logon entry can only be installed on Windows")
}
