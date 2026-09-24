// Package sinks fans notifications out to web-push, a Discord webhook and ntfy,
// best-effort and never blocking the caller.
//
// ntfy approval notifications carry real approve/deny HTTP action buttons, so a
// phone can decide without opening the app (and without VAPID setup).
package sinks

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/push"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// Keys are the settings rows this package owns. Anything else is rejected by the
// settings endpoint so a typo cannot silently disable notifications.
//
// The alert_* keys and session_permission_mode belong conceptually to
// internal/alerts and internal/sessions respectively (docs/agent-events.md
// section 3), but PUT/GET /api/settings only validates against this one
// list (internal/api/misc.go), so they are appended here rather than
// standing up a second settings registry for two features. Values are
// "0"/"1" strings for the toggles (missing or anything but "0" means ON —
// see internal/alerts.Enabled) and "bypass"/"ask" for the permission mode
// default (missing or anything but "ask" means "bypass").
var Keys = []string{
	"discord_webhook", "ntfy_server", "ntfy_topic", "summary_agent", "summary_model",
	"alert_waiting_permission", "alert_waiting_input", "alert_idle", "alert_error", "alert_compacting",
	"session_permission_mode",
	"judge_agent", "judge_model", "eval_concurrency",
	// Cross-agent awareness (docs/agent-events.md "Cross-agent awareness"):
	// belongs conceptually to internal/awareness, appended here for the same
	// reason the alert_* keys above are — one settings registry, not two.
	// Same "default ON, off only when explicitly '0'" convention.
	"awareness_briefing", "awareness_edit_warning",
}

// Payload is one outbound notification, already addressed and rendered.
type Payload struct {
	Kind string         `json:"kind"`
	URL  string         `json:"url"`
	Body map[string]any `json:"body"`
}

// Extra carries the notification's context — which is what turns an ntfy message
// into one with decision buttons, and what a web-push payload's kind/session_id
// fields (docs/agent-events.md section 3) come from.
type Extra struct {
	Kind       string
	ApprovalID int64
	// SessionID, when set, is included in the web-push payload as
	// session_id so the service worker (and anything else reading a push
	// message) can act on the session directly rather than parsing it back
	// out of the deep-link URL.
	SessionID int64
}

// Notifier owns the sink fan-out.
type Notifier struct {
	DB      *store.DB
	BaseURL string
	Push    *push.Sender
	Log     *slog.Logger
	Client  *http.Client

	// Hook, when set, replaces real delivery. Tests assert on the built payloads
	// without any network at all — the same seam the Python suite used.
	Hook func([]Payload)

	wg sync.WaitGroup
}

// Settings reads the configured sinks.
func (n *Notifier) Settings() map[string]string {
	out := map[string]string{}
	for _, k := range Keys {
		out[k] = n.DB.Setting(k)
	}
	return out
}

// BuildPayloads is the pure builder: one payload per configured sink.
func BuildPayloads(cfg map[string]string, baseURL, title, body, urlPath string, extra *Extra) []Payload {
	out := []Payload{}
	click := strings.TrimRight(baseURL, "/") + urlPath
	if hook := cfg["discord_webhook"]; hook != "" {
		content := "**" + title + "** — " + body
		if len(content) > 1900 {
			content = content[:1900]
		}
		out = append(out, Payload{"discord", hook, map[string]any{"content": content}})
	}
	if server, topic := cfg["ntfy_server"], cfg["ntfy_topic"]; server != "" && topic != "" {
		msg := map[string]any{
			"topic": topic, "title": "lectern: " + title,
			"message": clip(body, 800), "click": click,
		}
		if extra != nil && extra.Kind == "approval" {
			decisionURL := strings.TrimRight(baseURL, "/") +
				"/api/approvals/" + strconv.FormatInt(extra.ApprovalID, 10) + "/decision"
			headers := map[string]any{"Content-Type": "application/json"}
			msg["priority"] = 4
			msg["actions"] = []any{
				map[string]any{"action": "http", "label": "✅ Approve", "url": decisionURL,
					"method": "POST", "headers": headers,
					"body": `{"decision":"approved"}`},
				map[string]any{"action": "http", "label": "⛔ Deny", "url": decisionURL,
					"method": "POST", "headers": headers,
					"body": `{"decision":"denied","note":"denied from ntfy"}`},
			}
		}
		out = append(out, Payload{"ntfy", strings.TrimRight(server, "/"), msg})
	}
	return out
}

// Notify sends to every configured sink. Delivery happens on a goroutine: a sink
// outage must never slow a dispatch down, let alone fail one.
func (n *Notifier) Notify(title, body, urlPath string, extra *Extra) {
	if urlPath == "" {
		urlPath = "/"
	}
	n.sendPush(title, body, urlPath, extra)
	payloads := BuildPayloads(n.Settings(), n.BaseURL, title, body, urlPath, extra)
	if len(payloads) == 0 {
		return
	}
	if n.Hook != nil {
		n.Hook(payloads)
		return
	}
	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		n.deliver(payloads)
	}()
}

