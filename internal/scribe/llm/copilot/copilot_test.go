package copilot

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// fakeCopilot serves the token exchange and one chat endpoint.
type fakeCopilot struct {
	mu        sync.Mutex
	tokenHits int
	tokenAuth string
	chatAuth  string
	chatInt   string
	expiresAt int64
	status    int // token endpoint status; 0 means 200
}

func (f *fakeCopilot) handler(t *testing.T) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/copilot_internal/v2/token":
			f.tokenHits++
			f.tokenAuth = r.Header.Get("Authorization")
			if f.status != 0 {
				w.WriteHeader(f.status)
				return
			}
			fmt.Fprintf(w, `{"token":"bearer-%d","expires_at":%d}`, f.tokenHits, f.expiresAt)
		case "/chat/completions":
			f.chatAuth = r.Header.Get("Authorization")
			f.chatInt = r.Header.Get("Copilot-Integration-Id")
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: [DONE]\n\n")
		default:
			http.NotFound(w, r)
		}
	})
}

func TestExchangeAndChatWithEditorHeaders(t *testing.T) {
	f := &fakeCopilot{expiresAt: time.Now().Add(time.Hour).Unix()}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)

	p := New(Options{OAuthToken: "oauth-1", TokenURL: srv.URL + "/copilot_internal/v2/token", BaseURL: srv.URL})
	for i := 0; i < 2; i++ {
		ch, err := p.Stream(context.Background(), llm.Request{Model: "gpt-4.1", Messages: []llm.Message{llm.UserText("hi")}})
		if err != nil {
			t.Fatalf("Stream %d: %v", i, err)
		}
		var text strings.Builder
		for ev := range ch {
			if ev.Type == llm.EvError {
				t.Fatalf("stream error: %v", ev.Err)
			}
			text.WriteString(ev.Text)
		}
		if text.String() != "hi" {
			t.Fatalf("text = %q", text.String())
		}
	}

	if f.tokenAuth != "token oauth-1" {
		t.Fatalf("exchange Authorization = %q", f.tokenAuth)
	}
	if f.tokenHits != 1 {
		t.Fatalf("token exchanges = %d, want 1 (cached between calls)", f.tokenHits)
	}
	if f.chatAuth != "Bearer bearer-1" {
		t.Fatalf("chat Authorization = %q, want the exchanged bearer", f.chatAuth)
	}
	if f.chatInt != "vscode-chat" {
		t.Fatalf("Copilot-Integration-Id = %q", f.chatInt)
	}
}

func TestTokenRenewedBeforeExpiry(t *testing.T) {
	f := &fakeCopilot{}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)

	clock := time.Date(2026, 10, 16, 9, 0, 0, 0, time.UTC)
	ts := NewTokenSource(Options{OAuthToken: "o", TokenURL: srv.URL + "/copilot_internal/v2/token", Now: func() time.Time { return clock }})
	f.expiresAt = clock.Add(5 * time.Minute).Unix()

	first, err := ts.Token(context.Background())
	if err != nil || first != "bearer-1" {
		t.Fatalf("first = %q, %v", first, err)
	}
	// Inside the one-minute margin before expiry, the token is renewed.
	clock = clock.Add(4*time.Minute + 30*time.Second)
	second, err := ts.Token(context.Background())
	if err != nil || second != "bearer-2" {
		t.Fatalf("second = %q, %v; want a renewed token", second, err)
	}
}

func TestRefusedExchangeNamesTheKey(t *testing.T) {
	f := &fakeCopilot{status: http.StatusUnauthorized}
	srv := httptest.NewServer(f.handler(t))
	t.Cleanup(srv.Close)
	ts := NewTokenSource(Options{OAuthToken: "bad", TokenURL: srv.URL + "/copilot_internal/v2/token"})
	_, err := ts.Token(context.Background())
	if err == nil || !strings.Contains(err.Error(), "COPILOT_API_KEY") {
		t.Fatalf("err = %v, want a message naming COPILOT_API_KEY", err)
	}
}
