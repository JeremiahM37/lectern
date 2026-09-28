package executor

import (
	"net/http"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
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
	// Env, when set, resolves what is known about a target (its session
	// backend, its lectern binary) once its executor is made; see TargetEnv.
	// It runs under the registry's lock, so it must not reach the target:
	// it decides from the row (and its stored probe) and this host alone.
	Env func(t *store.Target, ex Executor) TargetEnv
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
		opts := ParseSSHOptions(t.SSHJSON)
		if opts.Transport == "openssh" {
			ex = NewOpenSSH(t.Host, t.User, t.Port, t.KeyPath, t.CommandPrefix, opts)
		} else {
			ex = NewSSH(t.Host, t.User, t.Port, t.KeyPath, t.CommandPrefix).WithOptions(opts)
		}
	case "pct":
		ex = NewPct(t.Host)
	default:
		return nil, Errf("unknown target kind %q", kind)
	}
	if r.Env != nil {
		SetTargetEnv(ex, r.Env(t, ex))
	}
	r.cache[t.ID] = ex
	return ex, nil
}

// Refresh re-resolves what is known about a target whose row changed (a new
// probe), keeping its executor and connection.
func (r *Registry) Refresh(t *store.Target) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ex, ok := r.cache[t.ID]; ok && r.Env != nil {
		SetTargetEnv(ex, r.Env(t, ex))
	}
}

// Cached returns the executor already made for a target, without making one,
// so asking how a connection is doing never opens it.
func (r *Registry) Cached(id int64) Executor {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cache[id]
}

// Forget drops one target's executor, so the next command dials fresh.
func (r *Registry) Forget(id int64) {
	r.mu.Lock()
	ex := r.cache[id]
	delete(r.cache, id)
	r.mu.Unlock()
	if ex != nil {
		forgetTargetEnv(ex)
		_ = ex.Close()
	}
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
		forgetTargetEnv(ex)
		_ = ex.Close()
	}
}
