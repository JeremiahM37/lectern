package agent

// SetMaxToolResults applies the max_tool_results config to the running
// agent, the way SetCompaction applies the strategy. Zero keeps the default.
// It only changes a SummaryCompactor; a disabled compaction is left alone.
func (a *Agent) SetMaxToolResults(n int) {
	if n < 0 {
		n = 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	sc, ok := a.opts.Compactor.(SummaryCompactor)
	if !ok {
		return
	}
	sc.MaxToolResults = n
	a.opts.Compactor = sc
}
