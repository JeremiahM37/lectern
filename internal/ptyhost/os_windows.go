//go:build windows

package ptyhost

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"

	"github.com/JeremiahM37/lectern/v2/internal/gitbash"
)

// defaultDir is %LOCALAPPDATA%\lectern. The profile's ACL already keeps it to
// this user, SYSTEM and Administrators, which is what makes an AF_UNIX socket
// there private (Windows has no mode bits to set).
func defaultDir() (string, error) {
	base := os.Getenv("LOCALAPPDATA")
	if base == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			return "", err
		}
		base = dir
	}
	return filepath.Join(base, "lectern"), nil
}

func checkOwner(string, os.FileInfo) error { return nil }

// tryLock takes an exclusive lock without waiting; held=false means another
// host has it.
func tryLock(f *os.File) (bool, error) {
	var ol windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_IO_PENDING) {
		return false, nil
	}
	return false, err
}

// startDetached starts the host with no console and in its own process
// group, and out of the starter's job object when that job allows it. A
// console being closed, or a Ctrl-C in it, then reaches neither the host nor
// its sessions: on Windows a session lives exactly as long as the process
// holding its pseudoconsole.
func startDetached(cmd *exec.Cmd) (*os.Process, error) {
	flags := uint32(windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags | windows.CREATE_BREAKAWAY_FROM_JOB, HideWindow: true}
	err := cmd.Start()
	if err == nil {
		return cmd.Process, nil
	}
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return nil, err
	}
	// The starter's job forbids breaking away; stay in it rather than fail.
	retry := exec.Command(cmd.Path, cmd.Args[1:]...)
	retry.Env, retry.Dir, retry.Stdin, retry.Stdout, retry.Stderr = cmd.Env, cmd.Dir, cmd.Stdin, cmd.Stdout, cmd.Stderr
	retry.SysProcAttr = &syscall.SysProcAttr{CreationFlags: flags, HideWindow: true}
	if err := retry.Start(); err != nil {
		return nil, err
	}
	return retry.Process, nil
}

func childAttr() *syscall.SysProcAttr { return nil }

// defaultShell runs a single command line with Git Bash, which is what every
// command line Lectern builds is written for; cmd.exe when there is none.
func defaultShell() []string {
	if bash, err := gitbash.Find(); err == nil {
		return []string{bash, "-c"}
	}
	return []string{"cmd.exe", "/c"}
}

// posixProgram maps a POSIX path such as /bin/sh or /usr/bin/env, which
// Lectern's command lines name, to the program in Git for Windows; "" when
// file is not such a path or Git has no such program.
func posixProgram(file string) string {
	if !strings.HasPrefix(file, "/") {
		return ""
	}
	bash, err := gitbash.Find()
	if err != nil {
		return ""
	}
	root := filepath.Dir(filepath.Dir(bash)) // <root>\bin\bash.exe or <root>\usr\bin\bash.exe
	if strings.EqualFold(filepath.Base(root), "usr") {
		root = filepath.Dir(root)
	}
	rel := filepath.FromSlash(file)
	for _, p := range []string{filepath.Join(root, rel), filepath.Join(root, "usr", rel)} {
		for _, ext := range []string{"", ".exe"} {
			if fi, err := os.Stat(p + ext); err == nil && !fi.IsDir() {
				return p + ext
			}
		}
	}
	return ""
}
