// Package worktree owns the git worktree lifecycle and diff capture, all through
// the Executor interface so it works identically on every target kind.
package worktree

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/internal/executor"
)

// FileStat is one file's line churn in a captured diff.
type FileStat struct {
	Path      string `json:"path"`
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// FilePatch is one file's slice of a unified diff, for the mobile viewer.
type FilePatch struct {
	Path  string `json:"path"`
	Patch string `json:"patch"`
}

// BranchName is the branch one attempt works on.
func BranchName(taskID int64, attemptN int) string {
	return fmt.Sprintf("lec/task%d-a%d", taskID, attemptN)
}

// Path is where one attempt's worktree lives under a workroot.
func Path(workroot string, taskID int64, attemptN int) string {
	return fmt.Sprintf("%s/task%d-a%d", strings.TrimRight(workroot, "/"), taskID, attemptN)
}

// NamespacedWorkroot keeps automatically-created allocations from separate
// control-plane instances out of one another's default directory.
func NamespacedWorkroot(workroot, namespace string) string {
	namespace = namespaceSlug(namespace)
	if namespace == "" {
		return workroot
	}
	return filepath.Join(strings.TrimRight(workroot, "/"), namespace)
}

// NamespacedBranch scopes an automatically-created branch to one runtime.
func NamespacedBranch(branch, namespace string) string {
	namespace = namespaceSlug(namespace)
	if namespace == "" || branch == "" {
		return branch
	}
	return "lec/" + namespace + "/" + strings.TrimPrefix(branch, "lec/")
}

func namespaceSlug(value string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(value) {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			b.WriteRune(r)
		} else if b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
			b.WriteByte('-')
		}
		if b.Len() >= 80 {
			break
		}
	}
	return strings.Trim(b.String(), "-.")
}

// DefaultWorkroot puts worktrees beside the repo, not inside it.
func DefaultWorkroot(repoPath string) string {
	trimmed := strings.TrimRight(repoPath, "/")
	parent := trimmed
	if i := strings.LastIndex(trimmed, "/"); i >= 0 {
		parent = trimmed[:i]
	}
	return parent + "/.lectern-worktrees"
}

// Ensure creates the attempt's worktree, tolerating one that already exists —
// follow-up attempts and the reviewer gate deliberately reuse it.
func Ensure(ctx context.Context, ex executor.Executor, repo, baseBranch, branch, path string) error {
	q := executor.ShellQuote
	r, err := ex.Run(ctx, fmt.Sprintf("git -C %s worktree add -b %s %s %s",
		q(repo), q(branch), q(path), q(baseBranch)), executor.RunOpts{Timeout: 120})
	if err != nil {
		return err
	}
	if !r.OK() {
		out := r.Stderr
		if strings.Contains(out, "already exists") || strings.Contains(out, "already checked out") {
			return nil
		}
		msg := strings.TrimSpace(r.Stderr)
		if msg == "" {
			msg = strings.TrimSpace(r.Stdout)
		}
		return executor.Errf("worktree add failed: %s", msg)
	}
	return AddExcludes(ctx, ex, path)
}

// AddExcludes keeps our runtime dir and verify-run artifacts out of git
// status/diff/commit. Verify artifacts leaking into `git add -A` was a real bug
// found only on a live dispatch.
func AddExcludes(ctx context.Context, ex executor.Executor, repoDir string) error {
	q := executor.ShellQuote
	// ".agentdeck/" is what a project launched before the rename still has on
	// disk; without the pattern it would surface as untracked in every one.
	for _, pattern := range []string{".lectern/", ".agentdeck/", "__pycache__/", "*.pyc"} {
		cmd := fmt.Sprintf(
			`ex_file=$(git -C %s rev-parse --git-common-dir)/info/exclude; `+
				`grep -qx %s "$ex_file" 2>/dev/null || echo %s >> "$ex_file"`,
			q(repoDir), q(pattern), q(pattern))
		if _, err := ex.Run(ctx, cmd, executor.RunOpts{Timeout: 30}); err != nil {
			return err
		}
	}
	return nil
}

var numstatRe = regexp.MustCompile(`^(\d+|-)\t(\d+|-)\t(.+)$`)

// CaptureDiff returns the full patch plus per-file stats of everything the
// attempt changed against its base branch.
func CaptureDiff(ctx context.Context, ex executor.Executor, wt, baseBranch string) (string, []FileStat, error) {
	q := executor.ShellQuote
	// -N stages intents-to-add so brand new files show up in the diff
	if _, err := ex.Run(ctx, fmt.Sprintf("git -C %s add -A -N", q(wt)),
		executor.RunOpts{Timeout: 60}); err != nil {
		return "", nil, err
	}
	pr, err := ex.Run(ctx, fmt.Sprintf("git -C %s diff --no-color %s", q(wt), q(baseBranch)),
		executor.RunOpts{Timeout: 120})
	if err != nil {
		return "", nil, err
	}
	nr, err := ex.Run(ctx, fmt.Sprintf("git -C %s diff --numstat %s", q(wt), q(baseBranch)),
		executor.RunOpts{Timeout: 60})
	if err != nil {
		return "", nil, err
	}
	files := []FileStat{}
	for _, line := range strings.Split(nr.Stdout, "\n") {
		m := numstatRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		add, _ := strconv.Atoi(m[1]) // "-" (binary) parses to 0, which is right
		del, _ := strconv.Atoi(m[2])
		files = append(files, FileStat{Path: m[3], Additions: add, Deletions: del})
	}
	return pr.Stdout, files, nil
}

var diffHeaderRe = regexp.MustCompile(` b/(.+?)\s*$`)

// SplitPatch slices a unified diff into per-file chunks.
func SplitPatch(patch string) []FilePatch {
	out := []FilePatch{}
	var cur *FilePatch
	for _, line := range splitKeepEnds(patch) {
		if strings.HasPrefix(line, "diff --git ") {
			if cur != nil {
				out = append(out, *cur)
			}
			path := "?"
			if m := diffHeaderRe.FindStringSubmatch(line); m != nil {
				path = m[1]
			}
			cur = &FilePatch{Path: path, Patch: line}
		} else if cur != nil {
			cur.Patch += line
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// Remove reclaims a worktree.
func Remove(ctx context.Context, ex executor.Executor, repo, path string) error {
	q := executor.ShellQuote
	_, err := ex.Run(ctx, fmt.Sprintf("git -C %s worktree remove --force %s", q(repo), q(path)),
		executor.RunOpts{Timeout: 60})
	return err
}

func splitKeepEnds(s string) []string {
	var out []string
	for len(s) > 0 {
		i := strings.IndexByte(s, '\n')
		if i < 0 {
			out = append(out, s)
			break
		}
		out = append(out, s[:i+1])
		s = s[i+1:]
	}
	return out
}
