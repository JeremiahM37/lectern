package app

// DisposeLastBus is called when this backend's tab closes. It disposes the
// newest dispatch's bus so the closed tab's subagent caches are released.
func (b *scribeBackend) DisposeLastBus() {
	a := b.current()
	if a == nil || a.Dispatch == nil {
		return
	}
	a.Dispatch.DisposeLastBus()
}
