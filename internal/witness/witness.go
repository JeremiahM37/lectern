// Package witness records the state of a git worktree that a human was
// looking at when they were asked to approve something, so the durable effect
// (a push, a merge, a deploy, a commit) can be re-checked against that state
// before it runs.
//
// An approval is granted against what the person saw. The effect runs later,
// and in between another agent can edit the same worktree or the agent can
// commit more. Comparing a fresh Snapshot with the stored Witness catches
// that drift. This is a point-in-time check, not a lock: it narrows the window
// between approval and effect but cannot close it.
package witness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path"
	"regexp"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// Witness is the git state of one worktree at one moment.
type Witness struct {
	Branch   string `json:"branch"`
	Head     string `json:"head"`
	Upstream string `json:"upstream"`
	DiffHash string `json:"diff_hash"`
}

// Snapshot reads the worktree at dir. It fails when dir is not a git repository
// with a commit, so a caller that cannot snapshot simply has no witness.
func Snapshot(ctx context.Context, ex executor.Executor, dir string) (Witness, error) {
	q := executor.ShellQuote
	git := "git -C " + q(dir) + " "
	head, err := ex.Run(ctx, git+"rev-parse HEAD", executor.RunOpts{Timeout: 15})
	if err != nil {
		return Witness{}, err
	}
	if !head.OK() {
		return Witness{}, fmt.Errorf("rev-parse HEAD: %s", strings.TrimSpace(head.Stderr+head.Stdout))
	}
	w := Witness{Head: strings.TrimSpace(head.Stdout)}

	// A detached HEAD has no branch name; "HEAD" is what git prints then, and
	// it is stable, so it still compares correctly.
	if br, err := ex.Run(ctx, git+"rev-parse --abbrev-ref HEAD", executor.RunOpts{Timeout: 15}); err == nil && br.OK() {
		w.Branch = strings.TrimSpace(br.Stdout)
	}
	// No upstream is normal (a local branch), so a failure here is an empty value.
	if up, err := ex.Run(ctx, git+"rev-parse @{u}", executor.RunOpts{Timeout: 15}); err == nil && up.OK() {
		w.Upstream = strings.TrimSpace(up.Stdout)
	}

	diff, err := ex.Run(ctx, git+"diff HEAD --binary", executor.RunOpts{Timeout: 60})
	if err != nil {
		return Witness{}, err
	}
	if !diff.OK() {
		return Witness{}, fmt.Errorf("git diff HEAD: %s", strings.TrimSpace(diff.Stderr))
	}
	untracked, err := ex.Run(ctx, git+"ls-files --others --exclude-standard -z", executor.RunOpts{Timeout: 30})
	if err != nil {
		return Witness{}, err
	}
	if !untracked.OK() {
		return Witness{}, fmt.Errorf("git ls-files: %s", strings.TrimSpace(untracked.Stderr))
	}

	// The diff does not contain untracked files, so they are hashed here. Their
	// names alone would miss an edit to a new file, which is exactly the kind of
	// change another agent makes.
	h := sha256.New()
	h.Write([]byte(diff.Stdout))
	h.Write([]byte{0})
	for _, name := range strings.Split(untracked.Stdout, "\x00") {
		if name == "" {
			continue
		}
		data, err := ex.ReadFile(ctx, path.Join(dir, name), 0)
		if err != nil {
			return Witness{}, err
		}
		h.Write([]byte(name))
		h.Write([]byte{0})
		h.Write(data)
		h.Write([]byte{0})
	}
	w.DiffHash = hex.EncodeToString(h.Sum(nil))
	return w, nil
}

// Drift describes how the current state b differs from the state a the person
// approved. It is "" when nothing changed. The text is one line because it is
// shown to the agent and in the review UI.
func (a Witness) Drift(b Witness) string {
	switch {
	case a.Branch != b.Branch:
		return fmt.Sprintf("branch changed %s → %s", a.Branch, b.Branch)
	case a.Head != b.Head:
		return fmt.Sprintf("HEAD moved %s → %s", short(a.Head), short(b.Head))
	case a.Upstream != b.Upstream:
		return fmt.Sprintf("upstream moved %s → %s", short(a.Upstream), short(b.Upstream))
	case a.DiffHash != b.DiffHash:
		return "working tree changed"
	}
	return ""
}

