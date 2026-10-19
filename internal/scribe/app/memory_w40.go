package app

// Memory wiring the file tools and the compaction path need (spec 05 §1.7,
// spec 03 §8.4). The app calls these from its existing open and event paths.

// publishMemoryService exposes the memory stores to the file tools' read and
// edit footers under Services["memory"]. It publishes nothing when memory is
// off: a nil *memory.Stores stored in the map would satisfy the interface and
// then be dereferenced.
func (a *App) publishMemoryService() {
	if a.Memory == nil || a.services == nil {
		return
	}
	a.services["memory"] = a.Memory
}

// memoryTurnStarted clears the per-turn memory-search suppression, so file
// hints come back on the new turn. It is called on the lead agent's turn start.
func (a *App) memoryTurnStarted() {
	if a.Memory != nil {
		a.Memory.BeginTurn()
	}
}

// saveCompactionMemory writes the compaction summary to memory in the
// background. A failed write is dropped: the compaction itself already
// succeeded and the summary is still in the conversation.
func (a *App) saveCompactionMemory(summary string) {
	m := a.Memory
	if m == nil || summary == "" {
		return
	}
	workdir := a.Workdir
	go func() {
		_, _, _ = m.SaveCompaction(summary, workdir)
	}()
}
