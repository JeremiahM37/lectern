package discord

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type hook struct {
	mu      sync.Mutex
	bodies  []map[string]any
	agents  []string
	replies []int // status for each request, in order; default 204
}

func newHook(t *testing.T, statuses ...int) (*hook, *httptest.Server) {
	t.Helper()
	h := &hook{replies: statuses}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		h.mu.Lock()
		defer h.mu.Unlock()
		h.bodies = append(h.bodies, b)
		h.agents = append(h.agents, r.Header.Get("User-Agent"))
		status := http.StatusNoContent
		if len(h.bodies) <= len(h.replies) {
			status = h.replies[len(h.bodies)-1]
		}
		if status == http.StatusTooManyRequests {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = io.WriteString(w, `{"message":"slow down","retry_after":0.01}`)
			return
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return h, srv
}

func (h *hook) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.bodies)
}

// Messages go out with the explicit user agent and with mentions suppressed.
func TestPostSendsContentWithoutMentions(t *testing.T) {
	h, srv := newHook(t)
	n, err := New(srv.URL, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := n.Post(context.Background(), "@everyone build is green"); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.bodies) != 1 {
		t.Fatalf("requests = %d", len(h.bodies))
	}
	if h.bodies[0]["content"] != "@everyone build is green" {
		t.Fatalf("content = %v", h.bodies[0]["content"])
	}
	am, _ := h.bodies[0]["allowed_mentions"].(map[string]any)
	if parse, ok := am["parse"].([]any); !ok || len(parse) != 0 {
		t.Fatalf("allowed_mentions = %v, want parse: []", am)
	}
	if !strings.HasPrefix(h.agents[0], "scribe-discord-webhook") {
		t.Fatalf("user agent = %q", h.agents[0])
	}
}

// A long note is split under the 2000 character limit, with no text lost.
func TestPostSplitsLongText(t *testing.T) {
	h, srv := newHook(t)
	n, _ := New(srv.URL, 0, nil)
	long := strings.Repeat("0123456789 ", 400) // about 4400 bytes
	if err := n.Post(context.Background(), long); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.bodies) < 3 {
		t.Fatalf("requests = %d, want at least 3", len(h.bodies))
	}
	var got strings.Builder
	for _, b := range h.bodies {
		c := b["content"].(string)
		if len(c) > maxContent {
			t.Fatalf("part of %d bytes exceeds %d", len(c), maxContent)
		}
		got.WriteString(c)
	}
	if strings.Join(strings.Fields(got.String()), " ") != strings.Join(strings.Fields(long), " ") {
		t.Fatal("split text lost content")
	}
}

// A rate-limited request is retried once after the wait Discord asks for.
func TestPostRetriesOnceAfter429(t *testing.T) {
	h, srv := newHook(t, http.StatusTooManyRequests)
	n, _ := New(srv.URL, 0, nil)
	if err := n.Post(context.Background(), "hello"); err != nil {
		t.Fatalf("retry after 429 failed: %v", err)
	}
	if h.count() != 2 {
		t.Fatalf("requests = %d, want 2 (one retry)", h.count())
	}
}

// The webhook URL is secret: a failure reports the status, never the URL.
func TestErrorDoesNotLeakWebhookURL(t *testing.T) {
	_, srv := newHook(t, http.StatusNotFound)
	n, _ := New(srv.URL+"/api/webhooks/SECRET-PART", 0, nil)
	err := n.Post(context.Background(), "hi")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Fatalf("error = %v, want the HTTP status", err)
	}
	if strings.Contains(err.Error(), "SECRET-PART") {
		t.Fatalf("error leaks the webhook URL: %v", err)
	}
}

// Plain http is refused except to a loopback host, and a non-URL is refused.
func TestNewRequiresHTTPSExceptLoopback(t *testing.T) {
	if _, err := New("http://discord.example.com/hook", 0, nil); err == nil {
		t.Fatal("plain http to a remote host was accepted")
	}
	if _, err := New("https://discord.com/api/webhooks/1/x", 0, nil); err != nil {
		t.Fatalf("https webhook refused: %v", err)
	}
	if _, err := New("not a url", 0, nil); err == nil {
		t.Fatal("garbage accepted")
	}
}

// Progress lines are throttled; a second one inside the interval is dropped.
func TestProgressIsThrottled(t *testing.T) {
	h, srv := newHook(t)
	n, _ := New(srv.URL, time.Hour, nil)
	n.Progress("first")
	n.Progress("second")
	deadline := time.Now().Add(3 * time.Second)
	for h.count() < 1 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(150 * time.Millisecond)
	if h.count() != 1 {
		t.Fatalf("requests = %d, want 1 after throttling", h.count())
	}
}
