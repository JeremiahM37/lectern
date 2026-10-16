package telegram

// The surface keeps the last lines its bot logs, so `/remote logs` can show
// why a poll or send failed without a terminal scrape. Lines are redacted
// before they are kept, and the ring is small and bounded.

import (
	"fmt"
	"strings"
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/redact"
)

const logKeep = 50

type logRing struct {
	mu    sync.Mutex
	lines []string
}

// wrap returns a logf that records each line and then calls next (if set).
func (r *logRing) wrap(next func(string, ...any)) func(string, ...any) {
	return func(format string, args ...any) {
		line := redact.Default().Redact(fmt.Sprintf(format, args...))
		r.add(line)
		if next != nil {
			next(format, args...)
		}
	}
}

func (r *logRing) add(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, strings.TrimSpace(line))
	if over := len(r.lines) - logKeep; over > 0 {
		r.lines = append([]string(nil), r.lines[over:]...)
	}
}

func (r *logRing) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.lines...)
}

// Logs returns the most recent surface log lines, oldest first.
func (s *Surface) Logs() []string { return s.logs.snapshot() }
