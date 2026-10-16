package agent

// SetCompaction applies the compaction settings from config to the running
// agent: the strategy ("v1" or "v2") and how many recent messages to keep
// verbatim (0 keeps the token-sized tail). It only changes a SummaryCompactor;
// a disabled compaction has no compactor and is left alone. The next
// compaction uses the new values, so a settings change lands at the next turn.
func (a *Agent) SetCompaction(strategy string, keepRecent int) {
	if keepRecent < 0 {
		keepRecent = 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	sc, ok := a.opts.Compactor.(SummaryCompactor)
	if !ok {
		return
	}
	sc.Strategy = strategy
	sc.KeepRecent = keepRecent
	a.opts.Compactor = sc
}
