package app

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/memory"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// memoryStaleAfter is how long a memory may sit unused before the cleanup
// tab lists it. Records used at least memoryStaleMinUses times are kept.
const (
	memoryStaleAfter   = 90 * 24 * time.Hour
	memoryStaleMinUses = 2
	memoryCleanupLimit = 500
)

// MemoryCleanupCandidates implements tui.MemoryCleanup. It reports memories
// that are stale (not used for memoryStaleAfter and rarely used) and, in the
// project store, memories that name a file which no longer exists. Pinned
// memories are never stale. Nothing is changed here.
func (b *scribeBackend) MemoryCleanupCandidates() []tui.CleanupCandidate {
	a := b.current()
	if a.Memory == nil {
		return nil
	}
	var out []tui.CleanupCandidate
	index := map[string]int{}
	add := func(r memory.Record, reason string) {
		if i, ok := index[r.ID]; ok {
			out[i].Reasons = append(out[i].Reasons, reason)
			return
		}
		index[r.ID] = len(out)
		out = append(out, tui.CleanupCandidate{Item: memoryItem(r), Reasons: []string{reason}})
	}
	for _, db := range a.Memory.DBs() {
		stale, err := db.StaleCandidates(memoryStaleAfter, memoryStaleMinUses, memoryCleanupLimit)
		if err == nil {
			for _, r := range stale {
				add(r, fmt.Sprintf("not used in %d days", int(memoryStaleAfter/(24*time.Hour))))
			}
		}
		if groups, err := db.DuplicateGroups(memoryCleanupLimit); err == nil {
			for _, g := range groups {
				keeper := g[0] // the keeper: pinned or most recently used
				for _, r := range g[1:] {
					add(r, "duplicate of "+keeper.ID[:min(8, len(keeper.ID))])
				}
			}
		}
		if db.Scope() != memory.ScopeProject {
			continue // file references are relative to this project only
		}
		recs, err := db.List(memory.ListOptions{Limit: memoryCleanupLimit})
		if err != nil {
			continue
		}
		for _, r := range recs {
			for _, f := range r.Files {
				p := f
				if !filepath.IsAbs(p) {
					p = filepath.Join(a.Workdir, p)
				}
				if _, err := os.Stat(p); os.IsNotExist(err) {
					add(r, "file gone: "+f)
				}
			}
		}
	}
	return out
}
