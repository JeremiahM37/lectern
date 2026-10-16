package memory

// StaleCount counts the live, unpinned records across every open store that
// the stale check would review: not used for 90 days and used fewer than
// twice. It reads the same rule as the memory tool's "stale" action, and it
// deletes nothing. Errors count as zero, so a hint never blocks the UI.
func (s *Stores) StaleCount() int {
	n := 0
	for _, d := range s.DBs() {
		got, err := d.StaleCandidates(staleAfter, staleUses, 10000)
		if err != nil {
			continue
		}
		n += len(got)
	}
	return n
}
