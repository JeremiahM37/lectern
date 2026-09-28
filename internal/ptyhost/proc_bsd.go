//go:build !windows && !linux

package ptyhost

import (
	"os/exec"
	"strconv"
	"strings"
)

// macOS and the BSDs have no /proc; ps(1) and lsof(8) are part of the base
// system and answer the same questions.

func processArgs(pid int) string {
	if lines := psArgs("-p", strconv.Itoa(pid)); len(lines) > 0 {
		return lines[0]
	}
	return ""
}

func processName(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return ""
	}
	return commandName(strings.TrimSpace(string(out)))
}

func processCwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	out, err := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if dir, ok := strings.CutPrefix(line, "n"); ok {
			return dir
		}
	}
	return ""
}

func ttyProcesses(tty string) []string {
	return psArgs("-t", strings.TrimPrefix(tty, "/dev/"))
}
