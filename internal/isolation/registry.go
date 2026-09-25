package isolation

import (
	"fmt"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
)

// DefaultRuntimeDir holds every session's egress-proxy unix socket. It is
// unrelated to any project's working tree on purpose — a bwrap sandbox's
// bind mounts should not have to reach outside the workdir to find it, and a
// stray socket file must never end up inside a git worktree.
const DefaultRuntimeDir = "/tmp/lectern-isolation"

// ProxyRegistry keeps one AllowlistProxy alive per session or attempt for as
// long as its bwrap network=deny sandbox is running, and tears it down when
// the caller reports it ended. A launch's proxy outlives the tmux-launch
// call that started it — tmux sessions are detached and long-running, so the
// proxy has to be too.
type ProxyRegistry struct {
	// Dir overrides DefaultRuntimeDir; tests point it at a temp directory.
	Dir string
	log *slog.Logger

	mu   sync.Mutex
	live map[int64]*runningProxy
}

type runningProxy struct {
	listener net.Listener
	path     string
}

// NewProxyRegistry builds an empty registry.
func NewProxyRegistry(log *slog.Logger) *ProxyRegistry {
	if log == nil {
		log = slog.Default()
	}
	return &ProxyRegistry{log: log, live: map[int64]*runningProxy{}}
}

func (r *ProxyRegistry) dir() string {
	if r.Dir != "" {
		return r.Dir
	}
	return DefaultRuntimeDir
}

// Start opens id's egress proxy on a fresh unix socket and begins serving in
// the background. The returned path is what the sandbox's bind mount and
// BuildBwrapArgv's ProxySocket both need. Calling Start again for an id that
// already has one replaces it — a relaunch or resume gets a fresh proxy.
func (r *ProxyRegistry) Start(id int64, allow []string) (socketPath string, err error) {
	if r == nil {
		return "", fmt.Errorf("isolation: no proxy registry configured")
	}
	r.Stop(id)
	dir := r.dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("isolation: proxy runtime dir: %w", err)
	}
	socketPath = filepath.Join(dir, fmt.Sprintf("session-%d.sock", id))
	_ = os.Remove(socketPath)
	l, err := net.Listen("unix", socketPath)
	if err != nil {
		return "", fmt.Errorf("isolation: could not open proxy socket: %w", err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		l.Close()
		return "", err
	}
	p := NewAllowlistProxy(allow)
	go func() {
		if err := p.Serve(l); err != nil {
			r.log.Debug("isolation egress proxy stopped", "session", id, "err", err)
		}
	}()
	r.mu.Lock()
	r.live[id] = &runningProxy{listener: l, path: socketPath}
	r.mu.Unlock()
	return socketPath, nil
}

// Stop closes id's proxy, if one is running, and removes its socket file.
// It is always safe to call, including for an id with no live proxy or on a
// nil registry (a Manager built without sessions.New's constructor never
// started one, so this is a deliberate no-op rather than a crash).
func (r *ProxyRegistry) Stop(id int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	rp := r.live[id]
	delete(r.live, id)
	r.mu.Unlock()
	if rp == nil {
		return
	}
	_ = rp.listener.Close()
	_ = os.Remove(rp.path)
}

// Running reports whether id currently has a live proxy — tests use this to
// confirm Stop actually tore one down.
func (r *ProxyRegistry) Running(id int64) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.live[id]
	return ok
}
