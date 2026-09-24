// Package alerts pushes a phone notification when an interactive session
// needs its operator — docs/agent-events.md section 3. It is wired as
// agentevents.Ingester.OnHookEvent (see internal/app), so it only observes
// hook-driven sessions. That covers the overwhelming majority in practice:
// every builtin claude/codex session installs its hooks at launch unless the
// install itself fails (section 2), and a hooked session's state is
// hook-preferred for 10 minutes of silence before the screen-scrape fallback
// is even allowed to touch it (internal/sessions/poll.go). A session that
// never gets hooks at all, or one that has been silent past that window, is
// not separately alerted by this package — see the section 3 worker's final
// report for that scope call. PreCompact and Stop's last_assistant_message
// never reach the bus as structured session fields, which is why this
// listens on the raw hook event rather than a generic "session changed" bus
// subscription: the fields it needs are only ever on the wire, once, right
// here.
package alerts

import (
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/sinks"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Settings keys, appended to sinks.Keys so the existing PUT/GET
// /api/settings endpoint and Notifications UI can read and write them
// without a second settings registry. Every one defaults ON: a fresh
// install should not silently swallow the first alert while an operator is
// still discovering the toggle exists.
const (
	KeyWaitingPermission = "alert_waiting_permission"
	KeyWaitingInput      = "alert_waiting_input"
	KeyIdle              = "alert_idle"
	KeyError             = "alert_error"
	KeyCompacting        = "alert_compacting"
)

// DefaultCooldown is the minimum spacing between two alerts for the SAME
// session, regardless of what changed — a flapping pane must not become a
// notification storm. Never repeating for the same target state is a
// separate, always-on rule that falls out of only ever firing on an actual
// IngestEvent state transition (see HandleHookEvent) — no bookkeeping is
// needed for that half, only for this one.
const DefaultCooldown = 60 * time.Second

// SuppressWindow: a browser that sent real terminal input for a session in
// the last 30s means a person is plainly already looking at it.
const SuppressWindow = 30 * time.Second

// Activity records the last time a browser sent input into a session's
// terminal, for the "suppressed if a browser typed into that session
// within the last 30s" rule.
//
// Populated from POST /api/term/{kind}/{id}/activity, an explicit heartbeat
// the terminal page sends whenever it actually sends bytes to the pty
// (frontend/src/terminal/engine.ts's input(), throttled client-side) —
// documented deviation from hooking the ttyd reverse proxy itself: the
// proxy (internal/api/termproxy.go's termProxy) hands an Upgrade request
// straight to net/http/httputil.ReverseProxy, which hijacks the connection
// and pipes it byte-for-byte in both directions with no per-message
// callback. Adding one would mean replacing that proxy with a hand-rolled
// hijacking implementation across every terminal attachment kind
// (session/attempt/project/companion shells) — a lot of surface and
// regression risk to take on for what an explicit page-level heartbeat
// already gets, with far less code and no risk to the existing terminal
// e2e suite.
type Activity struct {
	mu   sync.Mutex
	seen map[int64]time.Time
}

// NewActivity builds an empty tracker.
func NewActivity() *Activity { return &Activity{seen: map[int64]time.Time{}} }

// Touch records that a browser just sent real input to sessionID.
func (a *Activity) Touch(sessionID int64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen[sessionID] = time.Now()
}

// Recent reports whether sessionID had input within window.
func (a *Activity) Recent(sessionID int64, window time.Duration) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	t, ok := a.seen[sessionID]
	return ok && time.Since(t) < window
}

// Watcher turns hook events into pushes through the existing sinks.Notifier.
// Wire HandleHookEvent as agentevents.Ingester.OnHookEvent.
type Watcher struct {
	DB       *store.DB
	Notifier *sinks.Notifier
	// Activity suppresses an alert when set and the session was just typed
	// into; nil disables suppression entirely (still safe, just less quiet).
	Activity *Activity
	// Cooldown overrides DefaultCooldown; zero means DefaultCooldown.
	Cooldown time.Duration
	// Clock overrides time.Now for tests. nil means time.Now.
	Clock func() time.Time

	mu          sync.Mutex
	lastAlertAt map[int64]time.Time
}

func (w *Watcher) now() time.Time {
	if w.Clock != nil {
		return w.Clock()
	}
	return time.Now()
}

func (w *Watcher) cooldown() time.Duration {
	if w.Cooldown > 0 {
		return w.Cooldown
	}
	return DefaultCooldown
}

// enabled reports a toggle's value: default ON, off only when explicitly "0".
func (w *Watcher) enabled(key string) bool {
	return strings.TrimSpace(w.DB.Setting(key)) != "0"
}

