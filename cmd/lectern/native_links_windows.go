//go:build windows

package main

import (
	"os"
	"syscall"
)

func detachedProcess() *syscall.SysProcAttr { return &syscall.SysProcAttr{} }

// The temporary folder is per user on Windows.
func ownedByMe(os.FileInfo) bool { return true }
