//go:build !windows

package ptyhost

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/charmbracelet/x/xpty"
	"golang.org/x/sys/unix"
)

func ttyName(p xpty.Pty) string {
	if u, ok := p.(*xpty.UnixPty); ok {
		return u.SlaveName()
	}
	return ""
}

// foreground is the terminal's foreground process group, which is what a
// user sees running in it: the agent, or the shell once the agent exits.
func foreground(p xpty.Pty, root int) int {
	u, ok := p.(*xpty.UnixPty)
	if !ok {
		return root
	}
	pgrp := 0
	// Control, not Fd: Fd would switch the master to blocking mode and a
	// Read in progress could then never be interrupted by Close.
	_ = u.Control(func(fd uintptr) {
		if v, err := unix.IoctlGetInt(int(fd), unix.TIOCGPGRP); err == nil {
			pgrp = v
		}
	})
	if pgrp > 0 {
		return pgrp
	}
	return root
}

// signalGroup hangs up the session's process group, as a closing terminal
// does.
func signalGroup(p *os.Process) {
	if p != nil {
		_ = syscall.Kill(-p.Pid, syscall.SIGHUP)
	}
}

// killGroup ends whatever of the group survived the hangup.
func killGroup(p *os.Process) {
	if p != nil {
		_ = syscall.Kill(-p.Pid, syscall.SIGKILL)
	}
}

// commandName is how tmux names a pane's current command: the program's
// base name.
func commandName(argv0 string) string {
	return filepath.Base(strings.TrimSpace(argv0))
}

// psArgs runs ps(1) for process arguments, as `ps -o args=` prints them.
func psArgs(args ...string) []string {
	out, err := exec.Command("ps", append([]string{"-o", "args="}, args...)...).Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// probeArgs is what the agent-exit probe reads: the session program's own
// arguments, and those of every process on its terminal.
func probeArgs(s *Session, pid int) (root string, tty []string) {
	return processArgs(pid), ttyProcesses(ttyName(s.pty))
}
