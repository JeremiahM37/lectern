package web

import (
	"sync"
	"time"
)

// ttlCache holds results for a fixed time and a fixed number of keys. When
// it is full, the entry stored longest ago is dropped first. It is safe for
// concurrent use.
type ttlCache[V any] struct {
	mu    sync.Mutex
	ttl   time.Duration
	max   int
	now   func() time.Time
	items map[string]ttlItem[V]
	seq   uint64
}

type ttlItem[V any] struct {
	val     V
	expires time.Time
	seq     uint64
}

func newTTLCache[V any](ttl time.Duration, max int) *ttlCache[V] {
	return &ttlCache[V]{ttl: ttl, max: max, now: time.Now, items: map[string]ttlItem[V]{}}
}

// Get returns the value for key if it is stored and still fresh. An expired
// entry is removed.
func (c *ttlCache[V]) Get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero V
	it, ok := c.items[key]
	if !ok {
		return zero, false
	}
	if !c.now().Before(it.expires) {
		delete(c.items, key)
		return zero, false
	}
	return it.val, true
}

// Put stores val under key for the cache's TTL.
func (c *ttlCache[V]) Put(key string, val V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; !exists && len(c.items) >= c.max {
		c.evictOldest()
	}
	c.seq++
	c.items[key] = ttlItem[V]{val: val, expires: c.now().Add(c.ttl), seq: c.seq}
}

// evictOldest removes the entry with the smallest sequence number. Expired
// entries go first. The caller holds mu.
func (c *ttlCache[V]) evictOldest() {
	now := c.now()
	var oldestKey string
	var oldest uint64
	found := false
	for k, it := range c.items {
		if !now.Before(it.expires) {
			delete(c.items, k)
			return
		}
		if !found || it.seq < oldest {
			oldestKey, oldest, found = k, it.seq, true
		}
	}
	if found {
		delete(c.items, oldestKey)
	}
}

const (
	// resultTTL is how long a search or fetch result is reused.
	resultTTL = 5 * time.Minute
	// resultCacheSize bounds each cache.
	resultCacheSize = 100
	// maxCachedPageChars keeps one very large page from filling the cache.
	maxCachedPageChars = 1 << 20
)
