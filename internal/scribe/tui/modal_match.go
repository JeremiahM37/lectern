package tui

// Matching helpers for the shared picker: the slash split for group/item
// queries, the token test, and the busy gate that async modals implement.

import (
	"strings"
	"unicode/utf8"
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

// Match tiers. A name match always outranks a description-only match, and
// within names a prefix beats a word start, a word start beats a substring,
// and a substring beats a loose subsequence. The fuzzy score only breaks ties
// inside a tier, so tierStep must stay far above any fuzzy score.
const (
	tierDesc = iota
	tierFuzzy
	tierSubstring
	tierWordStart
	tierPrefix
)

const tierStep = 1 << 20

// descMinQuery is the shortest query that matches description text. Shorter
// queries hit so many descriptions that the list is mostly noise.
const descMinQuery = 3

// nameScore scores a query against one name (a label or value). A leading
// slash is ignored, so "und" finds "/undo". q must already be lower case.
func nameScore(name, q string) (int, bool) {
	fuzzy, hit := fuzzyScore(name, q)
	if !hit {
		return 0, false
	}
	n := strings.ToLower(strings.TrimPrefix(name, "/"))
	tier := tierFuzzy
	switch {
	case strings.HasPrefix(n, q):
		tier = tierPrefix
	case wordStartContains(n, q):
		tier = tierWordStart
	case strings.Contains(n, q):
		tier = tierSubstring
	}
	return tier*tierStep + fuzzy, true
}

// wordStartContains reports whether q starts at a word boundary inside n.
func wordStartContains(n, q string) bool {
	r := []rune(n)
	for i := 1; i < len(r); i++ {
		if isSep(r[i-1]) && strings.HasPrefix(string(r[i:]), q) {
			return true
		}
	}
	return false
}

// matchItem scores one item against the picker's query. It reports whether
// the item survives. A query with a slash splits at the first one: the left
// side must match the group label, and the right side fuzzy-matches the item
// label (an empty right side keeps every item of a matching group). Without a
// slash an item survives if its label or value matches; its description
// matches too, but only for queries of descMinQuery characters or more, and
// always below every name match. Group text is not searched on its own, so a
// category name does not pull in every row of that category.
func matchItem(it PickItem, query string) (int, bool) {
	if i := strings.IndexByte(query, '/'); i >= 0 {
		group, item := query[:i], query[i+1:]
		if !tokenMatches(it.Group, group) {
			return 0, false
		}
		if item == "" {
			return 0, true
		}
		return nameScore(it.Label, strings.ToLower(item))
	}
	if query == "" {
		return 0, true
	}
	q := strings.ToLower(query)
	best, ok := 0, false
	for _, f := range []string{it.Label, it.Value} {
		if s, hit := nameScore(f, q); hit && (!ok || s > best) {
			best, ok = s, true
		}
	}
	if ok {
		return best, true
	}
	if utf8.RuneCountInString(q) >= descMinQuery && strings.Contains(strings.ToLower(it.Desc), q) {
		return tierDesc * tierStep, true
	}
	return 0, false
}
