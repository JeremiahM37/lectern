package remote

import (
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// Throttle lets an event through at most once per interval for each key.
// Progress lines use it so a chatty tool cannot flood a phone.
type Throttle struct {
	mu    sync.Mutex
	every time.Duration
	last  map[string]time.Time
	now   func() time.Time
}

// NewThrottle returns a Throttle that allows one event per key every d.
func NewThrottle(d time.Duration) *Throttle {
	return &Throttle{every: d, last: map[string]time.Time{}, now: time.Now}
}

// Allow reports whether an event for key may go out now, and records it if so.
func (t *Throttle) Allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	if last, ok := t.last[key]; ok && now.Sub(last) < t.every {
		return false
	}
	t.last[key] = now
	return true
}

// Chunk splits text into pieces of at most n bytes for platforms with a
// message size limit. It breaks at the last newline that fits, else at the
// last rune boundary, and never splits a UTF-8 sequence.
func Chunk(text string, n int) []string {
	if n <= 0 {
		n = 1
	}
	var out []string
	for len(text) > n {
		cut := strings.LastIndexByte(text[:n+1], '\n')
		if cut <= 0 {
			cut = n
			for cut > 0 && !utf8.RuneStart(text[cut]) {
				cut--
			}
		}
		if cut <= 0 {
			break
		}
		out = append(out, strings.TrimRight(text[:cut], "\n"))
		text = text[cut:]
		text = strings.TrimLeft(text, "\n")
	}
	if text != "" || len(out) == 0 {
		out = append(out, text)
	}
	return out
}
