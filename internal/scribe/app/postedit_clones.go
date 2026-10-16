package app

import (
	"context"
	"strings"
	"time"
)

// cloneBudget bounds the clone hints added after an edit. The hints come from
// the code map's in-memory indexes, so this is normally far more than enough;
// the budget only guards the first build of those indexes.
const cloneBudget = 500 * time.Millisecond

// cloneSource is the part of the code map the clone hints need. The code map
// satisfies it; a dependentSource that does not is simply given no hints.
type cloneSource interface {
	CloneHints(file string, max int) []string
}

// cloneBlock returns advisory lines saying which duplicates an edited file now
// takes part in, or "" when there are none or the map cannot say in time. It
// never blocks the edit: a slow answer is dropped.
func (p *postEditor) cloneBlock(files []string) string {
	p.mu.Lock()
	src, ok := p.deps.(cloneSource)
	p.mu.Unlock()
	if !ok || len(files) == 0 {
		return ""
	}
	type result struct {
		lines []string
	}
	done := make(chan result, 1)
	go func() {
		var out []string
		for _, abs := range files {
			for _, h := range src.CloneHints(p.rel(abs), 3) {
				out = append(out, p.rel(abs)+": "+h)
			}
		}
		done <- result{out}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), cloneBudget)
	defer cancel()
	select {
	case r := <-done:
		if len(r.lines) == 0 {
			return ""
		}
		return "Possible duplicates (advisory):\n  " + strings.Join(r.lines, "\n  ")
	case <-ctx.Done():
		return ""
	}
}
