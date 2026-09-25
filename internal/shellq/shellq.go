// Package shellq renders values as shell words.
//
// Commands lectern builds are executed by a real shell on the target, so every
// interpolated value has to survive as ONE argument no matter what is in it.
// Quoting only what needs quoting also keeps the generated commands readable in
// the timeline and in `tmux list-panes`.
package shellq

import (
	"regexp"
	"strings"
)

// safeWord matches the characters that need no quoting at all.
var safeWord = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// Quote renders one shell word.
func Quote(s string) string {
	if s != "" && safeWord.MatchString(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// HomePath renders "$HOME" followed by rel (which must start with "/") as
// ONE shell word that the target shell expands at run time.
//
// A naive `Quote("$HOME" + rel)` is a real, reproducible bug: `$` is not in
// safeWord's charset, so Quote falls back to single-quoting the whole
// string — which disables `$HOME` expansion entirely, leaving the shell to
// look for a literal subdirectory named "$HOME" relative to its cwd and fail
// with "No such file or directory". Confirmed against a real bash: this
// exact shape (`shellq.Quote("$HOME/...")`, as internal/agentevents'
// notify/hooks installers built their target path) breaks at the `cat >`
// that writes the installed script/settings file.
//
// The fix is the standard shell idiom of concatenating a double-quoted
// (expanding) segment directly against a second quoted-or-bare segment with
// no whitespace between them — bash joins adjacent word fragments into one
// argument, so `"$HOME"/.lectern/hooks/x` is one word whose first part
// expands and whose second part is Quote's ordinary literal rendering.
func HomePath(rel string) string {
	return `"$HOME"` + Quote(rel)
}
