package backend

import (
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// ptyBackend drives Lectern's own PTY host through `lectern pty`, which
// speaks tmux's command language (docs/ptyhost.md).
type ptyBackend struct {
	cli
	bin string
}

// Pty returns the PTY-host backend driving the lectern binary at bin on the
// target.
func Pty(bin string) Backend {
	return ptyBackend{cli: cli{prog: shellq.Quote(bin) + " pty"}, bin: bin}
}

func (ptyBackend) Name() string { return NamePty }

// NewSession needs no extended keys: the host passes the program's keyboard
// requests and the browser's answers through untouched.
func (p ptyBackend) NewSession(o NewSession) string { return p.newSession(o) }

// SendText pastes the staged file in one command; tmux needs a buffer held
// between two.
func (p ptyBackend) SendText(target, stagePath string) string {
	q, f := p.q(target), p.q(stagePath)
	return p.prog + " paste-file -t " + q + " " + f + " && " +
		p.prog + " send-keys -t " + q + " Enter && rm -f " + f
}

// Poll and AgentProbe are native, so they need no shell tools on the target
// (base64, ps) and work the same in Git Bash on Windows.
func (p ptyBackend) Poll(names []string, lines int) string {
	return p.prog + " poll -S " + itoa(lines) + " -- " + quoteAll(names)
}

func (p ptyBackend) AgentProbe(names []string) string {
	return AgentProbeMarker + " " + p.prog + " probe " + quoteAll(names)
}

// Discover is not offered: the PTY host holds only what Lectern started, so
// there is nothing started by hand to find. Adoption is tmux-only.
func (ptyBackend) Discover() (string, bool) { return "", false }

func (p ptyBackend) AttachArgv(session string, web bool) []string {
	return []string{p.bin, "pty", "attach-session", "-t", Exact(session)}
}

func (p ptyBackend) ShellArgv(session, workdir string, web bool) []string {
	return []string{p.bin, "pty", "new-session", "-A", "-s", session, "-c", workdir,
		"--", "/bin/sh", "-c", `exec "${SHELL:-/bin/sh}" -i`}
}

func quoteAll(words []string) string {
	out := make([]string, len(words))
	for i, w := range words {
		out[i] = shellq.Quote(w)
	}
	return strings.Join(out, " ")
}
