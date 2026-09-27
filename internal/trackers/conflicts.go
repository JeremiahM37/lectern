package trackers

import (
	"context"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// Conflicts is which files a pull request conflicts on.
type Conflicts struct {
	Files []string `json:"files"`
	// Checked is false when the files could not be worked out (no git 2.38
	// on the target, say); Detail says why.
	Checked bool   `json:"checked"`
	Detail  string `json:"detail,omitempty"`
}

// conflictMarker separates the fetch's output from merge-tree's, so an
// unexpected line from fetch can never be read as a file name.
const conflictMarker = "--lectern-merge-tree--"

// ConflictingFiles fetches the PR's base and head into the project's own
// clone and asks `git merge-tree --write-tree` which files would conflict —
// a merge computed entirely in the object store: no checkout, no index, no
// working tree touched. Nothing is written but fetched objects and
// FETCH_HEAD, which every `git fetch` writes.
func ConflictingFiles(ctx context.Context, ex executor.Executor, repoPath, baseRef, headRef string) Conflicts {
	q := executor.ShellQuote
	script := fmt.Sprintf(`cd %s && git fetch --quiet --no-tags origin %s %s && fh=$(git rev-parse --git-path FETCH_HEAD) && `+
		`b=$(sed -n 1p "$fh" | cut -f1) && h=$(sed -n 2p "$fh" | cut -f1) && echo %s && `+
		`git merge-tree --write-tree --name-only --no-messages "$b" "$h"; echo "rc=$?"`,
		q(repoPath), q(baseRef), q(headRef), conflictMarker)
	res, err := ex.Run(ctx, script, executor.RunOpts{Timeout: 120})
	if err != nil {
		return Conflicts{Files: []string{}, Detail: err.Error()}
	}
	return parseMergeTree(res.Stdout, res.Stderr)
}

func parseMergeTree(stdout, stderr string) Conflicts {
	_, after, ok := strings.Cut(stdout, conflictMarker)
	if !ok {
		return Conflicts{Files: []string{}, Detail: "could not fetch the pull request's branches: " + clip(stdout+stderr, 300)}
	}
	lines := strings.Split(strings.TrimSpace(after), "\n")
	rc := ""
	if n := len(lines); n > 0 && strings.HasPrefix(lines[n-1], "rc=") {
		rc, lines = strings.TrimPrefix(lines[n-1], "rc="), lines[:n-1]
	}
	switch rc {
	case "0":
		return Conflicts{Files: []string{}, Checked: true}
	case "1":
	default:
		detail := "git merge-tree failed"
		if strings.Contains(stderr, "unknown option") || strings.Contains(stderr, "usage:") {
			detail = "the target's git is older than 2.38 and cannot list conflicting files"
		} else if s := strings.TrimSpace(stderr); s != "" {
			detail += ": " + clip(s, 300)
		}
		return Conflicts{Files: []string{}, Detail: detail}
	}
	// First line is the tree merge-tree wrote; the rest are conflicted paths.
	files := []string{}
	seen := map[string]bool{}
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if i == 0 || l == "" || seen[l] {
			continue
		}
		seen[l] = true
		files = append(files, l)
	}
	return Conflicts{Files: files, Checked: true}
}
