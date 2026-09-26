package server

import (
	"sync"
	"time"
)

// bucket is a token bucket: rate tokens per second, holding at most burst.
type bucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newBucket(rate, burst float64) *bucket {
	return &bucket{rate: rate, burst: burst, tokens: burst, last: time.Now()}
}

func (b *bucket) refill() {
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
}

// take spends n tokens if they are all there.
func (b *bucket) take(n float64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	if b.tokens < n {
		return false
	}
	b.tokens -= n
	return true
}

// reserve spends n tokens, going into debt if needed, and says how long the
// caller must wait for the debt to clear. Used to throttle a device's reads
// (backpressure) rather than dropping its data.
func (b *bucket) reserve(n float64) time.Duration {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	b.tokens -= n
	if b.tokens >= 0 {
		return 0
	}
	return time.Duration(-b.tokens / b.rate * float64(time.Second))
}

// full reports whether the bucket has refilled completely, i.e. it holds no
// memory of recent use and can be forgotten.
func (b *bucket) full() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.refill()
	return b.tokens >= b.burst
}
