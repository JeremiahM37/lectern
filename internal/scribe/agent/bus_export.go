package agent

// BusCache is what a dispatch's bus keeps for the next dispatch in the same
// tab (spec 03 section 3.2 and 3.8). It holds finished reads and read-only
// tool results only: no findings, no edit owners, no running reads.
type BusCache struct {
	reads   map[string]cachedRead
	results map[string]cachedResult
}

// ExportCaches copies what a later dispatch can use. A read keeps its stamp,
// so an edit since then makes it a miss; the subagent that read it is dropped,
// since its id means nothing in the next dispatch. A result is left out when
// it depends on files the cache could not name, because nothing can check it
// later, and when it is past its TTL. Export returns nil for a nil or disposed
// bus.
func (b *AgentBus) ExportCaches() *BusCache {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.disposed {
		return nil
	}
	c := &BusCache{reads: map[string]cachedRead{}, results: map[string]cachedResult{}}
	for k, r := range b.reads {
		r.by = ""
		c.reads[k] = r
	}
	now := b.clock()
	for k, r := range b.results {
		if r.wild || now.Sub(r.at) > resultTTL {
			continue
		}
		c.results[k] = r
	}
	return c
}

// SeedCaches loads c into a bus that has not run yet, through the same limits
// as a live store, so a seed cannot push the bus past them. A nil c, or a
// disposed bus, does nothing.
func (b *AgentBus) SeedCaches(c *BusCache) {
	if b == nil || c == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.disposed {
		return
	}
	for k, r := range c.reads {
		b.storeReadLocked(k, r)
	}
	for k, r := range c.results {
		b.storeResultLocked(k, r)
	}
}

// Dispose ends the bus's caches when its tab closes or its dispatch is over.
// It drops every read and result, wakes any reader still waiting on a read
// that will not finish, and makes later stores and seeds no-ops. Findings and
// edit owners are kept, because the dispatch's report is built from them.
func (b *AgentBus) Dispose() {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.disposed = true
	b.reads = map[string]cachedRead{}
	b.readBytes = 0
	b.results = map[string]cachedResult{}
	for k, f := range b.flights {
		releaseFlightLocked(f)
		delete(b.flights, k)
	}
}
