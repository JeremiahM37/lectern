package backend

import (
	"encoding/json"
	"os/exec"
	"runtime"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/gitbash"
	"github.com/JeremiahM37/lectern/v2/internal/ptyhost"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Setting values for LECTERN_SESSION_BACKEND.
const (
	SettingAuto = "auto"
	SettingTmux = "tmux"
	SettingPty  = "pty"
)

// Resolver decides each target's TargetEnv when its executor is made
// (executor.Registry.Env). It must not reach a remote target: it decides from
// the target's row, its stored probe, and this machine.
type Resolver struct {
	// Setting is auto, tmux or pty.
	Setting string
	// Self is this lectern binary; "" disables everything that runs it.
	Self string
	GOOS string
	// LookPath finds tmux on this machine.
	LookPath func(string) (string, error)
	// HoldsSessions reports whether a backend already keeps Lectern sessions
	// on this machine, so auto never hides running ones by switching.
	HoldsSessions func(backend string) bool
}

// NewResolver is a Resolver for this machine.
func NewResolver(setting, self string) Resolver {
	return Resolver{Setting: setting, Self: self, GOOS: runtime.GOOS, LookPath: exec.LookPath,
		HoldsSessions: localSessionsIn}
}

// Env is executor.Registry.Env.
func (r Resolver) Env(t *store.Target, _ executor.Executor) executor.TargetEnv {
	switch t.Kind {
	case "local":
		if r.Self == "" {
			return executor.TargetEnv{SessionBackend: NameTmux}
		}
		return executor.TargetEnv{SessionBackend: r.Local(), Lectern: ShellPath(r.Self, r.GOOS)}
	case "ssh", "pct":
		probe := RemoteLectern(t.InfoJSON)
		env := executor.TargetEnv{SessionBackend: NameTmux, Lectern: probe.Path, Helpers: probe.Helpers}
		if env.Helpers == nil {
			env.Helpers = []string{} // a remote lectern runs only what it listed
		}
		switch r.setting() {
		case SettingPty:
			if probe.Pty {
				env.SessionBackend = NamePty
			}
		case SettingAuto:
			if probe.Pty && !probe.Tmux {
				env.SessionBackend = NamePty
			}
		}
		return env
	}
	// Sandboxes run their agents in a container through a per-attempt
	// executor; the host side needs nothing.
	return executor.TargetEnv{}
}

func (r Resolver) setting() string {
	switch s := strings.ToLower(strings.TrimSpace(r.Setting)); s {
	case SettingTmux, SettingPty:
		return s
	}
	return SettingAuto
}

// Local is the backend for this machine.
//
// auto keeps whichever backend already holds Lectern sessions, so neither
// installing tmux nor upgrading Lectern hides a running session. With none,
// it is the PTY host on Windows and macOS, and tmux on Linux when tmux is
// installed.
func (r Resolver) Local() string {
	switch r.setting() {
	case SettingTmux:
		return NameTmux
	case SettingPty:
		return NamePty
	}
	if r.GOOS == "windows" {
		return NamePty
	}
	_, tmuxErr := r.LookPath("tmux")
	if r.HoldsSessions != nil {
		if r.HoldsSessions(NamePty) {
			return NamePty
		}
		if tmuxErr == nil && r.HoldsSessions(NameTmux) {
			return NameTmux
		}
	}
	if r.GOOS == "darwin" || tmuxErr != nil {
		return NamePty
	}
	return NameTmux
}

// ShellPath is how a command line on this machine names a file: as it is,
// or in Git Bash's /c/... form on Windows.
func ShellPath(p, goos string) string {
	if goos == "windows" {
		return gitbash.PosixPath(p)
	}
	return p
}

// LecternProbe is what a target's probe found out about lectern on it.
type LecternProbe struct {
	Path    string
	Pty     bool
	Tmux    bool
	Helpers []string
}

// RemoteLectern reads the lectern binary and its capabilities from a target's
// stored probe (executor.Probe's "lectern" entry: the path, then one
// capability per line, as `lectern helper --capabilities` prints them).
func RemoteLectern(infoJSON string) LecternProbe {
	var info map[string]any
	if json.Unmarshal([]byte(infoJSON), &info) != nil {
		return LecternProbe{}
	}
	var p LecternProbe
	p.Tmux = info["tmux"] != nil
	raw, _ := info["lectern"].(string)
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) == 0 || !strings.HasPrefix(strings.TrimSpace(lines[0]), "/") {
		return p
	}
	p.Path = strings.TrimSpace(lines[0])
	for _, line := range lines[1:] {
		line = strings.TrimSpace(line)
		if line == "pty" {
			p.Pty = true
		} else if name, ok := strings.CutPrefix(line, "helper:"); ok {
			p.Helpers = append(p.Helpers, name)
		}
	}
	return p
}

// localSessionsIn reports whether a backend on this machine holds sessions
// Lectern started. It asks only this user's own tmux server or PTY host, and
// only reads.
func localSessionsIn(name string) bool {
	switch name {
	case NamePty:
		socket, err := ptyhost.SocketPath()
		if err != nil {
			return false
		}
		c, err := ptyhost.Dial(socket)
		if err != nil {
			return false
		}
		defer c.Close()
		res, err := c.Do(ptyhost.Request{Op: "list"})
		return err == nil && len(res.Sessions) > 0
	case NameTmux:
		out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
		if err != nil {
			return false
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.HasPrefix(line, "lec-") || strings.HasPrefix(line, "adk-") {
				return true
			}
		}
	}
	return false
}
