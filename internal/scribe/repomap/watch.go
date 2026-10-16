package repomap

import (
	"context"
	"time"
)

// Watch keeps the map current with files changed outside the agent (an editor
// save, a formatter, a build step). Every poll it stats the indexed files and,
// once the set of changed files has stopped changing for debounce, re-indexes
// them with Update. Deleted files are dropped the same way.
//
// This is polling, not fsnotify: the module has no watcher dependency and a
// recursive inotify watch would cost a handle per directory. New files are not
// seen until the next Build. Watch returns when ctx is done. Defaults: poll
// 1s, debounce 500ms.
func (m *Map) Watch(ctx context.Context, poll, debounce time.Duration) {
	if poll <= 0 {
		poll = time.Second
	}
	if debounce <= 0 {
		debounce = 500 * time.Millisecond
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	var pending []string
	var since time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		stale := m.staleFiles()
		if len(stale) == 0 {
			pending = nil
			continue
		}
		if !sameFiles(stale, pending) {
			pending, since = stale, time.Now()
			continue
		}
		if time.Since(since) < debounce {
			continue
		}
		// An error leaves the files stale, so the next settled poll retries.
		if err := m.Update(stale); err == nil {
			pending = nil
		}
	}
}

// sameFiles reports whether a and b hold the same paths, in order.
func sameFiles(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
