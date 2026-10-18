package app

// Wave 38: the proxy sidecar's wiring into the backend. Three hooks:
//
//   - EnsureProxy starts the sidecar in the background when the model in use
//     is a proxy model (spec 03 section 11, step 1). Ensure adopts a proxy that
//     is already answering, so calling it again is cheap.
//   - StopProxy stops the sidecar this Scribe started, at shutdown. A proxy
//     another process started is never stopped.
//   - ProxySidecar hands /proxy the sidecar for the proxy provider's address.
//
// The call sites that run EnsureProxy and StopProxy are outside this file; see
// the wave 38 report.

import (
	"context"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/proxy"
)

// proxyEnsureWait bounds a background start. Ensure waits up to 5 s for a
// started proxy to answer, plus the probes before that.
const proxyEnsureWait = 30 * time.Second

// proxyStopWait bounds the stop at shutdown. The sidecar gets 3 s after
// SIGTERM, then a SIGKILL, so this leaves room for both.
const proxyStopWait = 5 * time.Second

// proxyAutoStart keeps one background start in flight. Each Ensure builds its
// own sidecar value, so this guard is what stops two model switches from
// starting two proxies.
var proxyAutoStart struct {
	sync.Mutex
	running bool
}

// ProxySidecar returns the sidecar for the proxy provider's address, or nil
// when there is no app or no proxy provider.
func (b *scribeBackend) ProxySidecar() *proxy.Sidecar {
	a := b.current()
	if a == nil || a.Registry == nil {
		return nil
	}
	info, ok := a.Registry.Lookup("proxy")
	if !ok {
		return nil
	}
	return proxy.NewSidecar(proxy.SidecarOptions{BaseURL: info.BaseURL})
}

// EnsureProxy starts the proxy sidecar in the background when the model in
// use is a proxy model. Other models do nothing. A missing binary is not an
// error here: the first request to the proxy reports it, and /proxy status
// shows it.
func (b *scribeBackend) EnsureProxy() {
	a := b.current()
	if a == nil || a.Agent == nil || !isProxyModel(a.Agent.CurrentModel()) {
		return
	}
	sc := b.ProxySidecar()
	if sc == nil || !beginProxyStart() {
		return
	}
	go func() {
		defer endProxyStart()
		ctx, cancel := context.WithTimeout(context.Background(), proxyEnsureWait)
		defer cancel()
		// The error is dropped on purpose: the proxy is started on first use
		// anyway, and that request reports the same failure in the chat.
		_, _ = sc.Ensure(ctx)
	}()
}

// StopProxy stops the proxy sidecar if this Scribe started it. It is the
// shutdown step for the proxy (spec 06 section 8.3, deactivate provider).
func (b *scribeBackend) StopProxy() error {
	sc := b.ProxySidecar()
	if sc == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), proxyStopWait)
	defer cancel()
	return sc.Stop(ctx)
}

// isProxyModel reports whether a model is served by the proxy provider.
func isProxyModel(m agent.Model) bool {
	return m.Provider != nil && m.Provider.ID() == "proxy"
}

// beginProxyStart claims the single background start. It reports false when
// one is already running.
func beginProxyStart() bool {
	proxyAutoStart.Lock()
	defer proxyAutoStart.Unlock()
	if proxyAutoStart.running {
		return false
	}
	proxyAutoStart.running = true
	return true
}

func endProxyStart() {
	proxyAutoStart.Lock()
	defer proxyAutoStart.Unlock()
	proxyAutoStart.running = false
}
