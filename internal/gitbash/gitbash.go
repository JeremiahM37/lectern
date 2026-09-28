// Package gitbash finds the bash that ships with Git for Windows.
//
// Lectern's command lines are POSIX shell, and on Windows the one shell every
// agent machine already has is Git for Windows' bash: Git is a requirement,
// and Claude Code needs Git Bash on Windows too. A bare `bash` on PATH is
// avoided on purpose — C:\Windows\System32\bash.exe is the WSL launcher,
// which would run the command in a Linux distribution, not on this machine.
package gitbash

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// Env names a bash.exe to use instead of searching.
const Env = "LECTERN_BASH"

var (
	once  sync.Once
	found string
	err   error
)

// Find returns the path of Git for Windows' bash.exe.
func Find() (string, error) {
	once.Do(func() { found, err = find(exec.LookPath, os.Getenv) })
	return found, err
}

func find(lookPath func(string) (string, error), getenv func(string) string) (string, error) {
	if p := getenv(Env); p != "" {
		return p, nil
	}
	var roots []string
	if git, e := lookPath("git"); e == nil {
		// git.exe lives in <root>\cmd, <root>\bin or <root>\mingw64\bin.
		dir := filepath.Dir(git)
		roots = append(roots, filepath.Dir(dir), filepath.Dir(filepath.Dir(dir)))
	}
	for _, env := range []string{"ProgramW6432", "ProgramFiles", "ProgramFiles(x86)"} {
		if base := getenv(env); base != "" {
			roots = append(roots, filepath.Join(base, "Git"))
		}
	}
	if local := getenv("LOCALAPPDATA"); local != "" {
		roots = append(roots, filepath.Join(local, "Programs", "Git"))
	}
	for _, root := range roots {
		for _, rel := range [][]string{{"bin", "bash.exe"}, {"usr", "bin", "bash.exe"}} {
			p := filepath.Join(append([]string{root}, rel...)...)
			if fi, e := os.Stat(p); e == nil && !fi.IsDir() {
				return p, nil
			}
		}
	}
	return "", errors.New("Git for Windows' bash.exe was not found; install Git for Windows (https://git-scm.com/download/win) or set " + Env)
}

// PosixPath turns C:\Users\me into /c/Users/me, the form Git Bash expects in a
// command line. Other paths are returned unchanged.
func PosixPath(p string) string {
	if len(p) >= 2 && p[1] == ':' {
		drive := strings.ToLower(p[:1])
		return "/" + drive + strings.ReplaceAll(p[2:], `\`, "/")
	}
	return p
}

// NativePath turns a path a Git Bash command line used (/c/Users/me) into the
// Windows form (C:\Users\me). Other paths are returned unchanged.
func NativePath(p string) string {
	isDrive := func(c byte) bool { return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') }
	switch {
	case len(p) >= 3 && p[0] == '/' && isDrive(p[1]) && p[2] == '/':
		return strings.ToUpper(p[1:2]) + ":" + strings.ReplaceAll(p[2:], "/", `\`)
	case len(p) == 2 && p[0] == '/' && isDrive(p[1]):
		return strings.ToUpper(p[1:2]) + `:\`
	}
	return p
}
