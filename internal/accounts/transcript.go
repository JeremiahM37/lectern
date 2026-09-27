package accounts

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/shellq"
)

// A conversation lives inside the config directory of the login that ran it,
// so moving work to another account means moving the conversation too. The
// transcript is copied (not linked) into the same relative place in the new
// account's directory, which is where that CLI's resume-by-id looks:
//
//	claude  projects/<cwd with every non-alphanumeric as '-'>/<id>.jsonl
//	        (plus the <id>/ directory beside it: subagent transcripts and
//	        large tool results)
//	codex   sessions/YYYY/MM/DD/rollout-<timestamp>-<id>.jsonl
//
// Both CLIs find a conversation by id anywhere in that tree, whatever the
// current directory (Claude Code 2.1.283, Codex 0.157.0; Codex rebuilds its
// sqlite thread cache from the rollout on first resume). Gemini is not moved:
// its chats sit under a per-home project registry (.gemini/projects.json) and
// Lectern does not capture a Gemini conversation id, so a Gemini swap starts
// the next account from the task or the screen instead.
//
// A copy, rather than a symlink, because only one account runs the
// conversation at a time and a CLI may replace its file instead of appending;
// the newest copy is always the one that moves next.

// ClaudeProjectDir is the directory name Claude Code files a working
// directory's conversations under.
func ClaudeProjectDir(workdir string) string {
	return nonAlnum.ReplaceAllString(workdir, "-")
}

var nonAlnum = regexp.MustCompile(`[^a-zA-Z0-9]`)

var conversationID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,127}$`)

// StageCommand copies conversation id from one account directory to another
// and prints the transcript's path relative to the directory. It exits 3 when
// the conversation is not in the source. from and to are account directories
// ("" is the CLI's default); workdir is where the conversation ran.
func StageCommand(agent, from, to, id, workdir string) (string, error) {
	if !conversationID.MatchString(id) {
		return "", fmt.Errorf("not a conversation id: %q", id)
	}
	var find string
	switch agent {
	case "claude":
		// Prefer the working directory's own folder; fall back to any folder
		// holding the id (a conversation started in a parent directory).
		find = `src="$from/projects/"` + shellq.Quote(ClaudeProjectDir(workdir)+"/"+id+".jsonl") + `
[ -f "$src" ] || src=$(find "$from/projects" -mindepth 2 -maxdepth 2 -type f -name ` + shellq.Quote(id+".jsonl") + ` 2>/dev/null | head -n 1)`
	case "codex":
		find = `src=$(find "$from/sessions" -type f -name ` + shellq.Quote("rollout-*-"+id+".jsonl") + ` 2>/dev/null | sort | tail -n 1)`
	default:
		return "", fmt.Errorf("%s conversations cannot be moved between accounts", agent)
	}
	script := `set -u
umask 077
from=` + DirExpr(agent, from) + `
to=` + DirExpr(agent, to) + `
` + find + `
[ -n "$src" ] && [ -f "$src" ] || { echo "conversation not found in the current account" >&2; exit 3; }
rel=${src#"$from"/}
dst="$to/$rel"
if [ "$(cd "$(dirname "$src")" && pwd -P)/$(basename "$src")" = "$(mkdir -p "$(dirname "$dst")" && cd "$(dirname "$dst")" && pwd -P)/$(basename "$dst")" ]; then
  echo "$rel"; exit 0
fi
mkdir -p "$(dirname "$dst")" || exit 4
cp -p "$src" "$dst.lectern-tmp" && mv -f "$dst.lectern-tmp" "$dst" || exit 4
`
	if agent == "claude" {
		script += `side=${src%.jsonl}
if [ -d "$side" ]; then mkdir -p "${dst%.jsonl}" && cp -Rp "$side/." "${dst%.jsonl}/" || exit 4; fi
`
	}
	script += `echo "$rel"
`
	return "bash -c " + shellq.Quote(script), nil
}

// FirstLine returns the first non-empty line of s, trimmed.
func FirstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}