// HandleHookEvent is agentevents.Ingester.OnHookEvent. It runs synchronously
// inside the hook HTTP handler's call to IngestEvent, so every code path
// here must stay cheap — Notifier.Notify itself already fans out
// asynchronously (see internal/sinks), which is what keeps this fast.
func (w *Watcher) HandleHookEvent(sess *store.Session, event string, body []byte, state string, changed bool) {
	switch event {
	case agentevents.EventPreCompact:
		w.handlePreCompact(sess, body)
		return
	case agentevents.EventPermissionRequest:
		// internal/broker's session-approval flow (docs/agent-events.md
		// section 3, "Session permission mode") already sends a dedicated,
		// actionable push for this exact tool call — Approve/Deny buttons,
		// the tool name and its input summary. A second, text-only
		// "needs permission" push for the same event would just double-ping
		// the phone for one decision, so it is deliberately skipped here.
		// Claude's own Notification(permission_prompt) hook — the signal a
		// session gets when no PermissionRequest hook is registered, i.e.
		// outside "ask" mode — still reaches the generic case below.
		return
	}
	if !changed || state == "" {
		return
	}
	switch state {
	case agentevents.StateWaitingPermission:
		w.fire(sess, KeyWaitingPermission, "waiting_permission", "Needs permission",
			fmt.Sprintf("%s needs permission: %s", label(sess), notificationHint(body)))
	case agentevents.StateWaitingInput:
		w.fire(sess, KeyWaitingInput, "waiting_input", "Waiting for you",
			fmt.Sprintf("%s is waiting for you", label(sess)))
	case agentevents.StateIdle:
		w.fire(sess, KeyIdle, "idle", "Finished",
			doneMessage(sess, body))
	case agentevents.StateError:
		w.fire(sess, KeyError, "error", "Error",
			fmt.Sprintf("%s hit an error", label(sess)))
	}
}

func (w *Watcher) handlePreCompact(sess *store.Session, body []byte) {
	var probe struct {
		Trigger string `json:"trigger"`
	}
	_ = json.Unmarshal(body, &probe)
	if probe.Trigger != "auto" {
		return
	}
	w.fire(sess, KeyCompacting, "compacting", "Compacting",
		fmt.Sprintf("%s is compacting its context", label(sess)))
}

// fire applies the toggle, suppression and cooldown rules and, if all three
// clear, sends exactly one push.
func (w *Watcher) fire(sess *store.Session, settingKey, kind, title, body string) {
	if !w.enabled(settingKey) {
		return
	}
	if w.Activity != nil && w.Activity.Recent(sess.ID, SuppressWindow) {
		return
	}
	now := w.now()
	w.mu.Lock()
	if w.lastAlertAt == nil {
		w.lastAlertAt = map[int64]time.Time{}
	}
	if last, ok := w.lastAlertAt[sess.ID]; ok && now.Sub(last) < w.cooldown() {
		w.mu.Unlock()
		return
	}
	w.lastAlertAt[sess.ID] = now
	w.mu.Unlock()
	w.Notifier.Notify(title, body, fmt.Sprintf("/session/%d", sess.ID),
		&sinks.Extra{Kind: kind, SessionID: sess.ID})
}

// label is what a push names the session by — its own name, or a fallback
// that is still useful in a notification tray.
func label(sess *store.Session) string {
	if sess == nil || strings.TrimSpace(sess.Name) == "" {
		return "A session"
	}
	return sess.Name
}

// notificationHint pulls whatever a Claude Notification(permission_prompt)
// payload offers as a human-readable reason — its own "message" field, the
// only text Claude puts there; there is no discrete tool name on this event
// (that only exists on PermissionRequest, handled separately above).
func notificationHint(body []byte) string {
	var probe struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &probe)
	if hint := strings.TrimSpace(probe.Message); hint != "" {
		return clip(hint, 140)
	}
	return "review needed"
}

// doneMessage renders the working->idle push, with the Stop hook's own
// last_assistant_message as an excerpt when Claude reports one.
func doneMessage(sess *store.Session, body []byte) string {
	var probe struct {
		LastAssistantMessage string `json:"last_assistant_message"`
	}
	_ = json.Unmarshal(body, &probe)
	excerpt := strings.TrimSpace(probe.LastAssistantMessage)
	if excerpt == "" {
		return fmt.Sprintf("%s finished", label(sess))
	}
	return fmt.Sprintf("%s finished: %s", label(sess), clip(excerpt, 140))
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
