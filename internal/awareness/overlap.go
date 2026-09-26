package awareness

import "github.com/JeremiahM37/lectern/v2/internal/store"

// Overlap is the "⚠ overlaps #N" card chip: another live session that touched
// at least one of the same files as this one within EditWarnWindow.
type Overlap struct {
	SessionID int64
	Name      string
	Files     []string
}

// Overlaps computes the overlap chip for every row of a session list in one
// pass, from data the caller has already loaded: the live sessions (in the
// order LiveSessions returns them) and every file edit recorded since
// now-EditWarnWindow, newest first (SessionFileEditsSince).
//
// It gives each row exactly what a per-row Peers lookup would: the first live
// peer in the same repository that edited a file this row also edited, with
// the shared paths in the peer's newest-first order. Doing it per row cost a
// full live-session scan plus one edit query per peer for every row, which
// made listing N sessions that share a repository O(N²) queries — 160 ms for
// 100 sessions in the scale benchmark (docs/benchmarks/scale.md).
func Overlaps(rows, live []*store.Session, edits []*store.SessionFileEdit) map[int64]*Overlap {
	bySession := map[int64][]*store.SessionFileEdit{}
	for _, e := range edits {
		bySession[e.SessionID] = append(bySession[e.SessionID], e)
	}
	byRepo := map[string][]*store.Session{}
	for _, s := range live {
		if s.RepoKey != "" && s.RepoKey != RepoKeyNone && len(bySession[s.ID]) > 0 {
			byRepo[s.RepoKey] = append(byRepo[s.RepoKey], s)
		}
	}
	out := map[int64]*Overlap{}
	for _, row := range rows {
		if row.RepoKey == "" || row.RepoKey == RepoKeyNone {
			continue
		}
		self := bySession[row.ID]
		if len(self) == 0 {
			continue
		}
		selfPaths := make(map[string]bool, len(self))
		for _, e := range self {
			selfPaths[e.RelPath] = true
		}
		for _, peer := range byRepo[row.RepoKey] {
			if peer.ID == row.ID {
				continue
			}
			var shared []string
			for _, e := range bySession[peer.ID] {
				if selfPaths[e.RelPath] {
					shared = append(shared, e.RelPath)
				}
			}
			if len(shared) > 0 {
				out[row.ID] = &Overlap{SessionID: peer.ID, Name: peer.Name, Files: shared}
				break
			}
		}
	}
	return out
}
