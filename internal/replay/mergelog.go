package replay

import (
	"fmt"
	"regexp"
	"strings"
)

// MergeLogFormat is the `git log --pretty=format:` string
// internal/api/replay.go's fallback importer asks git for when `gh` is
// unavailable on the target (no login, not installed, or the repo has no
// GitHub remote). \x1f separates a commit's own fields, \x1e separates
// commits, so a body containing blank lines or literal tabs never confuses
// the parser the way splitting on newlines would.
const MergeLogFormat = "%H\x1f%s\x1f%b\x1e"

var mergePRNumRe = regexp.MustCompile(`Merge pull request #(\d+)`)

// ParseMergeLog parses `git log --merges --first-parent
// --pretty=format:MergeLogFormat`'s stdout into PRs. This is the fallback
// path's equivalent of ParseGHPRList: no PR number, base branch name, file
// list or linked issues are available from git history alone, so the
// caller (internal/api/replay.go) fills Files in separately via
// `git diff --numstat` against each commit's resolved parent, and
// PreFilter/BuildCase work identically either way once that's done.
//
// GitHub's default merge-commit message is a subject line ("Merge pull
// request #N from owner/branch") followed by a blank line and the PR's own
// title as the first line of the body — this function recovers the PR
// number from the subject and promotes that title line back into Title, so
// a replayed case built via this fallback reads the same as one built from
// gh's JSON. A squash or rebase merge has no such subject and is used
// as-is: its own commit subject becomes the Title, its full body the Body.
func ParseMergeLog(raw string) []PR {
	var out []PR
	for _, rec := range strings.Split(raw, "\x1e") {
		rec = strings.Trim(rec, "\n")
		if strings.TrimSpace(rec) == "" {
			continue
		}
		parts := strings.SplitN(rec, "\x1f", 3)
		if len(parts) < 2 {
			continue
		}
		hash := strings.TrimSpace(parts[0])
		subject := parts[1]
		body := ""
		if len(parts) > 2 {
			body = parts[2]
		}
		if hash == "" {
			continue
		}
		out = append(out, parseMergeLogEntry(hash, subject, body))
	}
	return out
}

func parseMergeLogEntry(hash, subject, body string) PR {
	number := 0
	if m := mergePRNumRe.FindStringSubmatch(subject); m != nil {
		fmt.Sscanf(m[1], "%d", &number)
	}
	title, prBody := subject, strings.TrimSpace(body)
	if strings.HasPrefix(subject, "Merge pull request") {
		lines := strings.SplitN(prBody, "\n", 2)
		if strings.TrimSpace(lines[0]) != "" {
			title = strings.TrimSpace(lines[0])
			if len(lines) > 1 {
				prBody = strings.TrimSpace(lines[1])
			} else {
				prBody = ""
			}
		}
	}
	return PR{Number: number, Title: title, Body: prBody, MergeSHA: hash}
}
