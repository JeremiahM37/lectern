package agent

import "sync"

// cacheRef is the read cache one dispatch leaves for the next one in the same
// tab (spec 03 §3.2 and §3.8 step 5). A new dispatch bus starts with these
// reads, so a file read in one dispatch is not read again in the next. Each
// entry still has to match the file's stamp to be used, so a file that changed
// in between is read afresh.
type cacheRef struct {
	mu    sync.Mutex
	reads map[string]cachedRead
}

// keep copies the reads of b, the bus of a finished dispatch, as the reads the
// next dispatch starts with. It replaces what was kept before.
func (c *cacheRef) keep(b *AgentBus) {
	if b == nil {
		return
	}
	b.mu.Lock()
	reads := make(map[string]cachedRead, len(b.reads))
	for k, v := range b.reads {
		reads[k] = v
	}
	b.mu.Unlock()
	c.mu.Lock()
	c.reads = reads
	c.mu.Unlock()
}

// seed stores the kept reads into b, a bus about to start a dispatch. The bus
// applies its own size limit, so a large kept cache is trimmed as it fills.
func (c *cacheRef) seed(b *AgentBus) {
	if b == nil {
		return
	}
	c.mu.Lock()
	reads := c.reads
	c.mu.Unlock()
	if len(reads) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, v := range reads {
		b.storeReadLocked(k, v)
	}
}
