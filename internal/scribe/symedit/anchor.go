package symedit

import (
	"fmt"
	"strings"
)

// Anchor matching for replace_in_body. An anchor is found in three passes and
// the first pass that finds anything wins:
//
//  1. exact substring of the symbol;
//  2. whole lines, equal after leading whitespace is dropped (so an anchor
//     copied at a different indentation still matches);
//  3. whole lines, equal after every run of whitespace is collapsed, which
//     also ignores CR and trailing whitespace.
//
// Passes 2 and 3 match whole lines and replace whole lines. The replacement
// is re-indented to the indentation of the matched lines.

// span is a byte range of the symbol region, and the pass that found it.
type span struct {
	start, end int
	pass       int
}

// findAnchor returns every match of anchor in region, from the first pass
// that matches at all. The caller decides what more than one match means.
func findAnchor(region, anchor string) []span {
	var out []span
	for _, p := range allIndexes(region, anchor) {
		out = append(out, span{p, p + len(anchor), 1})
	}
	if len(out) > 0 {
		return out
	}
	want := strings.Split(strings.Trim(strings.ReplaceAll(anchor, "\r\n", "\n"), "\n"), "\n")
	if len(want) == 0 || (len(want) == 1 && isBlank(want[0])) {
		return nil
	}
	lines, starts := regionLines(region)
	for _, pass := range []int{2, 3} {
		key := dropLeadingSpace
		if pass == 3 {
			key = collapseSpace
		}
		wantKeys := make([]string, len(want))
		for i, l := range want {
			wantKeys[i] = key(l)
		}
		for i := 0; i+len(want) <= len(lines); i++ {
			ok := true
			for j := range want {
				if key(lines[i+j]) != wantKeys[j] {
					ok = false
					break
				}
			}
			if ok {
				last := i + len(want) - 1
				out = append(out, span{starts[i], starts[last] + len(lines[last]), pass})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// regionLines splits region into lines and gives each line's start offset.
func regionLines(region string) ([]string, []int) {
	lines := strings.Split(region, "\n")
	starts := make([]int, len(lines))
	off := 0
	for i, l := range lines {
		starts[i] = off
		off += len(l) + 1
	}
	return lines, starts
}

func dropLeadingSpace(s string) string {
	return strings.TrimLeft(strings.TrimRight(s, "\r"), " \t")
}

func collapseSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// replaceInBodyAnchored applies replace_in_body. With end empty, old is one
// anchor and the match is replaced. With end set, the span from the start of
// old through the end of end is replaced, and both anchors must be unique.
func replaceInBodyAnchored(idx lineIndex, s symbol, old, end, nw string) (string, string, error) {
	region := idx.src[s.start:s.end]
	hits := findAnchor(region, old)
	if err := anchorCount(idx, s, "old_string", hits, old); err != nil {
		return "", "", err
	}
	first := hits[0]
	if end == "" {
		repl := nw
		if first.pass > 1 {
			repl = reindent(nw, matchedIndent(region, first))
		}
		p := s.start + first.start
		q := s.start + first.end
		return idx.src[:p] + repl + idx.src[q:],
			fmt.Sprintf("replaced 1 match at line %d in %s", idx.lineOf(p), s.qual()), nil
	}

	endHits := findAnchor(region, end)
	if err := anchorCount(idx, s, "end_string", endHits, end); err != nil {
		return "", "", err
	}
	last := endHits[0]
	if last.start < first.end {
		return "", "", fmt.Errorf("end_string must come after old_string inside %s, but it starts at line %d and old_string ends at line %d",
			s.qual(), idx.lineOf(s.start+last.start), idx.lineOf(s.start+first.end))
	}
	repl := nw
	if first.pass > 1 {
		repl = reindent(nw, matchedIndent(region, first))
	}
	p, q := s.start+first.start, s.start+last.end
	return idx.src[:p] + repl + idx.src[q:],
		fmt.Sprintf("replaced lines %d-%d in %s", idx.lineOf(p), idx.lineOf(q), s.qual()), nil
}

// anchorCount refuses an anchor with no match or with more than one.
func anchorCount(idx lineIndex, s symbol, label string, hits []span, text string) error {
	hint := ""
	if strings.Count(strings.Trim(text, "\n"), "\n") >= 2 {
		hint = " Long anchors are easy to get wrong: use a short start and end anchor (old_string with end_string) to replace a block."
	}
	switch {
	case len(hits) == 0:
		return fmt.Errorf("%s not found in %s (lines %d-%d). Copy it exactly from a fresh read. Closest lines:%s%s",
			label, s.qual(), s.startLine, s.endLine, closestLines(idx, s, text), hint)
	case len(hits) > 1:
		var lines []string
		for _, h := range hits {
			lines = append(lines, fmt.Sprint(idx.lineOf(s.start+h.start)))
		}
		return fmt.Errorf("%s matches %d places inside %s (lines %s). Add surrounding lines so it matches once, or use an anchor pair (old_string with end_string).%s",
			label, len(hits), s.qual(), strings.Join(lines, ", "), hint)
	}
	return nil
}

// matchedIndent is the indentation of the first line a fuzzy match covers.
func matchedIndent(region string, h span) string {
	line := region[h.start:]
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	return lineIndent(line)
}
