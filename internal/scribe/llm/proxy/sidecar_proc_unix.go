//go:build unix

package proxy

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

// detach puts the sidecar in its own process group. A Ctrl-C in the terminal
// that started Scribe then does not reach it, and it outlives Scribe, which
// is what makes an orphan possible and worth reclaiming.
func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// startTicks returns the kernel start time of pid, in clock ticks since boot.
// With the pid it tells the recorded process from a later one that reused it.
func startTicks(pid int) (uint64, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return 0, err
	}
	// The command name is in parentheses and may hold spaces, so the fields
	// are counted from the last ')'. starttime is field 22, which is index 19
	// of the fields after it.
	i := bytes.LastIndexByte(b, ')')
	if i < 0 {
		return 0, fmt.Errorf("unexpected /proc stat format")
	}
	fields := strings.Fields(string(b[i+1:]))
	if len(fields) < 20 {
		return 0, fmt.Errorf("short /proc stat line")
	}
	return strconv.ParseUint(fields[19], 10, 64)
}

// identifies reports whether the record's pid is still the process it names:
// the same executable and the same start time. Any doubt is a no, so nothing
// is signalled unless both match.
func (r sidecarRecord) identifies() bool {
	if r.PID <= 0 || r.Binary == "" {
		return false
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", r.PID))
	if err != nil || exe != r.Binary {
		return false
	}
	start, err := startTicks(r.PID)
	return err == nil && start == r.Start
}

// processAlive reports whether pid exists. EPERM still means it exists.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || err == syscall.EPERM
}

func terminatePID(pid int) error { return syscall.Kill(pid, syscall.SIGTERM) }

func killPID(pid int) error { return syscall.Kill(pid, syscall.SIGKILL) }
