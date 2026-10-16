package app

import (
	"sync"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// openHooks run at the end of Open, after the host and the built-in tools
// are in place and before the caller gets the App. The Lectern bridge uses
// one so that the headless runner and the TUI both pick it up without either
// having to know about Lectern.
var (
	openHooksMu sync.Mutex
	openHooks   []func(*App)
)

// OnOpen registers fn to run at the end of every Open in this process.
// Registration is process-wide and cannot be undone; register once.
func OnOpen(fn func(*App)) {
	openHooksMu.Lock()
	openHooks = append(openHooks, fn)
	openHooksMu.Unlock()
}

func runOpenHooks(a *App) {
	openHooksMu.Lock()
	hooks := append([]func(*App){}, openHooks...)
	openHooksMu.Unlock()
	for _, fn := range hooks {
		fn(a)
	}
}

// SetHost replaces the host that answers approvals and questions. Call it
// before the first Run (OnOpen hooks are the intended place): tool calls read
// the host when they start, and a swap in the middle of a turn is not
// supported.
func (a *App) SetHost(h tools.Host) {
	if h == nil {
		h = headlessHost{}
	}
	a.host = h
}

// Host returns the host that answers approvals and questions.
func (a *App) Host() tools.Host { return a.host }

// Interactive reports whether a host can answer right now, in a way a person
// sees. The headless host cannot; a remote host (Lectern) can wait for one.
// Hosts that do not implement it are treated as interactive.
func (headlessHost) Interactive() bool { return false }
