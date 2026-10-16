package fs

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// Forbidden path patterns are hard blocks the user configures, unlike the
// secret-name rules in security.go, which only ask. Three sources are merged:
//
//   - the global file <config home>/forbidden.json,
//   - the project file <workdir>/.scribe/forbidden.json,
//   - session patterns passed in env.Services["forbidden"] as []string.
//
// The two files hold {"patterns": ["*.log", "secrets/**"]}. A pattern is a
// glob matched case-insensitively, anchored at a path-segment boundary, and
// tested against both the real path and its base name.

// forbiddenRule is one compiled pattern.
type forbiddenRule struct {
	pattern string
	re      *regexp.Regexp
}

// Patterns returns every forbidden glob in force for env: the list from
// config.ForbiddenPatterns (config key, global file, project file) plus the
// session list in env.Services["forbidden"]. The file tools and the shell both
// read it, so one list governs both. A broken forbidden file is an error:
// failing open would expose the paths the user meant to hide.
func Patterns(env *tools.Env) ([]string, error) {
	root := ""
	if env != nil {
		root = env.Workdir
	}
	pats, err := config.ForbiddenPatterns(root)
	if err != nil {
		return nil, err
	}
	if env != nil {
		if s, ok := tools.Service[[]string](env, "forbidden"); ok {
			pats = append(pats, s...)
		}
	}
	return pats, nil
}

// loadForbidden returns every active rule for env.
func loadForbidden(env *tools.Env) ([]forbiddenRule, error) {
	pats, err := Patterns(env)
	if err != nil {
		return nil, err
	}
	return compileForbidden(pats)
}

// CommandHit names a forbidden path found in a shell command.
type CommandHit struct {
	Path    string // the word of the command that matched
	Pattern string // the pattern it matched
}

// MatchCommand returns the first word of command that names a path matching
// one of pats. Words are matched the way file tools match paths: the word as
// written and its base name, anchored at a path-segment boundary. Flags and
// redirection noise are skipped. An unusable pattern is an error.
func MatchCommand(pats []string, command string) (CommandHit, bool, error) {
	if len(pats) == 0 {
		return CommandHit{}, false, nil
	}
	rules, err := compileForbidden(pats)
	if err != nil {
		return CommandHit{}, false, err
	}
	for _, raw := range strings.Fields(command) {
		tok := cleanCommandWord(raw)
		if tok == "" || strings.HasPrefix(tok, "-") {
			continue
		}
		if r := matchForbidden(rules, filepath.Clean(tok)); r != nil {
			return CommandHit{Path: tok, Pattern: r.pattern}, true, nil
		}
	}
	return CommandHit{}, false, nil
}

// cleanCommandWord strips redirection operators and quotes from a shell word.
func cleanCommandWord(t string) string {
	t = strings.TrimLeft(t, "<>&0123456789")
	return strings.Trim(t, `"'`)
}

// compileForbidden turns glob patterns into rules. Blank patterns are
// skipped.
func compileForbidden(pats []string) ([]forbiddenRule, error) {
	var out []forbiddenRule
	for _, p := range pats {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		re, err := regexp.Compile("(?i)(^|/)" + globToRegexp(p) + "$")
		if err != nil {
			return nil, fmt.Errorf("forbidden pattern %q is not usable: %v", p, err)
		}
		out = append(out, forbiddenRule{pattern: p, re: re})
	}
	return out, nil
}

// globToRegexp converts a glob to a regular expression body: ** crosses
// directories, * and ? do not.
func globToRegexp(glob string) string {
	var sb strings.Builder
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*' && i+1 < len(glob) && glob[i+1] == '*':
			if i+2 < len(glob) && glob[i+2] == '/' {
				// "**/" matches zero or more whole directories.
				sb.WriteString("(?:.*/)?")
				i += 2
			} else {
				sb.WriteString(".*")
				i++
			}
		case c == '*':
			sb.WriteString("[^/]*")
		case c == '?':
			sb.WriteString("[^/]")
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	return sb.String()
}

// matchForbidden returns the first rule matching real (the symlink-resolved
// absolute path) by its full forward-slash form or its base name. It returns
// nil when nothing matches.
func matchForbidden(rules []forbiddenRule, real string) *forbiddenRule {
	if len(rules) == 0 {
		return nil
	}
	full := filepath.ToSlash(real)
	base := filepath.Base(real)
	for i := range rules {
		if rules[i].re.MatchString(full) || rules[i].re.MatchString(base) {
			return &rules[i]
		}
	}
	return nil
}

// checkForbidden is the gate resolve applies to every path a tool touches.
func checkForbidden(env *tools.Env, t target) error {
	rules, err := loadForbidden(env)
	if err != nil {
		return err
	}
	if r := matchForbidden(rules, t.real); r != nil {
		return forbiddenErr(t.rel, r.pattern)
	}
	return nil
}

// forbiddenErr is the refusal for a forbidden path. The tag lets the UI and
// hooks recognise it.
func forbiddenErr(rel, pattern string) error {
	return &taggedError{
		tag: "forbidden",
		msg: fmt.Sprintf("Access denied: %q matches forbidden pattern %q. Do not try another route to this file; choose a different one or ask the user.", rel, pattern),
	}
}

// taggedError carries a machine tag next to its message. fail and the
// tools that return errors read it to set Result.Tag.
type taggedError struct {
	tag string
	msg string
}

func (e *taggedError) Error() string { return e.msg }

// failTagged converts err to an error Result, carrying its tag if it has
// one.
func failTagged(err error) tools.Result {
	var te *taggedError
	if errors.As(err, &te) {
		return tools.Result{Output: te.msg, IsError: true, Tag: te.tag}
	}
	return fail(err)
}

// tagOf returns the machine tag carried by err, or "" when it has none.
func tagOf(err error) string {
	var te *taggedError
	if errors.As(err, &te) {
		return te.tag
	}
	return ""
}

// errTag is an error Result for a formatted message with a machine tag.
func errTag(tag, format string, a ...any) tools.Result {
	res := tools.Errorf(format, a...)
	res.Tag = tag
	return res
}
