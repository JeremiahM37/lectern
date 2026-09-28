// Package backend is the seam between Lectern and whatever keeps its terminals
// alive on a target: tmux, or Lectern's own PTY host (docs/ptyhost.md).
//
// A Backend builds shell command lines, it does not run them. That is the
// contract of the executor layer below it — a command runs on a target locally,
// over SSH or in a container — so both backends work on every target kind, and
// the parsers of what the commands print are shared.
//
// Both backends speak tmux's command language. `lectern pty` implements the
// subset Lectern uses, down to its target syntax, format language and error
// texts, because callers key on those. The Pty backend therefore differs from
// Tmux mainly in its program word, and replaces the few commands that lean on
// the target's shell tools with native ones.
package backend

import (
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// Names of the backends, as configured and as reported.
const (
	NameTmux = "tmux"
	NamePty  = "pty"
)

// Backend builds the commands that drive terminals on one target.
//
// Methods that take a target take it in tmux's syntax: Exact(name) is the
// session called exactly name, Pane(name) its active pane. A bare name is
// tmux's prefix match, kept only where a caller already relied on it.
type Backend interface {
	// Name is NameTmux or NamePty.
	Name() string
	// NewSession starts a detached session.
	NewSession(o NewSession) string
	// HasSession exits 0 when the target exists. Quiet discards stderr.
	HasSession(target string, quiet bool) string
	// KillSession ends a session. Quiet discards errors and always succeeds.
	KillSession(target string, quiet bool) string
	// KillIf ends session kill only when the tmux format cond is true in
	// target — how a session is stopped only while its tracking identity
	// still matches the one on record.
	KillIf(target, cond, kill string) string
	// SendText pastes a staged file into target as one bracketed paste,
	// presses Enter, and removes the file.
	SendText(target, stagePath string) string
	// SendKeys presses tmux-named keys in target.
	SendKeys(target string, keys ...string) string
	// CapturePane prints target's pane, with the last lines of history when
	// lines > 0, and wrapped lines joined when join is set.
	CapturePane(target string, lines int, join bool) string
	// Display prints a tmux format evaluated in target.
	Display(target, format string) string
	// ShowOption prints a session option's value, or nothing.
	ShowOption(target, option string) string
	// SetOptionOnce sets a session option only when it is not set yet.
	SetOptionOnce(target, option, value string) string
	// ShowEnvironment prints NAME=value from a session's environment.
	ShowEnvironment(target, name string) string
	// Poll captures the last lines of every named session in one command, in
	// the framing sessions.ParsePollSnapshot reads.
	Poll(names []string, lines int) string
	// AgentProbe reports each named session's root process, foreground
	// command and every process on its terminal, framed for
	// sessions.parseAgentProbe.
	AgentProbe(names []string) string
	// Discover lists every pane and process on the target, for adopting
	// agents started by hand. ok is false when the backend cannot.
	Discover() (cmd string, ok bool)
	// AttachArgv is the command a terminal runs to show an existing session.
	// web marks the browser terminal, which declares extended keys.
	AttachArgv(session string, web bool) []string
	// ShellArgv attaches to a shell session, creating it in workdir when it
	// does not exist yet.
	ShellArgv(session, workdir string, web bool) []string
}

// NewSession describes a session to start.
type NewSession struct {
	Name string
	// Dir is the starting directory; "" inherits the caller's.
	Dir string
	// Env entries (KEY=value) are set in the session's own environment,
	// where ShowEnvironment can read them back.
	Env []string
	// Argv, already rendered as shell words, is the program to run. It is
	// passed after "--", so the backend runs it without another shell.
	Argv string
	// Shell is one command line for the backend to run with a shell, used
	// when Argv is empty.
	Shell string
	// ExtendedKeys turns tmux's extended keys on before the program starts
	// (internal/tmuxkeys). The PTY host passes keys through as they are, so
	// it has nothing to turn on.
	ExtendedKeys bool
}

// Exact is the tmux target for exactly the session called name, never a
// prefix of another.
func Exact(name string) string { return "=" + name }

// Pane is the tmux target for the active pane of exactly session name.
func Pane(name string) string { return "=" + name + ":" }

// Tmux is the tmux backend.
var Tmux Backend = tmuxBackend{cli{prog: "tmux"}}

// For is the backend of the target an executor drives. A target whose
// environment was never resolved (tests, a sandbox's container) uses tmux.
func For(ex executor.Executor) Backend {
	return FromEnv(executor.TargetEnvOf(ex))
}

// FromEnv is the backend a resolved target environment names.
func FromEnv(env executor.TargetEnv) Backend {
	if env.SessionBackend == NamePty && env.Lectern != "" {
		return Pty(env.Lectern)
	}
	return Tmux
}

// cli renders tmux-language commands for a program word.
type cli struct{ prog string }

func (c cli) q(s string) string { return shellq.Quote(s) }

func (c cli) newSession(o NewSession) string {
	var b strings.Builder
	b.WriteString(c.prog + " new-session -d")
	for _, kv := range o.Env {
		b.WriteString(" -e " + c.q(kv))
	}
	b.WriteString(" -s " + c.q(o.Name))
	if o.Dir != "" {
		b.WriteString(" -c " + c.q(o.Dir))
	}
	if o.Argv != "" {
		b.WriteString(" -- " + o.Argv)
	} else {
		b.WriteString(" " + c.q(o.Shell))
	}
	return b.String()
}

func (c cli) HasSession(target string, quiet bool) string {
	cmd := c.prog + " has-session -t " + c.q(target)
	if quiet {
		cmd += " 2>/dev/null"
	}
	return cmd
}

func (c cli) KillSession(target string, quiet bool) string {
	cmd := c.prog + " kill-session -t " + c.q(target)
	if quiet {
		cmd += " 2>/dev/null || true"
	}
	return cmd
}

func (c cli) KillIf(target, cond, kill string) string {
	return c.prog + " if-shell -F -t " + c.q(target) + " " + c.q(cond) + " " + c.q("kill-session -t "+c.q(kill))
}

func (c cli) SendText(target, stagePath string) string {
	q, p := c.q(target), c.q(stagePath)
	return c.prog + " load-buffer -b lectern " + p + " && " +
		c.prog + " paste-buffer -b lectern -t " + q + " -d -p && " +
		c.prog + " send-keys -t " + q + " Enter && rm -f " + p
}

func (c cli) SendKeys(target string, keys ...string) string {
	return c.prog + " send-keys -t " + c.q(target) + " " + strings.Join(keys, " ")
}

func (c cli) CapturePane(target string, lines int, join bool) string {
	cmd := c.prog + " capture-pane -p -t " + c.q(target)
	if lines > 0 {
		cmd += " -S -" + itoa(lines)
	}
	if join {
		cmd += " -J"
	}
	return cmd
}

func (c cli) Display(target, format string) string {
	return c.prog + " display-message -p -t " + c.q(target) + " " + c.q(format)
}

func (c cli) ShowOption(target, option string) string {
	return c.prog + " show-options -qv -t " + c.q(target) + " " + option
}

func (c cli) SetOptionOnce(target, option, value string) string {
	return c.prog + " set-option -o -t " + c.q(target) + " " + option + " " + c.q(value)
}

func (c cli) ShowEnvironment(target, name string) string {
	return c.prog + " show-environment -t " + c.q(target) + " " + name
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
