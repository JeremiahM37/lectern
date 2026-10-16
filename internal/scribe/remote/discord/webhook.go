// Package discord is Scribe's Discord surface, and it is notify-only. An
// incoming webhook can post messages but cannot receive replies or button
// clicks, so questions, approvals and plan reviews stay on the host. A bot
// with the gateway and interactions would be needed for answers. That is not
// built here.
package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/remote"
)

// maxContent is Discord's limit on webhook message content.
const maxContent = 2000

// userAgent is set explicitly: Discord's edge rejects Go's default agent.
const userAgent = "scribe-discord-webhook/1"

// Notifier posts Scribe's notes to one Discord channel through its webhook.
type Notifier struct {
	url      string
	http     *http.Client
	throttle *remote.Throttle
	done     *remote.Throttle
	logf     func(string, ...any)
}

// New returns a Notifier for webhookURL. The URL must be https. Plain http is
// accepted only for a loopback host, so tests can use a local server. Progress
// lines go out at most once per progressEvery.
func New(webhookURL string, progressEvery time.Duration, logf func(string, ...any)) (*Notifier, error) {
	u, err := url.Parse(strings.TrimSpace(webhookURL))
	if err != nil || u.Host == "" {
		return nil, errors.New("discord: webhook URL is not a valid URL")
	}
	host := u.Hostname()
	loopback := host == "localhost" || (net.ParseIP(host) != nil && net.ParseIP(host).IsLoopback())
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback) {
		return nil, errors.New("discord: webhook URL must be https")
	}
	if progressEvery <= 0 {
		progressEvery = 20 * time.Second
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Notifier{
		url:      u.String(),
		http:     &http.Client{Timeout: 20 * time.Second},
		throttle: remote.NewThrottle(progressEvery),
		done:     remote.NewThrottle(2 * time.Second),
		logf:     logf,
	}, nil
}

// Post sends text, split to Discord's limit. Mentions are suppressed, so a
// model's output cannot ping @everyone.
func (n *Notifier) Post(ctx context.Context, text string) error {
	for _, part := range remote.Chunk(text, maxContent) {
		if err := n.postOne(ctx, part); err != nil {
			return err
		}
	}
	return nil
}

// postOne sends one message. A 429 is retried once after the wait Discord
// asks for.
func (n *Notifier) postOne(ctx context.Context, content string) error {
	raw, err := json.Marshal(map[string]any{
		"content":          content,
		"allowed_mentions": map[string]any{"parse": []string{}},
	})
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.url, bytes.NewReader(raw))
		if err != nil {
			return errors.New("discord webhook: bad request")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		resp, err := n.http.Do(req)
		if err != nil {
			// The error text would carry the webhook URL, and the URL is the
			// secret, so only a generic message is returned.
			return errors.New("discord webhook: request failed")
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		if resp.StatusCode/100 == 2 {
			return nil
		}
		if resp.StatusCode == http.StatusTooManyRequests && attempt == 0 {
			if !sleepCtx(ctx, retryWait(resp, body)) {
				return ctx.Err()
			}
			continue
		}
		return fmt.Errorf("discord webhook: HTTP %d", resp.StatusCode)
	}
}

// retryWait reads the wait from the body's retry_after (seconds, may be
// fractional) or the Retry-After header. The default is one second.
func retryWait(resp *http.Response, body []byte) time.Duration {
	var b struct {
		RetryAfter float64 `json:"retry_after"`
	}
	if json.Unmarshal(body, &b) == nil && b.RetryAfter > 0 {
		return time.Duration(b.RetryAfter * float64(time.Second))
	}
	if s := resp.Header.Get("Retry-After"); s != "" {
		if secs, err := strconv.ParseFloat(s, 64); err == nil && secs > 0 {
			return time.Duration(secs * float64(time.Second))
		}
	}
	return time.Second
}

// Progress posts a progress line, at most once per interval. It never
// blocks the caller.
func (n *Notifier) Progress(text string) {
	if n.throttle.Allow("progress") {
		n.background(text)
	}
}

// OnEvent is an agent sink. It posts a note when a main-agent turn ends or
// fails. Subagent activity is not sent.
func (n *Notifier) OnEvent(e agent.Event) {
	if e.AgentID != "" || e.Kind != agent.EvTurnDone || !n.done.Allow("done") {
		return
	}
	if e.Stats != nil && e.Stats.Error != "" && !e.Stats.Aborted {
		n.background("Scribe stopped with an error: " + clip(e.Stats.Error, 500))
		return
	}
	text := "Scribe finished this turn."
	if t := strings.TrimSpace(e.Text); t != "" {
		text += "\n\n" + clip(t, 1500)
	}
	n.background(text)
}

func (n *Notifier) background(text string) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := n.Post(ctx, text); err != nil {
			n.logf("discord: %v", err)
		}
	}()
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return remote.Chunk(s, n)[0] + "…"
}
