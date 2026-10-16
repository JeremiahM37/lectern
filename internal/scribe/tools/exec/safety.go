package exec

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Options are the config-supplied command rules.
//
//   - Deny: a command containing any entry (case-insensitive substring) is
//     refused outright, in every mode.
//   - Allow: a command that starts with an entry, on a word boundary, and
//     contains no shell control operators, skips the destructive-command
//     prompt. It never skips the refusals below or Deny.
//   - ToolTimeout: the limit for a shell or project command that sets no
//     timeout_ms (config tool_timeout, see config.ClampToolTimeout). Zero keeps
//     the built-in default. Forbidden paths are not an option here: the shell
//     reads them from fs.Patterns, the same list the file tools use.
type Options struct {
	Allow       []string
	Deny        []string
	ToolTimeout time.Duration
}

// refusal explains why a command is refused without asking. These are not
// configurable: they protect processes the session did not start.
type refusal struct {
	re  *regexp.Regexp
	msg string
}

var hardRefusals = []refusal{
	{
		regexp.MustCompile(`\b(pkill|killall)\b`),
		"refused: pkill and killall match processes by name and can hit processes this session does not own. " +
			"Kill the specific PID you started instead (background jobs can be stopped with shell_kill), " +
			"or find it with pgrep and kill that PID.",
	},
	{
		regexp.MustCompile(`\btmux\s+kill-(server|session)\b`),
		"refused: tmux kill-server and kill-session can end sessions this agent did not create. " +
			"Leave tmux alone; if a tmux pane needs to close, ask the user.",
	},
}

// destructive rules, checked in order; the first match gives the description
// shown in the approval prompt. rm is special-cased in classifyDestructive.
var destructiveRules = []struct {
	re   *regexp.Regexp
	desc string
}{
	{regexp.MustCompile(`\bgit\s+push\b[^;&|]*(\s--force(\s|=|$|-with-lease)|\s-f(\s|$))`),
		"force push (may overwrite remote history)"},
	{regexp.MustCompile(`\bgit\s+reset\b[^;&|]*--hard\b`), "hard reset (discards uncommitted changes)"},
	{regexp.MustCompile(`\bgit\s+clean\b[^;&|]*(\s-[a-zA-Z]*f|\s--force)`), "git clean (deletes untracked files)"},
	{regexp.MustCompile(`\bgit\s+checkout\s+--(\s|$)`), "discard working-tree changes (git checkout --)"},
	{regexp.MustCompile(`\bgit\s+branch\b[^;&|]*\s-D\b`), "force-delete a branch"},
	{regexp.MustCompile(`\bgit\s+rebase\b`), "rebase (rewrites history)"},
	{regexp.MustCompile(`(?i)\bdrop\s+(table|database|schema)\b`), "drop database objects"},
	{regexp.MustCompile(`(?i)\btruncate\s+table\b`), "truncate a table"},
	{regexp.MustCompile(`\bmkfs(\.\w+)?\b`), "format a filesystem"},
	{regexp.MustCompile(`\bdd\s+if=`), "raw disk write (dd)"},
	{regexp.MustCompile(`>\s*/dev/sd`), "write to a block device"},
	{regexp.MustCompile(`\bkill\s+-(9|KILL)\b`), "force-kill processes (kill -9)"},
	{regexp.MustCompile(`\bchmod\s+(-[A-Za-z]+\s+)*0?777\b`), "chmod 777 (world-writable)"},
	{regexp.MustCompile(`(^|[;&|(\s])(shutdown|reboot|poweroff|halt)\b`), "shut down or reboot the machine"},
	{regexp.MustCompile(`\b(curl|wget)\b[^|]*\|\s*(sudo\s+)?(ba|z|da|k)?sh\b`), "pipe remote script to shell"},
}

var rmRule = regexp.MustCompile(`(^|[;&|(\s])rm\s+((-[a-zA-Z]*[rRf][a-zA-Z]*|--recursive|--force)\s+)+(.*)$`)

// refusalFor returns a refusal message when command must never run.
func refusalFor(command string) (string, bool) {
	for _, r := range hardRefusals {
		if r.re.MatchString(command) {
			return r.msg, true
		}
	}
	return "", false
}

// classifyDestructive returns a human description of the destructive
// operation in command, or ok=false when the command is not destructive.
// workdir decides whether rm targets are inside the project.
func classifyDestructive(command, workdir string) (desc string, ok bool) {
	if m := rmRule.FindStringSubmatch(command); m != nil {
		return rmDescription(strings.Fields(m[len(m)-1]), workdir), true
	}
	for _, r := range destructiveRules {
		if r.re.MatchString(command) {
			return r.desc, true
		}
	}
	return "", false
}

// rmDescription names what a recursive or forced rm reaches.
func rmDescription(targets []string, workdir string) string {
	for _, t := range targets {
		if strings.HasPrefix(t, "-") {
			continue
		}
		switch {
		case t == "/" || t == "/*":
			return "delete files: target is the filesystem root"
		case t == "~" || strings.HasPrefix(t, "~/") || t == "$HOME" || strings.HasPrefix(t, "$HOME/"):
			return "delete files in the home directory"
		case t == "*" || t == ".*":
			return "delete everything matching a wildcard"
		}
		if filepath.IsAbs(t) {
			if !within(workdir, t) {
				return "delete files outside the project (" + t + ")"
			}
			continue
		}
		if !within(workdir, filepath.Join(workdir, t)) {
			return "delete files outside the project (" + t + ")"
		}
	}
	return "delete files or directories"
}

// within reports whether path lies inside root (lexical, no symlink resolution).
func within(root, path string) bool {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// denied reports whether command matches a Deny entry.
func (o Options) denied(command string) (string, bool) {
	c := normalize(command)
	for _, d := range o.Deny {
		if e := normalize(d); e != "" && strings.Contains(c, e) {
			return d, true
		}
	}
	return "", false
}

// allowed reports whether command matches an Allow entry and is a single
// simple command (no chaining, redirection or substitution).
func (o Options) allowed(command string) bool {
	if strings.ContainsAny(command, ";&|`$()<>\n\r") {
		return false
	}
	c := normalize(command)
	for _, a := range o.Allow {
		e := normalize(a)
		if e != "" && (c == e || strings.HasPrefix(c, e+" ")) {
			return true
		}
	}
	return false
}

func normalize(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}
