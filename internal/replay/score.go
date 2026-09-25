package replay

import "strings"

// DiffInfo is what Score needs out of one unified diff: which files it
// touched (in first-seen order, for stable display) and the trimmed content
// of every added/removed line, as a set.
type DiffInfo struct {
	Files []string
	Lines map[string]bool
}

// ParseDiff extracts file list and changed-line content from a unified diff.
// It reads +++/--- headers for file identity (not "diff --git", which some
// generators omit for renames-only or binary changes) and every remaining
// +/- line's content, trimmed, for line-level comparison.
func ParseDiff(diff string) DiffInfo {
	info := DiffInfo{Lines: map[string]bool{}}
	seen := map[string]bool{}
	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "+++ "), strings.HasPrefix(line, "--- "):
			if p := diffFilePath(line); p != "" && !seen[p] {
				seen[p] = true
				info.Files = append(info.Files, p)
			}
		case strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
			if content := strings.TrimSpace(line[1:]); content != "" {
				info.Lines[content] = true
			}
		}
	}
	return info
}

func diffFilePath(headerLine string) string {
	rest := strings.TrimSpace(headerLine[4:]) // past "+++ " / "--- "
	if rest == "/dev/null" || rest == "" {
		return ""
	}
	for _, prefix := range []string{"a/", "b/"} {
		if strings.HasPrefix(rest, prefix) {
			return rest[len(prefix):]
		}
	}
	return rest
}

// jaccard is |A∩B| / |A∪B|. Two empty sets score 1.0 — nothing to disagree
// on — rather than the 0/0 that would otherwise read as "completely
// different".
func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	inter, union := 0, len(a)
	for k := range b {
		if !a[k] {
			union++
		}
	}
	for k := range a {
		if b[k] {
			inter++
		}
	}
	if union == 0 {
		return 0
	}
	return float64(inter) / float64(union)
}

func toSet(list []string) map[string]bool {
	out := make(map[string]bool, len(list))
	for _, s := range list {
		out[s] = true
	}
	return out
}

// Similarity is one attempt's scored resemblance to its case's reference
// diff — everything stored on eval_results for a replay cell beyond
// pass/fail. See docs/replay-evals.md "Interpreting scores".
type Similarity struct {
	// FileOverlap is the Jaccard index of the two diffs' touched-file sets:
	// did the attempt change the same files the accepted fix changed.
	FileOverlap float64 `json:"file_overlap"`
	// LineOverlap is the Jaccard index of the two diffs' changed-line
	// content sets (added/removed lines, whitespace-trimmed): a coarse
	// "how much of the actual edit matches", not an AST-aware diff.
	LineOverlap float64 `json:"line_overlap"`
	// SizeRatio is attempt-changed-lines / reference-changed-lines: 1.0 is
	// "changed about as much code as the accepted fix did"; well above 1
	// suggests scope creep, well below suggests a partial fix.
	SizeRatio float64 `json:"size_ratio"`
}

// Score compares an attempt's diff against its case's reference diff. Both
// arguments are raw unified diff text (the same *.patch content the diff
// dir already stores per attempt).
func Score(attemptDiff, referenceDiff string) Similarity {
	a, r := ParseDiff(attemptDiff), ParseDiff(referenceDiff)
	sim := Similarity{
		FileOverlap: jaccard(toSet(a.Files), toSet(r.Files)),
		LineOverlap: jaccard(a.Lines, r.Lines),
	}
	switch {
	case len(r.Lines) > 0:
		sim.SizeRatio = float64(len(a.Lines)) / float64(len(r.Lines))
	case len(a.Lines) == 0:
		sim.SizeRatio = 1
	}
	return sim
}