// Wait blocks until in-flight deliveries finish — used at shutdown.
func (n *Notifier) Wait() { n.wg.Wait() }

func (n *Notifier) deliver(payloads []Payload) {
	client := n.Client
	if client == nil {
		client = &http.Client{Timeout: 6 * time.Second}
	}
	for _, p := range payloads {
		raw, _ := json.Marshal(p.Body)
		req, err := http.NewRequest("POST", p.URL, bytes.NewReader(raw))
		if err != nil {
			n.Log.Warn("sink request build failed", "sink", p.Kind, "err", err)
			continue
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			n.Log.Warn("sink failed", "sink", p.Kind, "err", err)
			continue
		}
		resp.Body.Close()
	}
}

// pushMessage builds the JSON a browser's service worker receives (before
// web-push encryption), pulled out of sendPush so its shape — kind, the
// deep-link url, and (docs/agent-events.md section 3) session_id — is
// testable without a real VAPID/encryption round trip.
func pushMessage(title, body, urlPath string, extra *Extra) map[string]any {
	msg := map[string]any{"title": title, "body": body, "url": urlPath}
	if extra != nil {
		msg["kind"] = extra.Kind
		if extra.ApprovalID != 0 {
			msg["approval_id"] = extra.ApprovalID
		}
		if extra.SessionID != 0 {
			msg["session_id"] = extra.SessionID
		}
	}
	return msg
}

func (n *Notifier) sendPush(title, body, urlPath string, extra *Extra) {
	if n.Push == nil || !n.Push.Enabled() {
		return
	}
	raw, _ := json.Marshal(pushMessage(title, body, urlPath, extra))
	rows, err := n.DB.Query(`SELECT id, endpoint, keys_json FROM push_subscriptions`)
	if err != nil {
		return
	}
	type sub struct {
		id int64
		s  push.Subscription
	}
	var subs []sub
	for rows.Next() {
		var id int64
		var endpoint, keysJSON string
		if err := rows.Scan(&id, &endpoint, &keysJSON); err != nil {
			continue
		}
		keys := map[string]string{}
		json.Unmarshal([]byte(keysJSON), &keys)
		subs = append(subs, sub{id, push.Subscription{Endpoint: endpoint, Keys: keys}})
	}
	rows.Close()

	n.wg.Add(1)
	go func() {
		defer n.wg.Done()
		for _, s := range subs {
			gone, err := n.Push.Send(s.s, raw)
			if gone {
				n.DB.Exec(`DELETE FROM push_subscriptions WHERE id=?`, s.id)
			} else if err != nil {
				n.Log.Warn("push failed", "err", err)
			}
		}
	}()
}

// Subscribe records (or refreshes) a browser push subscription.
func Subscribe(db *store.DB, sub push.Subscription) error {
	keys, _ := json.Marshal(sub.Keys)
	_, err := db.Exec(`INSERT INTO push_subscriptions(endpoint, keys_json, created_at)
		VALUES(?,?,?) ON CONFLICT(endpoint) DO UPDATE SET keys_json=excluded.keys_json`,
		sub.Endpoint, string(keys), store.Now())
	return err
}

// Unsubscribe removes one browser's push subscription by endpoint — the same
// identity a 404/410 delivery failure prunes automatically (sendPush above).
// It never errors on an endpoint that is already gone: asking to unsubscribe
// a device that was never (or is no longer) subscribed is not a failure.
func Unsubscribe(db *store.DB, endpoint string) error {
	_, err := db.Exec(`DELETE FROM push_subscriptions WHERE endpoint=?`, endpoint)
	return err
}

// SubscriptionInfo is one subscribed device, as listed for the settings UI.
// The endpoint is included so the UI can point out "this device" (it matches
// the endpoint the browser's own pushManager.getSubscription() returns) — it
// is not secret, unlike the p256dh/auth keys, which this never exposes.
type SubscriptionInfo struct {
	ID        int64   `json:"id"`
	Endpoint  string  `json:"endpoint"`
	CreatedAt float64 `json:"created_at"`
}

// Subscriptions lists every subscribed device, newest first.
func Subscriptions(db *store.DB) ([]SubscriptionInfo, error) {
	rows, err := db.Query(`SELECT id, endpoint, created_at FROM push_subscriptions ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SubscriptionInfo{}
	for rows.Next() {
		var s SubscriptionInfo
		var createdAt *float64
		if err := rows.Scan(&s.ID, &s.Endpoint, &createdAt); err != nil {
			return nil, err
		}
		if createdAt != nil {
			s.CreatedAt = *createdAt
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
