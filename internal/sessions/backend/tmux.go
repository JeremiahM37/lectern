package backend

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/tmuxkeys"
)

// PollEnd terminates a batched poll or agent probe, so a truncated answer is
// never mistaken for a complete one.
const PollEnd = "ADK-POLL-END-v2"

// AgentProbeMarker starts the agent probe command; the mock executor keys on it.
const AgentProbeMarker = ": LECTERN-AGENT-PROBE;"

// DiscoverDelimiter separates the pane listing from the process listing.
const DiscoverDelimiter = "\x1e---LECTERN-PS---\x1e"

type tmuxBackend struct{ cli }

func (tmuxBackend) Name() string { return NameTmux }

func (t tmuxBackend) NewSession(o NewSession) string {
	cmd := t.newSession(o)
	if o.ExtendedKeys {
		cmd += tmuxkeys.Suffix()
	}
	return cmd
}

// Poll encodes payloads so a pane cannot forge another pane's frame. Known
// tmux absence errors are told apart from socket permissions, bad arguments
// and the like, which must never read as a dead session.
func (t tmuxBackend) Poll(names []string, lines int) string {
	var command strings.Builder
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			continue
		}
		seen[name] = true
		fmt.Fprintf(&command, `if lec_poll_text=$(LC_ALL=C %s capture-pane -p -t %s -S -%d 2>&1); then lec_poll_state=ok; else case "$lec_poll_text" in "can't find session:"*|"no server running on "*|"error connecting to "*" (No such file or directory)") lec_poll_state=missing; lec_poll_text='' ;; *) lec_poll_state=error ;; esac; fi; lec_poll_payload=$(printf '%%s' "$lec_poll_text" | base64) || exit 1; lec_poll_payload=$(printf '%%s' "$lec_poll_payload" | tr -d '\r\n') || exit 1; printf '%%s\t%%s\t%%s\n' %s "$lec_poll_state" "$lec_poll_payload"; `,
			t.prog, shellq.Quote(Pane(name)), lines, shellq.Quote(b64(name)))
	}
	fmt.Fprintf(&command, "printf '%%s\\n' %s", shellq.Quote(PollEnd))
	return command.String()
}

// AgentProbe asks tmux for each pane's root pid, foreground command and tty,
// then ps for the processes behind them — base64 framed like the poll.
func (t tmuxBackend) AgentProbe(names []string) string {
	var b strings.Builder
	b.WriteString(AgentProbeMarker + " ")
	for _, name := range names {
		// One field per display-message: tmux 3.5 prints a tab inside a
		// format as "_", so a tab-separated triple cannot be split again.
		q := shellq.Quote(Pane(name))
		fmt.Fprintf(&b, `if lec_pid=$(%s display-message -p -t %s '#{pane_pid}' 2>/dev/null); then `+
			`lec_cur=$(%s display-message -p -t `+q+` '#{pane_current_command}' 2>/dev/null); lec_tty=$(%s display-message -p -t `+q+` '#{pane_tty}' 2>/dev/null); `+
			`lec_root=$(ps -o args= -p "$lec_pid" 2>/dev/null); lec_all=$(ps -o args= -t "${lec_tty#/dev/}" 2>/dev/null); `+
			`printf '%%s\tok\t%%s\t%%s\t%%s\n' %s "$(printf '%%s' "$lec_root" | base64 | tr -d '\r\n')" "$(printf '%%s' "$lec_cur" | base64 | tr -d '\r\n')" "$(printf '%%s' "$lec_all" | base64 | tr -d '\r\n')"; `+
			`else printf '%%s\tmissing\t\t\t\n' %s; fi; `,
			t.prog, q, t.prog, t.prog, shellq.Quote(b64(name)), shellq.Quote(b64(name)))
	}
	fmt.Fprintf(&b, "printf '%%s\\n' %s", shellq.Quote(PollEnd))
	return b.String()
}

// Discover is one command, two listings, joined later on the pane's tty —
// `pane_current_command` is not usable, because an agent launched from a
// login shell leaves bash in the foreground of the pane.
func (t tmuxBackend) Discover() (string, bool) {
	return t.prog + " list-panes -a -F '#{session_name}\t#{pane_tty}\t#{pane_current_path}' " +
		"2>/dev/null; printf '%s' " + "'" + DiscoverDelimiter + "'" +
		"; ps -eo tty=,args= 2>/dev/null", true
}

// extkeysProbe picks the client flag by the target's tmux version. -T (and
// the extkeys feature) arrived in tmux 3.2, and an older tmux refuses an
// unknown flag outright, which would leave the browser with no terminal.
// tmux 3.2-3.4 accept it but then drop Shift+Enter and Ctrl+Enter meant for a
// program that did not ask for extended keys (a shell), and send Ctrl+letters
// in legacy form even to one that did, so the browser only declares extended
// keys to tmux 3.5 and later (see tmuxkeys).
//
// The browser also declares OSC 8 hyperlinks (tmux 3.4 and later), so a link
// an agent prints — Claude Code's Markdown links to files, say — reaches the
// browser's terminal as one (docs/files.md, terminal links).
const extkeysProbe = `case "$(tmux -V 2>/dev/null)" in "tmux "[0-2].*|"tmux 3."[0-3]|"tmux 3."[0-3][!0-9]*) set -- ;; "tmux 3.4"|"tmux 3.4"[!0-9]*) set -- -T hyperlinks ;; *) set -- -T extkeys,hyperlinks ;; esac; exec tmux "$@"`

func (t tmuxBackend) AttachArgv(session string, web bool) []string {
	return t.attach([]string{"tmux", "attach", "-t", session}, session, web)
}

// ShellArgv uses `new-session -A`, which attaches if the session is already
// there and creates it otherwise, so reopening a shell returns to the same
// one with its history and whatever was half-typed.
//
// An empty command inherits tmux's default-command, which may launch an
// agent. SHELL is resolved on the target, not on the control-plane host, and
// a real shell is passed explicitly.
func (t tmuxBackend) ShellArgv(session, workdir string, web bool) []string {
	return t.attach([]string{"tmux", "new-session", "-A", "-s", session, "-c", workdir,
		"--", "/bin/sh", "-c", `exec "${SHELL:-/bin/sh}" -i`}, session, web)
}

func (t tmuxBackend) attach(inner []string, session string, web bool) []string {
	// One pane has one grid, so one client decides its size. `latest` gives it
	// to the client that last attached, typed or resized, and the browser
	// re-announces its size whenever its terminal is shown or focused, so the
	// screen being used is drawn at its own size. `smallest` let a forgotten
	// client (a sleeping phone, a hidden tab, a split pane) pin every other
	// view to a sliver of Claude's input box padded with tmux's dots until the
	// session was restarted. Apply this to the attached window, never to the
	// user's global tmux options. Queue it after new-session so companion
	// shells are created before targeting.
	inner = append(inner, ";", "set-option", "-w", "-t", Pane(session), "window-size", "latest")
	if !web {
		return inner
	}
	// The server options go first: tmux asks the outer terminal for extended
	// keys when the client starts, not when the option changes. (tmuxkeys:
	// the session's own launch did this already; a session Lectern adopted,
	// or a server restarted since, has it done here.)
	words := append(append(append([]string(nil), tmuxkeys.Commands...), ";"), inner[1:]...)
	for i, word := range words {
		words[i] = shellq.Quote(word)
	}
	return []string{"sh", "-c", extkeysProbe + " " + strings.Join(words, " ")}
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
