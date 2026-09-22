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
