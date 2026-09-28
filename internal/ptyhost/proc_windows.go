//go:build windows

package ptyhost

import (
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/charmbracelet/x/xpty"
	"golang.org/x/sys/windows"
)

// A pseudoconsole has no process groups and no terminal device, so the
// questions Unix answers with tcgetpgrp and /dev/pts are answered from the
// process tree under the session's program.

func ttyName(xpty.Pty) string { return "" }

type proc struct {
	pid, parent uint32
	exe         string
}

func snapshot() []proc {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return nil
	}
	defer windows.CloseHandle(snap)
	var e windows.ProcessEntry32
	e.Size = uint32(unsafe.Sizeof(e))
	var out []proc
	for err = windows.Process32First(snap, &e); err == nil; err = windows.Process32Next(snap, &e) {
		out = append(out, proc{pid: e.ProcessID, parent: e.ParentProcessID, exe: windows.UTF16ToString(e.ExeFile[:])})
	}
	return out
}

// descendants lists root's process tree, root first.
func descendants(root int) []proc {
	all := snapshot()
	children := map[uint32][]proc{}
	var self *proc
	for i := range all {
		children[all[i].parent] = append(children[all[i].parent], all[i])
		if all[i].pid == uint32(root) {
			self = &all[i]
		}
	}
	if self == nil {
		return nil
	}
	out := []proc{*self}
	for i := 0; i < len(out); i++ {
		for _, c := range children[out[i].pid] {
			if c.pid != out[i].pid {
				out = append(out, c)
			}
		}
	}
	return out
}

func created(pid uint32) int64 {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0
	}
	defer windows.CloseHandle(h)
	var c, e, k, u windows.Filetime
	if windows.GetProcessTimes(h, &c, &e, &k, &u) != nil {
		return 0
	}
	return c.Nanoseconds()
}

// foreground is the most recently started process in the session's tree —
// what a user would see in front: the agent, or the shell once it exits.
func foreground(_ xpty.Pty, root int) int {
	best, bestAt := root, int64(0)
	for _, p := range descendants(root) {
		if at := created(p.pid); at >= bestAt {
			best, bestAt = int(p.pid), at
		}
	}
	return best
}

func processName(pid int) string {
	for _, p := range snapshot() {
		if p.pid == uint32(pid) {
			return commandName(p.exe)
		}
	}
	return ""
}

// commandName is a program's base name without .exe, so a Windows process
// reads like a Unix one ("bash", not "C:\...\bash.exe").
func commandName(exe string) string {
	base := filepath.Base(strings.TrimSpace(exe))
	if strings.EqualFold(filepath.Ext(base), ".exe") {
		base = base[:len(base)-4]
	}
	return base
}

// processArgs is a process's command line with the program named as on Unix.
func processArgs(pid int) string {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, uint32(pid))
	if err != nil {
		return processName(pid)
	}
	defer windows.CloseHandle(h)
	buf := make([]byte, 64<<10)
	var n uint32
	if windows.NtQueryInformationProcess(h, windows.ProcessCommandLineInformation, unsafe.Pointer(&buf[0]), uint32(len(buf)), &n) != nil {
		return processName(pid)
	}
	us := (*windows.NTUnicodeString)(unsafe.Pointer(&buf[0]))
	line := us.String()
	args := windows.DecomposeCommandLine
	words, err := args(line)
	if err != nil || len(words) == 0 {
		return processName(pid)
	}
	words[0] = commandName(words[0])
	return strings.Join(words, " ")
}

// processCwd is not answerable for another process through a public API;
// the caller falls back to the directory the shell reported (OSC 7) and then
// to where the session started.
func processCwd(int) string { return "" }

// ttyProcesses lists the arguments of every process in a session's tree. It
// takes the root pid through the tty argument's place (see probeSession).
func ttyProcesses(string) []string { return nil }

func treeArgs(root int) []string {
	var out []string
	for _, p := range descendants(root) {
		if a := processArgs(int(p.pid)); a != "" {
			out = append(out, a)
		}
	}
	return out
}

// signalGroup has nothing to send on Windows: closing the pseudoconsole
// ends every process attached to it.
func signalGroup(*os.Process) {}

// killGroup ends the session's whole process tree.
func killGroup(p *os.Process) {
	if p == nil {
		return
	}
	for _, d := range descendants(p.Pid) {
		if h, err := windows.OpenProcess(windows.PROCESS_TERMINATE, false, d.pid); err == nil {
			_ = windows.TerminateProcess(h, 1)
			windows.CloseHandle(h)
		}
	}
}

// probeArgs is what the agent-exit probe reads. Without process groups the
// "root" is the process in front: a Lectern-launched session whose agent has
// exited has only its trailing shell there, which reads as "bash" as on Unix.
func probeArgs(s *Session, pid int) (root string, tty []string) {
	return processArgs(foreground(s.pty, pid)), treeArgs(pid)
}
