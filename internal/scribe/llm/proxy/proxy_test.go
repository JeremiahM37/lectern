package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// recorder keeps the last request the fake proxy saw.
type recorder struct {
	mu   sync.Mutex
	path string
	auth string
	body map[string]any
}

func (r *recorder) serve(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		b, _ := io.ReadAll(req.Body)
		var m map[string]any
		_ = json.Unmarshal(b, &m)
		r.mu.Lock()
		r.path, r.auth, r.body = req.URL.Path, req.Header.Get("Authorization"), m
		r.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasPrefix(req.URL.Path, "/v1/messages") {
			fmt.Fprint(w, "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":1}}}\n\n")
			fmt.Fprint(w, "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n")
			fmt.Fprint(w, "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")
			return
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

func drain(t *testing.T, ch <-chan llm.Event) {
	t.Helper()
	for ev := range ch {
		if ev.Type == llm.EvError {
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
}

func TestClaudeGoesToAnthropicRouteWithSystemCloaked(t *testing.T) {
	rec := &recorder{}
	srv := rec.serve(t)
	p := New(Options{BaseURL: srv.URL + "/v1", APIKey: "k-1"})

	msgs := []llm.Message{llm.UserText("first"), llm.AssistantText("ok")}
	ch, err := p.Stream(context.Background(), llm.Request{Model: "claude-sonnet-5-5", System: "SCRIBE RULES", Messages: msgs})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)

	if rec.path != "/v1/messages" {
		t.Fatalf("path = %q, want /v1/messages", rec.path)
	}
	if rec.auth != "" && rec.auth != "Bearer k-1" {
		t.Fatalf("auth = %q", rec.auth)
	}
	if _, has := rec.body["system"]; has {
		t.Fatalf("system field must be cloaked away: %v", rec.body["system"])
	}
	first := rec.body["messages"].([]any)[0].(map[string]any)
	content := first["content"].([]any)[0].(map[string]any)
	if first["role"] != "user" || content["text"] != "SCRIBE RULES" {
		t.Fatalf("first message = %v, want the instructions as user text", first)
	}
	if len(msgs) != 2 {
		t.Fatalf("caller's messages were modified")
	}
}

func TestOtherModelsUseChatCompletions(t *testing.T) {
	rec := &recorder{}
	srv := rec.serve(t)
	p := New(Options{BaseURL: srv.URL + "/v1"})
	ch, err := p.Stream(context.Background(), llm.Request{Model: "qwen3-coder", System: "SYS", Messages: []llm.Message{llm.UserText("hi")}})
	if err != nil {
		t.Fatal(err)
	}
	drain(t, ch)
	if rec.path != "/v1/chat/completions" {
		t.Fatalf("path = %q", rec.path)
	}
	msgs := rec.body["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Fatalf("chat route keeps the system prompt: %v", msgs[0])
	}
}

func TestIsClaudeOnlyForPrefix(t *testing.T) {
	if !IsClaude("claude-haiku-4-5") || !IsClaude("Claude-x") {
		t.Fatal("claude ids must route to Anthropic")
	}
	if IsClaude("openrouter/anthropic/claude-x") {
		t.Fatal("gateway-prefixed ids use chat completions")
	}
}

func TestProbeStates(t *testing.T) {
	cases := []struct {
		status int
		want   State
	}{
		{http.StatusOK, Running},
		{http.StatusUnauthorized, NeedsAuth},
		{http.StatusForbidden, NeedsAuth},
		{http.StatusInternalServerError, Unreachable},
	}
	for _, c := range cases {
		var gotAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			w.WriteHeader(c.status)
		}))
		if got := Probe(context.Background(), srv.Client(), srv.URL+"/v1", "key-1"); got != c.want {
			t.Errorf("status %d: state = %s, want %s", c.status, got, c.want)
		}
		if gotAuth != "Bearer key-1" {
			t.Errorf("status %d: Authorization = %q", c.status, gotAuth)
		}
		srv.Close()
	}

	srv := httptest.NewServer(http.NotFoundHandler())
	base := srv.URL + "/v1"
	srv.Close()
	if got := Probe(context.Background(), nil, base, ""); got != Unreachable {
		t.Fatalf("closed server: state = %s", got)
	}
}

func TestUsableKeyRejectsPlaceholdersAndHashes(t *testing.T) {
	bad := []string{"", "  ", "your-key", "CHANGEME", "replace-me", "xxx", "$2b$12$abcdefghijklmnopqrstuv", strings.Repeat("ab", 32)}
	for _, k := range bad {
		if UsableKey(k) {
			t.Errorf("%q should be rejected", k)
		}
	}
	for _, k := range []string{"sk-live-123", strings.Repeat("ab", 31) + "zz"} {
		if !UsableKey(k) {
			t.Errorf("%q should be usable", k)
		}
	}
}

func TestCandidateKeysEnvFirstAndDeduplicated(t *testing.T) {
	got := CandidateKeys(" env-key ", "changeme", "env-key", "other")
	if len(got) != 2 || got[0] != "env-key" || got[1] != "other" {
		t.Fatalf("candidates = %v", got)
	}
	if got := CandidateKeys("", "changeme"); len(got) != 0 {
		t.Fatalf("no usable keys: %v", got)
	}
}
