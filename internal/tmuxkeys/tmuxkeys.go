// Package tmuxkeys turns on tmux's extended keys for Lectern's sessions, so a
// program can tell Shift+Enter from Enter through tmux (docs/workspace.md).
//
// tmux only records a program's request for extended keys (modifyOtherKeys,
// CSI > 4 ; n m) while its server-wide extended-keys option is on; a request
// made while it is off is dropped for good. So the option is set when Lectern
// creates a session, before the agent in it starts, and again when a browser
// attaches.
//
// `on` passes extended keys only to programs that ask (a shell still gets a
// plain Enter), and `always`, if the operator chose it, is left alone.
// extended-keys-format csi-u (tmux 3.5+) is the form kitty-protocol readers,
// Claude Code among them, parse; xterm-format readers such as vim read both.
// That format is a server option too, so it is only chosen when extended keys
// were off, i.e. not configured by anyone.
//
// Only tmux 3.5 and later, recognised by having extended-keys-format at all.
// Before 3.5, tmux drops a modified Enter (Shift+Enter, Ctrl+Enter) meant for
// a program that did not ask for extended keys, so turning them on would
// make Shift+Enter do nothing at a shell prompt on tmux 3.2-3.4. There the
// option is left as it is, and so are the browser's keys (WebAttachArgv).
package tmuxkeys

import (
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// Commands is the tmux command, as argv words, that turns extended keys on.
var Commands = []string{"if-shell", "-F", "#{?#{extended-keys-format},#{==:#{extended-keys},off},0}",
	"set-option -sq extended-keys on ; set-option -sq extended-keys-format csi-u"}

// Suffix follows a `tmux new-session ...` shell command, running Commands in
// the same tmux invocation, right after the session is made.
func Suffix() string {
	words := append([]string{";"}, Commands...)
	for i, word := range words {
		words[i] = shellq.Quote(word)
	}
	return " " + strings.Join(words, " ")
}
