//go:build windows

package xhost

import "os/exec"

func setDetached(cmd *exec.Cmd) {}
