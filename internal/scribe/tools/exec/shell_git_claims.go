package exec

import (
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// shellGitSubs are the git subcommands that change files or commit them. A
// shell `git` call with one of these reaches files another tab may hold a
// claim on. Read-only subcommands are not listed, so they never warn.
var shellGitSubs = map[string]bool{
	"add": true, "rm": true, "mv": true, "commit": true, "stash": true,
	"reset": true, "restore": true, "checkout": true, "switch": true,
	"clean": true, "cherry-pick": true, "revert": true, "rebase": true,
	"merge": true, "pull": true, "am": true, "apply": true,
}

// gitCall is one git invocation found in a shell command line. dir is the
// directory it runs in, after any `cd` before it and any `git -C`.
type gitCall struct {
	dir  string
	sub  string
	args []string
}

// gitReach is what a git call may touch. paths are absolute and set only when
// all is false. all means the whole working tree is in reach (commit, stash, a
// branch switch, add -A), so the caller adds the files the tree has changed.
type gitReach struct {
	paths []string
	all   bool
}

// shellSegments splits a command line into simple commands, each a list of
// words. Separators are ; & | newline, parentheses, backticks and redirections.
// Quotes group words and are removed. This reads a command for warnings; it is
// not a shell, so variables and $(...) are not expanded.
func shellSegments(command string) [][]string {
	var segs [][]string
	var words []string
	var cur strings.Builder
	inWord := false
	var quote rune
	flush := func() {
		if inWord {
			words = append(words, cur.String())
			cur.Reset()
			inWord = false
		}
	}
	endSeg := func() {
		flush()
		if len(words) > 0 {
			segs = append(segs, words)
		}
		words = nil
	}
	for _, r := range command {
		switch {
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
			inWord = true
		case r == ' ' || r == '\t':
			flush()
		case strings.ContainsRune(";&|\n()`<>", r):
			endSeg()
		default:
			cur.WriteRune(r)
			inWord = true
		}
	}
	endSeg()
	return segs
}

// shellGitCalls returns the git invocations in command, in order, tracking the
// directory a `cd` moves to. A call behind a pipe runs in a subshell, so its
// cd is not carried forward; that is accepted, since a warning only needs to
// be right for the common forms.
func shellGitCalls(command, dir string) []gitCall {
	var calls []gitCall
	for _, words := range shellSegments(command) {
		words = skipCommandPrefix(words)
		if len(words) == 0 {
			continue
		}
		switch filepath.Base(words[0]) {
		case "cd":
			if len(words) >= 2 && words[1] != "-" {
				dir = resolveUnder(dir, words[1])
			}
		case "git":
			call, ok := parseGitCall(words, dir)
			if ok {
				dir = call.dir
				calls = append(calls, call)
			}
		}
	}
	return calls
}

// skipCommandPrefix drops leading VAR=value assignments and wrappers that run
// the command they precede, so `env X=1 git commit` is read as a git commit.
func skipCommandPrefix(words []string) []string {
	for len(words) > 0 {
		w := words[0]
		if strings.Contains(w, "=") && !strings.HasPrefix(w, "-") {
			words = words[1:]
			continue
		}
		switch filepath.Base(w) {
		case "env", "command", "exec", "time", "nohup":
			words = words[1:]
			continue
		}
		break
	}
	return words
}

// parseGitCall reads `git [global options] <sub> [args]`. Global options that
// move the working directory (-C) change dir; the others are skipped with
// their value.
func parseGitCall(words []string, dir string) (gitCall, bool) {
	i := 1
	for i < len(words) {
		w := words[i]
		switch {
		case w == "-C" && i+1 < len(words):
			dir = resolveUnder(dir, words[i+1])
			i += 2
		case w == "-c" || w == "--git-dir" || w == "--work-tree" || w == "--namespace":
			i += 2
		case strings.HasPrefix(w, "-"):
			i++
		default:
			return gitCall{dir: dir, sub: w, args: words[i+1:]}, true
		}
	}
	return gitCall{}, false
}

// resolveUnder returns p as an absolute, clean path. A relative p is taken
// from base; a leading ~ is the home directory.
func resolveUnder(base, p string) string {
	p = expandHome(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(base, p)
	}
	return filepath.Clean(p)
}

// splitGitArgs separates flags from positional words. Words after "--" are
// always positional, and afterDD reports whether that marker was present.
func splitGitArgs(args []string) (flags, pos []string, afterDD bool) {
	for _, a := range args {
		switch {
		case afterDD:
			pos = append(pos, a)
		case a == "--":
			afterDD = true
		case strings.HasPrefix(a, "-") && a != "-":
			flags = append(flags, a)
		default:
			pos = append(pos, a)
		}
	}
	return flags, pos, afterDD
}

// hasFlag reports whether flags holds the long option or a short option
// letter, alone or inside a combined group such as -am or -fdn.
func hasFlag(flags []string, long string, short byte) bool {
	for _, f := range flags {
		if f == long {
			return true
		}
		if len(f) > 2 && f[0] == '-' && f[1] != '-' && strings.IndexByte(f, short) > 0 {
			return true
		}
	}
	return false
}

// reach works out what this git call may change. The second result is false
// when the call changes nothing, such as stash list or clean -n.
func (c gitCall) reach() (gitReach, bool) {
	if !shellGitSubs[c.sub] {
		return gitReach{}, false
	}
	flags, pos, afterDD := splitGitArgs(c.args)
	var r gitReach
	switch c.sub {
	case "stash":
		if len(pos) > 0 && (pos[0] == "list" || pos[0] == "show") {
			return r, false
		}
		r.all = true
	case "clean":
		if hasFlag(flags, "--dry-run", 'n') {
			return r, false
		}
		r.all = true
	case "apply", "am":
		for _, f := range []string{"--check", "--stat", "--summary", "--numstat"} {
			if hasFlag(flags, f, 0) {
				return r, false
			}
		}
		r.all = true
	case "add":
		r.all = len(pos) == 0 || hasFlag(flags, "--all", 'A') || hasFlag(flags, "--update", 'u') || anyWord(pos, ".", ":/", ":")
	case "restore":
		r.all = len(pos) == 0
	case "reset", "checkout":
		// With "--" the words after it are paths. Without it, reset may move
		// HEAD and checkout may switch branches, so the whole tree is in reach.
		r.all = !afterDD
	case "rm", "mv":
		// Named paths only.
	default:
		// commit, switch, merge, pull, rebase, cherry-pick, revert.
		r.all = true
	}
	// Whole-tree calls need no named paths: the changed files already cover
	// anything they touch. Their positional words are often a message or a
	// revision, so they are left out rather than read as paths.
	if !r.all {
		for _, p := range pos {
			r.paths = append(r.paths, resolveUnder(c.dir, p))
		}
	}
	return r, true
}

func anyWord(words []string, want ...string) bool {
	for _, w := range words {
		for _, x := range want {
			if w == x {
				return true
			}
		}
	}
	return false
}

// shellClaimWarning names the files a shell git command may change that other
// tabs hold claims on. It mirrors the git tool: the same warning text, advisory
// only, never a refusal. It returns "" when no other tab holds a claim, so a
// single-tab session pays nothing, and when no git call in command changes
// files.
func shellClaimWarning(env *tools.Env, dir, command string) string {
	if env == nil || env.Claims == nil {
		return ""
	}
	others := env.Claims.OtherClaims()
	if len(others) == 0 {
		return ""
	}
	var reach []string
	allTree := false
	for _, c := range shellGitCalls(command, dir) {
		r, ok := c.reach()
		if !ok {
			continue
		}
		reach = append(reach, r.paths...)
		allTree = allTree || r.all
	}
	if len(reach) == 0 && !allTree {
		return ""
	}
	if allTree {
		if changed, err := ChangedFiles(env.Workdir); err == nil {
			for _, f := range changed {
				reach = append(reach, filepath.Join(env.Workdir, f))
			}
		}
	}
	return claimWarningFor(env.Workdir, others, reach)
}

// prefixWarning puts a warning ahead of a tool result, as the git tool does,
// so the model reads it before anything else. An empty warning leaves out.
func prefixWarning(warn, out string) string {
	if warn == "" {
		return out
	}
	return warn + "\n" + out
}
