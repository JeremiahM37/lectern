// Package accounts is everything CLI-specific about running one agent CLI
// under several logins (docs/accounts.md): which environment variable moves a
// CLI's config directory, how that CLI lays out a conversation inside it, how
// to sign an account in, and which account a usage limit should move work to.
//
// An account is a directory on the target holding one login. Lectern creates
// the directory (mode 0700) and points the CLI at it; it never reads the
// credentials inside. It only checks whether the CLI's credential file exists.
package accounts

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// Supported reports whether Lectern can run this CLI under more than one
// login: it must have a config-directory variable and a resumable
// conversation layout Lectern can move between directories.
func Supported(agent string) bool { return EnvKey(agent) != "" }

// Agents lists the CLIs with account support, in display order.
func Agents() []string { return []string{"claude", "codex", "gemini"} }

// EnvKey is the variable that moves a CLI's whole config (credentials and
// conversations) to another directory, or "" for a CLI without one.
func EnvKey(agent string) string {
	switch agent {
	case "claude":
		return "CLAUDE_CONFIG_DIR"
	case "codex":
		return "CODEX_HOME"
	case "gemini":
		// gemini-cli keeps everything under $GEMINI_CLI_HOME/.gemini
		// (it falls back to the user's home directory).
		return "GEMINI_CLI_HOME"
	}
	return ""
}

// defaultDir is the shell expression for where the CLI keeps its config when
// the variable is not set.
func defaultDir(agent string) string {
	switch agent {
	case "claude":
		return `"${CLAUDE_CONFIG_DIR:-$HOME/.claude}"`
	case "codex":
		return `"${CODEX_HOME:-$HOME/.codex}"`
	case "gemini":
		return `"${GEMINI_CLI_HOME:-$HOME}"`
	}
	return `"$HOME"`
}

// DirExpr renders a config directory as one shell word: dir when it is set,
// otherwise the CLI's own default, expanded by the target shell.
func DirExpr(agent, dir string) string {
	if dir == "" {
		return defaultDir(agent)
	}
	return shellq.Quote(dir)
}

// credentialFile is the file, relative to the account directory, whose
// presence means the CLI has been signed in there. Only its existence is
// checked; Lectern never opens it.
func credentialFile(agent string) string {
	switch agent {
	case "claude":
		return ".credentials.json"
	case "codex":
		return "auth.json"
	case "gemini":
		return ".gemini/oauth_creds.json"
	}
	return ""
}

// LoginCommand is the CLI's own sign-in, run in a terminal with the account's
// directory in the environment.
func LoginCommand(agent string) string {
	switch agent {
	case "claude":
		return "claude auth login"
	case "codex":
		return "codex login"
	case "gemini":
		// gemini-cli has no separate login command: its first run asks.
		return "gemini"
	}
	return ""
}

var labelSlug = regexp.MustCompile(`[^a-z0-9]+`)

// ValidLabel checks a user-chosen account label: short, printable, one line.
func ValidLabel(label string) error {
	label = strings.TrimSpace(label)
	if label == "" || len([]rune(label)) > 40 {
		return fmt.Errorf("label must be 1-40 characters")
	}
	for _, r := range label {
		if r < 0x20 || r == 0x7f {
			return fmt.Errorf("label must be one line of text")
		}
	}
	return nil
}

// CreateDirCommand creates (or adopts) an account directory on the target
// and prints its absolute path. dir empty means Lectern's own location,
// ~/.lectern/accounts/<agent>/<label slug>. The directory is made private
// whether or not it already existed.
func CreateDirCommand(agent, label, dir string) string {
	word := shellq.Quote(dir)
	if dir == "" {
		slug := strings.Trim(labelSlug.ReplaceAllString(strings.ToLower(label), "-"), "-")
		if slug == "" {
			slug = "account"
		}
		word = shellq.HomePath("/.lectern/accounts/" + agent + "/" + slug)
	}
	return "umask 077; d=" + word + ` && mkdir -p -- "$d" && chmod 700 -- "$d" && cd -- "$d" && pwd -P`
}

// StatusCommand prints "signed-in" when the CLI's credential file exists in
// the directory, without reading it.
func StatusCommand(agent, dir string) string {
	return "test -s " + DirExpr(agent, dir) + "/" + credentialFile(agent) + " && echo signed-in || echo signed-out"
}

// UnknownResetHold is how long an account that hit its limit without naming a
// reset is treated as still limited: Claude's shortest window is 5 hours.
const UnknownResetHold = 5 * time.Hour

// Candidate is one account as the selection sees it.
type Candidate struct {
	ID    int64
	Label string
	// LimitedAt/LimitedUntil are the last limit recorded against the account.
	LimitedAt    time.Time
	LimitedUntil time.Time
	// UsagePct and UsageReset are the fullest window the CLI itself last
	// reported for the account (Claude's statusline, Codex's rollout), when
	// known. 100 with a reset ahead means limited even without a hold.
	UsagePct   int
	UsageReset time.Time
}

// BlockedUntil reports when a candidate is next usable, or zero when it is
// usable now.
func (c Candidate) BlockedUntil(now time.Time) time.Time {
	var until time.Time
	if !c.LimitedUntil.IsZero() && c.LimitedUntil.After(now) {
		until = c.LimitedUntil
	} else if c.LimitedUntil.IsZero() && !c.LimitedAt.IsZero() && now.Sub(c.LimitedAt) < UnknownResetHold {
		until = c.LimitedAt.Add(UnknownResetHold)
	}
	if c.UsagePct >= 100 && c.UsageReset.After(now) && c.UsageReset.After(until) {
		until = c.UsageReset
	}
	return until
}

// Next picks the account to move to from current: the next one in rotation
// order after it that is not limited now. ok is false when every other
// account is limited (or there is none); soonest is then the earliest time
// one of them becomes usable, zero when unknown.
func Next(cands []Candidate, current int64, now time.Time) (next Candidate, soonest time.Time, ok bool) {
	start := 0
	for i, c := range cands {
		if c.ID == current {
			start = i + 1
			break
		}
	}
	for i := 0; i < len(cands); i++ {
		c := cands[(start+i)%len(cands)]
		if c.ID == current {
			continue
		}
		blocked := c.BlockedUntil(now)
		if blocked.IsZero() {
			return c, time.Time{}, true
		}
		if soonest.IsZero() || blocked.Before(soonest) {
			soonest = blocked
		}
	}
	return Candidate{}, soonest, false
}
