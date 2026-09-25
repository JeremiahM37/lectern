package replay

import (
	"regexp"
	"strings"
)

var fencedCodeBlockRe = regexp.MustCompile("(?s)```.*?```")

// diffHunkHeaderRe matches the start of a pasted unified diff embedded in
// prose (someone pasted `git diff`/`git show` output into the PR
// description instead of using a code fence) so StripSolutionDetails can
// drop it even when the author forgot to fence it.
var diffHunkHeaderRe = regexp.MustCompile(`(?m)^(diff --git |index [0-9a-f]|@@ |--- |\+\+\+ )`)

// StripSolutionDetails removes anything from PR/issue text that reads as the
// SOLUTION (pasted code, a pasted diff) rather than the INTENT (what should
// change and why), so a replay case's prompt asks the same question a human
// would have asked before the fix existed — it never hands the agent a
// worked answer.
//
// This is deliberately the FIRST of two independent leakage passes. It
// cannot be the only one: a fenced-code scan misses code pasted without a
// fence, and there is no way to prove a regex heuristic catches every way a
// human might paste a diff into a text box. The second, independent pass —
// RedactLeakedLines, run after this one by BuildCase — compares the
// (already-stripped) text against the PR's OWN reference diff line by line,
// so even a leak this function's heuristics miss cannot reach the prompt
// verbatim. See docs/replay-evals.md's leakage section.
func StripSolutionDetails(text string) string {
	text = fencedCodeBlockRe.ReplaceAllString(text, "[code removed]")
	lines := strings.Split(text, "\n")
	out := make([]string, 0, len(lines))
	inDiff := false
	for _, line := range lines {
		if diffHunkHeaderRe.MatchString(line) {
			inDiff = true
			continue
		}
		if inDiff {
			trimmed := strings.TrimSpace(line)
			looksLikeDiffBody := trimmed == "" ||
				strings.HasPrefix(line, "+") || strings.HasPrefix(line, "-") || strings.HasPrefix(line, " ")
			if !looksLikeDiffBody {
				inDiff = false
			} else {
				continue
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// BuildPrompt assembles a replay case's prompt from a PR's own stated intent
// — title, stripped body, and any linked issue's stripped title+body — and
// NOTHING else. It never reads pr.Files' content and never sees a diff; the
// only inputs are text GitHub already associates with "what was this PR
// trying to do", which is the whole leakage story for this function — there
// is no diff in scope here to leak. The second-stage guard (RedactLeakedLines)
// exists for the body/issue TEXT, which can itself contain pasted code.
func BuildPrompt(pr PR) string {
	var b strings.Builder
	b.WriteString(strings.TrimSpace(pr.Title))
	if body := strings.TrimSpace(StripSolutionDetails(pr.Body)); body != "" {
		b.WriteString("\n\n")
		b.WriteString(body)
	}
	for _, iss := range pr.Issues {
		text := strings.TrimSpace(StripSolutionDetails(iss.Title + "\n" + iss.Body))
		if text == "" {
			continue
		}
		b.WriteString("\n\n---\nLinked issue: ")
		b.WriteString(text)
	}
	return strings.TrimSpace(b.String())
}

// diffChangedLines pulls the CONTENT of every added/removed line out of a
// unified diff (the +/- payload, not the +++/--- file headers or @@ hunk
// markers), trimmed, deduplicated into a set. Lines shorter than the
// threshold are dropped — a bare "}" or "return" appears constantly in
// ordinary prose and code alike, so treating it as evidence of a leak would
// redact harmless text; anything long enough to be distinctive is kept.
func diffChangedLines(diff string) map[string]bool {
	const minLeakLen = 8
	out := map[string]bool{}
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		var content string
		switch {
		case strings.HasPrefix(line, "+"), strings.HasPrefix(line, "-"):
			content = strings.TrimSpace(line[1:])
		default:
			continue
		}
		if len(content) >= minLeakLen {
			out[content] = true
		}
	}
	return out
}

// RedactLeakedLines is the leakage guard's second, independent pass: any
// line of the (already-stripped) prompt that exactly matches a line the
// reference diff added or removed is replaced, so even solution text that
// slipped past StripSolutionDetails' heuristics — pasted inline, without a
// fence, not diff-shaped — cannot reach the agent verbatim. Safe to call
// with an empty referenceDiff (nothing to guard against yet, e.g. during the
// cheap filter pass before a diff has been fetched).
func RedactLeakedLines(text, referenceDiff string) string {
	if referenceDiff == "" {
		return text
	}
	leaked := diffChangedLines(referenceDiff)
	if len(leaked) == 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	changed := false
	for i, line := range lines {
		if leaked[strings.TrimSpace(line)] {
			lines[i] = "[redacted: matched the reference change]"
			changed = true
		}
	}
	if !changed {
		return text
	}
	return strings.Join(lines, "\n")
}
