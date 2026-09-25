// Package onboard holds the small set of environment checks a brand-new
// install needs answered before its first session: which agent CLIs are on
// PATH, and whether tmux/git are available. It exists as its own package
// because two very different callers need the exact same answers — the
// terminal `lectern doctor` command and the web app's first-run checklist
// (GET /api/onboarding) — and they must never drift out of agreement about
// what "found" means.
package onboard

import (
	"os"
	"os/exec"
	"runtime"
)

// AgentCheck reports whether one agent CLI was found. Builtin agents (claude,
// codex, gemini) already work the moment a session picks their name — nothing
// to register. Everything else Lectern recognizes by binary name is still
// usable in a session's own shell, but needs its own entry in `PUT /api/agents`
// (see docs/agents.md) before Lectern can launch it as a first-class agent.
type AgentCheck struct {
	Name    string `json:"name"`
	Found   bool   `json:"found"`
	Path    string `json:"path,omitempty"`
	Builtin bool   `json:"builtin"`
}

// extraAgents are CLIs Lectern does not ship an adapter for, but whose
// presence is worth surfacing during onboarding — a person who already has
// one installed is the person most likely to want it wired up next. Kept
// short and only includes binaries this codebase can name with confidence;
// guessing at a flag or a command that doesn't exist would be worse than
// leaving a CLI undetected.
var extraAgents = []string{"opencode", "aider", "goose"}

// DetectAgents looks up the three builtin agents (honoring any configured
// override binary name, same as internal/config.Config.{Claude,Codex,Gemini}Bin)
// plus a short list of other known CLIs, all via the caller's PATH.
func DetectAgents(claudeBin, codexBin, geminiBin string) []AgentCheck {
	builtin := []struct{ name, bin string }{
		{"claude", nonEmpty(claudeBin, "claude")},
		{"codex", nonEmpty(codexBin, "codex")},
		{"gemini", nonEmpty(geminiBin, "gemini")},
	}
	out := make([]AgentCheck, 0, len(builtin)+len(extraAgents))
	for _, a := range builtin {
		out = append(out, lookup(a.name, a.bin, true))
	}
	for _, name := range extraAgents {
		out = append(out, lookup(name, name, false))
	}
	return out
}

func lookup(name, bin string, builtin bool) AgentCheck {
	path, err := exec.LookPath(bin)
	return AgentCheck{Name: name, Found: err == nil, Path: path, Builtin: builtin}
}

func nonEmpty(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// EnvCheck is one environment precondition — tmux, git — with an actionable
// fix when it's missing. Fix is deliberately generic (not per-distro): the
// per-distro install line lives in install.sh, which runs before any of this
// code exists on the machine.
type EnvCheck struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail,omitempty"`
	Fix    string `json:"fix,omitempty"`
}

// CheckTmux reports whether tmux is on PATH. tmux is required on any machine
// that runs agents directly (local target, or a remote target reached over
// SSH); the CLI itself only needs it when acting as that machine.
func CheckTmux() EnvCheck {
	return checkBinary("tmux", "tmux", "install tmux (see install.sh's distro hint, or your package manager) — it's what keeps an agent's session alive between visits")
}

// CheckGit reports whether git is on PATH. Every dispatched task and every
// registered project needs it.
func CheckGit() EnvCheck {
	return checkBinary("git", "git", "install git — every project and task worktree needs it")
}

func checkBinary(name, bin, fix string) EnvCheck {
	path, err := exec.LookPath(bin)
	if err != nil {
		return EnvCheck{Name: name, OK: false, Detail: "not found on PATH", Fix: fix}
	}
	return EnvCheck{Name: name, OK: true, Detail: path}
}

// Headless reports whether this process looks like it has no way to open a
// browser window for the operator — no display server on Linux, and no
// obvious desktop session. It is a heuristic, not a guarantee; callers use it
// to decide whether attempting `xdg-open`/`open`/`start` is worth trying, not
// to hide functionality.
func Headless() bool {
	if runtime.GOOS != "linux" {
		return false
	}
	return envEmpty("DISPLAY") && envEmpty("WAYLAND_DISPLAY")
}

func envEmpty(key string) bool {
	v, ok := os.LookupEnv(key)
	return !ok || v == ""
}
