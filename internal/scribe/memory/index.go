package memory

import (
	"fmt"
	"sort"
	"strings"
)

// indexMaxChars caps the system-prompt memory index (spec 05 §1.7).
const indexMaxChars = 800

// Index is the counts-only line the system prompt carries about saved
// memories: one clause per open store with its live total, the per-category
// counts and the pinned count. Nothing is recalled here, so it costs one
// grouped count per store. It returns "" when both stores are empty.
func (s *Stores) Index() string {
	var parts []string
	for _, d := range s.DBs() {
		rows, err := d.db.Query("SELECT category, COUNT(*), COALESCE(SUM(pinned), 0) FROM memories WHERE " + live + " GROUP BY category")
		if err != nil {
			continue
		}
		counts := map[string]int{}
		total, pinned := 0, 0
		for rows.Next() {
			var cat string
			var n, p int
			if rows.Scan(&cat, &n, &p) == nil {
				counts[cat] = n
				total += n
				pinned += p
			}
		}
		rows.Close()
		if total == 0 {
			continue
		}
		cats := make([]string, 0, len(counts))
		for c := range counts {
			cats = append(cats, c)
		}
		sort.Strings(cats)
		var by []string
		for _, c := range cats {
			by = append(by, fmt.Sprintf("%s %d", c, counts[c]))
		}
		parts = append(parts, fmt.Sprintf("- %s: %d (%s), %d pinned", d.Scope(), total, strings.Join(by, ", "), pinned))
	}
	if len(parts) == 0 {
		return ""
	}
	out := strings.Join(parts, "\n")
	if len(out) > indexMaxChars {
		out = out[:indexMaxChars]
		if i := strings.LastIndexByte(out, '\n'); i > 0 {
			out = out[:i]
		}
	}
	return out
}
