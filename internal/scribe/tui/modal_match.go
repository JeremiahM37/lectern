package tui

// Matching helpers for the shared picker: the slash split for group/item
// queries, the token test, and the busy gate that async modals implement.

import (
	"strings"
)

// busyModal is a modal waiting on background work. While Busy is true it
// swallows key presses (Ctrl+C still quits, handled before modals); results
// from the work still reach it, because only key messages are held.
type busyModal interface{ Busy() bool }

// tokenMatches reports whether q is a case-insensitive substring of target,
// or a subsequence of it. An empty q matches everything.
func tokenMatches(target, q string) bool {
	q = strings.ToLower(q)
	if q == "" {
		return true
	}
	t := strings.ToLower(target)
	if strings.Contains(t, q) {
		return true
	}
	qr := []rune(q)
	i := 0
	for _, r := range t {
		if i < len(qr) && r == qr[i] {
			i++
		}
	}
	return i == len(qr)
}

// matchItem scores one item against the picker's query. It reports whether
// the item survives. A query with a slash splits at the first one: the left
// side must match the group label, and the right side fuzzy-matches the item
// label (an empty right side keeps every item of a matching group). Without a
// slash an item survives if its label, value, description, group or
// group/label matches.
func matchItem(it PickItem, query string) (int, bool) {
	if i := strings.IndexByte(query, '/'); i >= 0 {
		group, item := query[:i], query[i+1:]
		if !tokenMatches(it.Group, group) {
			return 0, false
		}
		if item == "" {
			return 0, true
		}
		return fuzzyScore(it.Label, item)
	}
	best, ok := -1<<30, false
	fields := []string{it.Label, it.Value, it.Desc, it.Group}
	if it.Group != "" {
		fields = append(fields, it.Group+"/"+it.Label)
	}
	for _, f := range fields {
		if s, hit := fuzzyScore(f, query); hit && s > best {
			best, ok = s, true
		}
	}
	return best, ok
}