func short(sha string) string {
	if sha == "" {
		return "(none)"
	}
	if len(sha) > 7 {
		return sha[:7]
	}
	return sha
}

// effectfulPatterns are the leading words of a command that publish, mutate or
// deploy something outside the worktree. Each entry is the program, then the
// accepted subcommands. An empty subcommand list means every subcommand counts.
var effectfulPatterns = []struct {
	program string
	subs    []string
}{
	{"git", []string{"push", "merge", "rebase", "commit"}},
	{"gh", nil}, // only "pr merge", "pr create" and "release" below count
	{"npm", []string{"publish"}},
	{"docker", []string{"push"}},
	{"systemctl", []string{"start", "stop", "restart", "reload"}},
	{"kubectl", []string{"apply", "delete"}},
	{"terraform", []string{"apply"}},
}

// ghEffectful lists the gh subcommand pairs that publish. gh pr view, gh run
// watch and the like are read-only and must not trip the gate.
var ghEffectful = map[string]bool{"pr merge": true, "pr create": true, "release": true}

// segmentSep splits a shell line into simple commands. Pipes are included, so
// "cat x | git push" is still caught.
var segmentSep = regexp.MustCompile(`&&|\|\||[;|\n]`)

// IsEffectful reports whether a tool call is one that publishes or mutates
// outside the worktree and so needs its approval re-checked before it runs.
//
// Only shell tools are inspected (Bash, bash, shell). Each simple command in a
// chain is checked, after stripping a leading "cd <dir>" and a leading "sudo".
// The word "deploy" counts anywhere in a segment, such as in a script name.
// Everything else is matched on the leading words only, so echo "git push" is
// not effectful, but a quoted command that starts with the program name is
// (sh -c "git push" is missed on purpose: nesting is out of scope here).
func IsEffectful(toolName string, input map[string]any) bool {
	if !strings.EqualFold(toolName, "bash") && !strings.EqualFold(toolName, "shell") {
		return false
	}
	cmd, _ := input["command"].(string)
	if cmd == "" {
		return false
	}
	for _, seg := range segmentSep.Split(cmd, -1) {
		if segmentEffectful(strings.TrimSpace(seg)) {
			return true
		}
	}
	return false
}

func segmentEffectful(seg string) bool {
	if seg == "" {
		return false
	}
	if strings.Contains(strings.ToLower(seg), "deploy") {
		return true
	}
	fields := strings.Fields(seg)
	// Strip "cd <dir>" (the chain split already handled the "&&"), "sudo" and
	// leading VAR=value assignments.
	for len(fields) > 0 {
		switch {
		case fields[0] == "cd" && len(fields) >= 2:
			fields = fields[2:]
		case fields[0] == "sudo" || fields[0] == "env":
			fields = fields[1:]
		case strings.Contains(fields[0], "=") && !strings.HasPrefix(fields[0], "-"):
			fields = fields[1:]
		default:
			goto done
		}
	}
done:
	if len(fields) == 0 {
		return false
	}
	program := path.Base(fields[0])
	args := fields[1:]
	for _, p := range effectfulPatterns {
		if program != p.program {
			continue
		}
		sub := firstSubcommand(program, args)
		if program == "gh" {
			return ghEffectful[sub] || ghEffectful[pairOf(args)]
		}
		if p.subs == nil {
			return false
		}
		for _, s := range p.subs {
			if sub == s {
				return true
			}
		}
		return false
	}
	return false
}

// firstSubcommand returns the first non-option word after the program. git
// takes options that take a value (-C dir, -c key=value), which are skipped
// with their value; gh's group word ("pr") is combined with the next one by
// pairOf, so for gh this returns the group.
func firstSubcommand(program string, args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			if program == "git" && (a == "-C" || a == "-c") {
				i++
			}
			continue
		}
		return a
	}
	return ""
}

// pairOf returns "group sub" for gh's two-word subcommands ("pr merge").
func pairOf(args []string) string {
	first := firstSubcommand("gh", args)
	if first == "" {
		return ""
	}
	seen := false
	for _, a := range args {
		if a == first {
			seen = true
			continue
		}
		if seen && !strings.HasPrefix(a, "-") {
			return first + " " + a
		}
	}
	return first
}
