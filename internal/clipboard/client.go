package clipboard

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client is a device's side of the bridge: it holds a connection open to the
// server, answers the server's clipboard requests from its own clipboard, says
// when its person is typing, and (optionally) mirrors the clipboard to the
// session's machine as it changes.
type Client struct {
	Base, Token string
	HTTP        *http.Client // no overall timeout: the listen stream is long-lived
	ID          string
	Session     int64
	Kind        string // "cli", "bridge", "android", ...
	// Always marks a bridge whose person cannot be seen typing (plain SSH).
	Always bool
	Reader Reader
	// MirrorText also mirrors the text clipboard (off by default: copying
	// text to a server is a privacy choice).
	MirrorText bool
	// Log receives short diagnostics; may be nil.
	Log func(format string, args ...any)

	mu         sync.Mutex
	lastActive time.Time
	lastMirror string
}

// NewClientID returns a random, stable-for-this-process client id.
func NewClientID(kind string) string {
	var b [6]byte
	rand.Read(b[:])
	return kind + "-" + hex.EncodeToString(b[:])
}

func (c *Client) logf(f string, a ...any) {
	if c.Log != nil {
		c.Log(f, a...)
	}
}

func (c *Client) http() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{}
}

func (c *Client) req(ctx context.Context, method, path string, q url.Values, body io.Reader) (*http.Request, error) {
	u := strings.TrimRight(c.Base, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	r, err := http.NewRequestWithContext(ctx, method, u, body)
	if err != nil {
		return nil, err
	}
	if c.Token != "" {
		r.Header.Set("Authorization", "Bearer "+c.Token)
	}
	return r, nil
}

func (c *Client) query() url.Values {
	q := url.Values{"client": {c.ID}}
	return q
}

// Serve keeps the listen stream open until ctx ends, reconnecting with a
// short back-off.
func (c *Client) Serve(ctx context.Context) {
	delay := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := c.listen(ctx)
		if ctx.Err() != nil {
			return
		}
		c.logf("clipboard stream ended: %v", err)
		if time.Since(start) > 30*time.Second {
			delay = time.Second
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		if delay < 15*time.Second {
			delay *= 2
		}
	}
}

func (c *Client) listen(ctx context.Context) error {
	q := c.query()
	q.Set("kind", c.Kind)
	if c.Session > 0 {
		q.Set("session", fmt.Sprint(c.Session))
	}
	if c.Always {
		q.Set("always", "1")
	}
	r, err := c.req(ctx, "GET", "/api/clipboard/listen", q, nil)
	if err != nil {
		return err
	}
	resp, err := c.http().Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("server answered %s", resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: ") && event == "request":
			var req Request
			if json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &req) == nil {
				go c.answer(ctx, req)
			}
		case line == "":
			event = ""
		}
	}
	return sc.Err()
}

func (c *Client) answer(ctx context.Context, req Request) {
	ctx, cancel := context.WithTimeout(ctx, 2500*time.Millisecond)
	defer cancel()
	h := http.Header{}
	var body []byte
	switch req.Op {
	case OpList:
		types, err := c.Reader.Types(ctx)
		if err != nil || len(types) == 0 {
			h.Set("X-Clipboard-Status", "unavailable")
		} else {
			h.Set("X-Clipboard-Types", strings.Join(types, ","))
			body = []byte(strings.Join(types, "\n"))
		}
	case OpRead:
		data, err := c.Reader.Read(ctx, req.Type)
		if err != nil || len(data) == 0 || len(data) > DefaultMaxBytes {
			h.Set("X-Clipboard-Status", "unavailable")
		} else {
			h.Set("Content-Type", req.Type)
			body = data
		}
	default:
		h.Set("X-Clipboard-Status", "unavailable")
	}
	r, err := c.req(ctx, "POST", "/api/clipboard/respond/"+url.PathEscape(req.ID), c.query(), bytes.NewReader(body))
	if err != nil {
		return
	}
	for k, v := range h {
		r.Header[k] = v
	}
	if resp, err := c.http().Do(r); err == nil {
		resp.Body.Close()
	}
}

// Active tells the server the person just typed (throttled).
func (c *Client) Active() {
	c.mu.Lock()
	if time.Since(c.lastActive) < 2*time.Second {
		c.mu.Unlock()
		return
	}
	c.lastActive = time.Now()
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r, err := c.req(ctx, "POST", "/api/clipboard/active", c.query(), nil)
	if err != nil {
		return
	}
	if resp, err := c.http().Do(r); err == nil {
		resp.Body.Close()
	}
}

// Mirror sends one item to the session's machine.
func (c *Client) Mirror(ctx context.Context, mime string, data []byte) error {
	q := url.Values{"session": {fmt.Sprint(c.Session)}}
	r, err := c.req(ctx, "PUT", "/api/clipboard/mirror", q, bytes.NewReader(data))
	if err != nil {
		return err
	}
	r.Header.Set("Content-Type", mime)
	resp, err := c.http().Do(r)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// SyncOnce mirrors the current clipboard if it changed since the last sync
// (an image; text only when MirrorText is set). force sends it regardless.
func (c *Client) SyncOnce(ctx context.Context, force bool) {
	if c.Session <= 0 || c.Reader == nil {
		return
	}
	types, err := c.Reader.Types(ctx)
	if err != nil || len(types) == 0 {
		return
	}
	want := ""
	for _, t := range types {
		if IsImage(t) {
			want = t
			break
		}
	}
	if want == "" && c.MirrorText {
		want = "text/plain"
	}
	if want == "" {
		return
	}
	data, err := c.Reader.Read(ctx, want)
	if err != nil || len(data) == 0 || len(data) > DefaultMaxBytes {
		return
	}
	sum := sha256.Sum256(data)
	sig := want + hex.EncodeToString(sum[:])
	c.mu.Lock()
	same := sig == c.lastMirror
	c.mu.Unlock()
	if same && !force {
		return
	}
	if err := c.Mirror(ctx, want, data); err != nil {
		c.logf("clipboard mirror: %v", err)
		return
	}
	c.mu.Lock()
	c.lastMirror = sig
	c.mu.Unlock()
}

// Watch mirrors the clipboard when it changes: once at attach, then on a poll.
func (c *Client) Watch(ctx context.Context, every time.Duration) {
	c.SyncOnce(ctx, true)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.SyncOnce(ctx, false)
		}
	}
}
