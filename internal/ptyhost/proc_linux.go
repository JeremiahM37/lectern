//go:build linux

package ptyhost

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// processArgs is a process's argument vector joined by spaces, as
// `ps -o args=` prints it.
func processArgs(pid int) string {
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.ReplaceAll(strings.TrimRight(string(b), "\x00"), "\x00", " "))
}

func processName(pid int) string {
	if pid <= 0 {
		return ""
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cmdline")
	if err != nil {
		return ""
	}
	argv0, _, _ := strings.Cut(string(b), "\x00")
	return commandName(argv0)
}

func processCwd(pid int) string {
	if pid <= 0 {
		return ""
	}
	dir, err := os.Readlink("/proc/" + strconv.Itoa(pid) + "/cwd")
	if err != nil {
		return ""
	}
	return dir
}

// ttyProcesses lists the arguments of every process whose controlling
// terminal is tty, like `ps -o args= -t TTY`.
func ttyProcesses(tty string) []string {
	fi, err := os.Stat(tty)
	if err != nil {
		return nil
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	want := uint64(st.Rdev)
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		// The command name is in parentheses and may contain anything, so
		// the fields are read after its closing one: state ppid pgrp session tty_nr.
		i := strings.LastIndexByte(string(stat), ')')
		if i < 0 {
			continue
		}
		fields := strings.Fields(string(stat[i+1:]))
		if len(fields) < 5 {
			continue
		}
		nr, err := strconv.ParseUint(fields[4], 10, 64)
		if err != nil || nr == 0 || nr != want {
			continue
		}
		if args := processArgs(pid); args != "" {
			out = append(out, args)
		}
	}
	return out
}
