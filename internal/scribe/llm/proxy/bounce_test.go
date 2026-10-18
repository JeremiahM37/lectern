package proxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// refusedAddr returns a loopback address with nothing listening on it, so a
// dial is refused.
func refusedAddr(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close()
	return addr
}

// serveOn starts a server on a fixed address, which is how a test brings a
// refused proxy back up.
func serveOn(t *testing.T, addr string, h http.Handler) {
	t.Helper()
	l, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("listen %s: %v", addr, err)
	}
	srv := &httptest.Server{Listener: l, Config: &http.Server{Handler: h}}
	srv.Start()
	t.Cleanup(srv.Close)
}

// chatOK answers the chat and model routes the way a healthy proxy does.
func chatOK() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	})
}

// claudeOK answers the Anthropic messages route with a short stream.
func claudeOK() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n")
		fmt.Fprint(w, "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"ok\"}}\n\n")
		fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
		fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
	})
}

func collect(t *testing.T, ch <-chan llm.Event) (string, error) {
	t.Helper()
	var text string
	var err error
	for ev := range ch {
		if ev.Type == llm.EvText {
			text += ev.Text
		}
		if ev.Type == llm.EvError {
			err = ev.Err
		}
	}
	return text, err
}

func TestBounceRecoversWhenProxyRefusesThenAccepts(t *testing.T) {
	addr := refusedAddr(t)
	var bounces atomic.Int32
	p := New(Options{
		BaseURL: "http://" + addr + "/v1",
		APIKey:  "k-1",
		Bounce: func(ctx context.Context) (Endpoint, error) {
			bounces.Add(1)
			serveOn(t, addr, chatOK())
			return Endpoint{BaseURL: "http://" + addr + "/v1", APIKey: "k-1"}, nil
		},
	})
	ch, err := p.Stream(context.Background(), llm.Request{Model: "gpt-5", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatalf("Stream after bounce: %v", err)
	}
	text, err := collect(t, ch)
	if err != nil || text != "ok" {
		t.Fatalf("text %q err %v, want ok", text, err)
	}
	if n := bounces.Load(); n != 1 {
		t.Fatalf("bounces = %d, want 1", n)
	}
}

func TestBounceRecoversOnClaudeRouteMidStream(t *testing.T) {
	addr := refusedAddr(t)
	var bounces atomic.Int32
	p := New(Options{
		BaseURL: "http://" + addr + "/v1",
		APIKey:  "k-1",
		Bounce: func(ctx context.Context) (Endpoint, error) {
			bounces.Add(1)
			serveOn(t, addr, claudeOK())
			return Endpoint{BaseURL: "http://" + addr + "/v1", APIKey: "k-1"}, nil
		},
	})
	// The Anthropic route reports a refused dial as the first event of the
	// stream, not as an error from Stream, so this covers the in-stream path.
	ch, err := p.Stream(context.Background(), llm.Request{Model: "claude-sonnet-4", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if _, err := collect(t, ch); err != nil {
		t.Fatalf("stream after bounce failed: %v", err)
	}
	if n := bounces.Load(); n != 1 {
		t.Fatalf("bounces = %d, want 1", n)
	}
}

func TestNoRestartHookReportsClearError(t *testing.T) {
	addr := refusedAddr(t)
	p := New(Options{BaseURL: "http://" + addr + "/v1", APIKey: "k-1"})
	_, err := p.Stream(context.Background(), llm.Request{Model: "gpt-5", Messages: []llm.Message{llm.UserText("hi")}})
	if err == nil {
		t.Fatal("want an error when the proxy is down and cannot be restarted")
	}
	for _, want := range []string{"not responding at", addr, "/proxy login"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func TestBounceAtMostOncePerFailingRun(t *testing.T) {
	old := bounceCap
	bounceCap = 500 * time.Millisecond
	t.Cleanup(func() { bounceCap = old })

	addr := refusedAddr(t)
	var bounces atomic.Int32
	p := New(Options{
		BaseURL: "http://" + addr + "/v1",
		APIKey:  "k-1",
		Bounce: func(ctx context.Context) (Endpoint, error) {
			bounces.Add(1)
			// The restart does not bring anything up.
			return Endpoint{BaseURL: "http://" + addr + "/v1", APIKey: "k-1"}, nil
		},
	})
	req := llm.Request{Model: "gpt-5", Messages: []llm.Message{llm.UserText("hi")}}
	for i := 0; i < 3; i++ {
		_, err := p.Stream(context.Background(), req)
		if err == nil {
			t.Fatalf("attempt %d: want an error, the proxy never came back", i)
		}
	}
	if n := bounces.Load(); n != 1 {
		t.Fatalf("bounces = %d over three failing calls, want 1", n)
	}
}

func TestBounceNeedsAuthReportsKey(t *testing.T) {
	addr := refusedAddr(t)
	p := New(Options{
		BaseURL: "http://" + addr + "/v1",
		APIKey:  "k-1",
		Bounce: func(ctx context.Context) (Endpoint, error) {
			serveOn(t, addr, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, "bad key", http.StatusUnauthorized)
			}))
			return Endpoint{BaseURL: "http://" + addr + "/v1", APIKey: "k-1"}, nil
		},
	})
	_, err := p.Stream(context.Background(), llm.Request{Model: "gpt-5", Messages: []llm.Message{llm.UserText("hi")}})
	if err == nil || !strings.Contains(err.Error(), "rejected the key") {
		t.Fatalf("err = %v, want a key rejection", err)
	}
}

func TestBounceCappedAtEightSeconds(t *testing.T) {
	old := bounceCap
	bounceCap = 300 * time.Millisecond
	t.Cleanup(func() { bounceCap = old })

	addr := refusedAddr(t)
	p := New(Options{
		BaseURL: "http://" + addr + "/v1",
		APIKey:  "k-1",
		Bounce: func(ctx context.Context) (Endpoint, error) {
			<-ctx.Done()
			return Endpoint{}, ctx.Err()
		},
	})
	start := time.Now()
	_, err := p.Stream(context.Background(), llm.Request{Model: "gpt-5", Messages: []llm.Message{llm.UserText("hi")}})
	if err == nil {
		t.Fatal("want an error when the restart hangs")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Fatalf("bounce took %v, cap is %v", elapsed, bounceCap)
	}
}

func TestDroppedConnectionRetriesWithoutBounceWhenProxyUp(t *testing.T) {
	var chatCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/chat/completions") && chatCalls.Add(1) == 1 {
			// Drop the connection with no response, as a proxy that restarts
			// mid-request would.
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		chatOK().ServeHTTP(w, req)
	}))
	t.Cleanup(srv.Close)

	var bounces atomic.Int32
	p := New(Options{
		BaseURL: srv.URL + "/v1",
		APIKey:  "k-1",
		Bounce: func(ctx context.Context) (Endpoint, error) {
			bounces.Add(1)
			return Endpoint{}, errors.New("should not restart a proxy that answers")
		},
	})
	ch, err := p.Stream(context.Background(), llm.Request{Model: "gpt-5", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	text, err := collect(t, ch)
	if err != nil || text != "ok" {
		t.Fatalf("text %q err %v, want the retried answer", text, err)
	}
	if n := bounces.Load(); n != 0 {
		t.Fatalf("bounces = %d, want 0 when the proxy answers its probe", n)
	}
}

func TestStatusErrorIsNotBounced(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)
	var bounces atomic.Int32
	p := New(Options{
		BaseURL: srv.URL + "/v1",
		APIKey:  "k-1",
		Bounce: func(ctx context.Context) (Endpoint, error) {
			bounces.Add(1)
			return Endpoint{}, nil
		},
	})
	if _, err := p.Stream(context.Background(), llm.Request{Model: "gpt-5", Messages: []llm.Message{llm.UserText("hi")}}); err == nil {
		t.Fatal("want the HTTP error")
	}
	if n := bounces.Load(); n != 0 {
		t.Fatalf("bounces = %d for an HTTP 500, want 0", n)
	}
}

func TestConnectionErrorClassifier(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"url error", &url.Error{Op: "Post", URL: "http://x", Err: errors.New("dial")}, true},
		{"refused", fmt.Errorf("wrapped: %w", syscall.ECONNREFUSED), true},
		{"unexpected eof", io.ErrUnexpectedEOF, true},
		{"openai unreachable text", errors.New("proxy: cannot reach http://x (dial tcp: refused); check"), true},
		{"status error", &llm.APIError{Provider: "proxy", StatusCode: 503, Message: "down"}, false},
		{"plain failure", errors.New("invalid json in tool args"), false},
	}
	for _, tc := range cases {
		if got := connectionError(tc.err); got != tc.want {
			t.Errorf("%s: connectionError = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestBounceNotTriggeredByCancelledCaller(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if bounceable(ctx, &url.Error{Op: "Post", URL: "http://x", Err: errors.New("dial")}) {
		t.Fatal("a cancelled caller must not cause a bounce")
	}
}
