package executor

import (
	"net/http"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/internal/store"
)

// Registry hands out one cached executor per target id.
type Registry struct {
	mu    sync.Mutex
	cache map[int64]Executor

	// Mock forces every target onto the scripted MockExecutor.
	Mock      bool
	MockDelay time.Duration
	// MockHTTP is the client fake agents use for hook callbacks. Tests point it
	// at their own server.
	MockHTTP *http.Client
}

// NewRegistry builds an executor registry.
func NewRegistry(mock bool, mockDelay time.Duration) *Registry {
	return &Registry{cache: map[int64]Executor{}, Mock: mock, MockDelay: mockDelay}
}

// For returns the executor for a target, creating and caching it on first use.
func (r *Registry) For(t *store.Target) (Executor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ex, ok := r.cache[t.ID]; ok {
		return ex, nil
	}
	kind := t.Kind
	if r.Mock {
		kind = "mock"
	}
	var ex Executor
	switch kind {
	case "mock":
		m := NewMock(r.MockDelay)
		if r.MockHTTP != nil {
			m.HTTP = r.MockHTTP
		}
		ex = m
	case "local", "sandbox":
		// sandbox host-side work (pct clone/destroy) runs on the control plane;
		// the agent itself runs inside the container via a per-attempt Pct
		ex = NewLocal()
	case "ssh":
		ex = NewSSH(t.Host, t.User, t.Port, t.KeyPath, t.CommandPrefix)
	case "pct":
		ex = NewPct(t.Host)
	default:
		return nil, Errf("unknown target kind %q", kind)
	}
	r.cache[t.ID] = ex
	return ex, nil
}

// Any returns one cached executor. Tests use it to inspect what reached "the
// target" without knowing which target id won the race.
func (r *Registry) Any() Executor {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, ex := range r.cache {
		return ex
	}
	return nil
}

// Reset drops the cache — called whenever a target's connection details change.
func (r *Registry) Reset() {
	r.mu.Lock()
	old := r.cache
	r.cache = map[int64]Executor{}
	r.mu.Unlock()
	for _, ex := range old {
		_ = ex.Close()
	}
}
