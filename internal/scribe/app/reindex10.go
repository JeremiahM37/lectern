package app

import (
	"sort"
	"sync"
	"time"
)

// reindexDelay is how long the code map waits after the last edit before it
// re-reads the changed files. An agent's edit often comes in a burst (edit,
// then the post-edit pass, then a formatter's rewrite); one re-parse covers
// the burst.
const reindexDelay = 150 * time.Millisecond

// reindexer coalesces file changes into one code map update after a quiet
// period. It is a trailing debounce: every Add restarts the wait.
type reindexer struct {
	delay time.Duration
	flush func(paths []string)

	mu      sync.Mutex
	pending map[string]bool
	timer   *time.Timer
	stopped bool
}

func newReindexer(delay time.Duration, flush func(paths []string)) *reindexer {
	return &reindexer{delay: delay, flush: flush, pending: map[string]bool{}}
}

// Add queues paths for the next flush and restarts the quiet period.
func (r *reindexer) Add(paths ...string) {
	if len(paths) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stopped {
		return
	}
	for _, p := range paths {
		r.pending[p] = true
	}
	if r.timer == nil {
		r.timer = time.AfterFunc(r.delay, r.fire)
	} else {
		r.timer.Reset(r.delay)
	}
}

// fire takes the queued paths and flushes them.
func (r *reindexer) fire() {
	r.mu.Lock()
	if r.stopped || len(r.pending) == 0 {
		r.timer = nil
		r.mu.Unlock()
		return
	}
	paths := make([]string, 0, len(r.pending))
	for p := range r.pending {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	r.pending = map[string]bool{}
	r.timer = nil
	r.mu.Unlock()
	r.flush(paths)
}

// Stop drops the queued paths and stops further flushes. The code map's
// cache is keyed by modification time, so a dropped update is repaired by
// the next start (or the watcher's next pass), not lost.
func (r *reindexer) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stopped = true
	r.pending = map[string]bool{}
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
}
