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

	"github.com/JeremiahM37/lectern/internal/push"
	"github.com/JeremiahM37/lectern/internal/store"
)

// Keys are the settings rows this package owns. Anything else is rejected by the
// settings endpoint so a typo cannot silently disable notifications.
var Keys = []string{"discord_webhook", "ntfy_server", "ntfy_topic"}

// Payload is one outbound notification, already addressed and rendered.
type Payload struct {
	Kind string         `json:"kind"`
	URL  string         `json:"url"`
	Body map[string]any `json:"body"`
}

// Extra carries the notification's context — which is what turns an ntfy message
// into one with decision buttons.
type Extra struct {
	Kind       string
	ApprovalID int64
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

func (n *Notifier) sendPush(title, body, urlPath string, extra *Extra) {
	if n.Push == nil || !n.Push.Enabled() {
		return
	}
	msg := map[string]any{"title": title, "body": body, "url": urlPath}
	if extra != nil {
		msg["kind"] = extra.Kind
		msg["approval_id"] = extra.ApprovalID
	}
	raw, _ := json.Marshal(msg)
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

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
