package app

import (
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui"
)

// errorSink collects faults that happen outside a turn (an MCP or language
// server that will not start, a hook, the auto-check) so the error log can
// show them with their source. Anything in the app may Post; the TUI drains
// the queue with BackgroundErrors. A fault that is still standing is posted
// once: Sync posts when a server first fails and re-arms when it recovers.
type errorSink struct {
	mu    sync.Mutex
	queue []tui.BackgroundError
	fails map[string]bool // keys of faults already posted and not yet cleared
}

// Post queues one fault. Empty text is ignored.
func (s *errorSink) Post(source, text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = append(s.queue, tui.BackgroundError{Source: source, Text: text, At: time.Now()})
}

// Sync reports the standing state of one component under key. A failing key
// is posted when it first fails; a healthy one is re-armed so the next
// failure posts again.
func (s *errorSink) Sync(key, source, text string, failing bool) {
	s.mu.Lock()
	if !failing {
		delete(s.fails, key)
		s.mu.Unlock()
		return
	}
	if s.fails == nil {
		s.fails = map[string]bool{}
	}
	already := s.fails[key]
	s.fails[key] = true
	s.mu.Unlock()
	if !already {
		s.Post(source, text)
	}
}

// Drain returns the queued faults, oldest first, and empties the queue.
func (s *errorSink) Drain() []tui.BackgroundError {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.queue
	s.queue = nil
	return out
}
