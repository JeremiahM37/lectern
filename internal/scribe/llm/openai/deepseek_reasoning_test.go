package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// The reasoner models must never get reasoning_content back (spec 7.2). The
// V4 thinking models must get it back on tool-call turns, because DeepSeek
// rejects a tool-loop request without it (400).
func TestDeepSeekReasonerNeverEchoesReasoning(t *testing.T) {
	for _, model := range []string{"deepseek-reasoner", "deepseek-r1", "deepseek-r1-0528"} {
		body := requestBody(t, "deepseek", llm.Request{Model: model, Messages: reasoningTurns()})
		if got := echoed(t, body); len(got) != 0 {
			t.Errorf("%s echoed reasoning_content: %v", model, got)
		}
	}
}

func TestDeepSeekV4ToolLoopKeepsReasoning(t *testing.T) {
	body := requestBody(t, "deepseek", llm.Request{Model: "deepseek-v4-pro", Messages: reasoningTurns()})
	got := echoed(t, body)
	if got[1] != "r1" || got[4] != "r2" || len(got) != 2 {
		t.Fatalf("v4 tool-call turns echoed %v, want r1 at 1 and r2 at 4", got)
	}
}

// A gateway id such as "deepseek/deepseek-reasoner" is not the DeepSeek
// provider's own reasoner; the provider check already keeps it out, and the
// strip rule matches the last path segment only.
func TestDeepSeekStripRuleMatchesLastSegment(t *testing.T) {
	if !deepSeekDropsReasoning("deepseek/deepseek-reasoner") {
		t.Fatal("reasoner behind a path prefix should be stripped")
	}
	if deepSeekDropsReasoning("deepseek-v4-pro") || deepSeekDropsReasoning("deepseek-chat") {
		t.Fatal("non-reasoner ids must keep reasoning_content on tool turns")
	}
}

func TestReasoningNeverEchoedOutsideDeepSeek(t *testing.T) {
	for _, id := range []string{"openai", "openrouter", "ollama"} {
		body := requestBody(t, id, llm.Request{Model: "deepseek-v4-pro", Messages: reasoningTurns()})
		if got := echoed(t, body); len(got) != 0 {
			t.Errorf("provider %s echoed reasoning_content: %v", id, got)
		}
	}
}

func TestStaticModelsCarryConfiguredContextWindow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	p := New(Options{
		ID:             "x",
		BaseURL:        srv.URL,
		Models:         []string{"big", "plain"},
		ContextWindows: map[string]int{"big": 262144, "ghost": 1},
	})
	ms, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 2 {
		t.Fatalf("models = %+v", ms)
	}
	if ms[0].ID != "big" || ms[0].ContextWindow != 262144 {
		t.Errorf("big = %+v, want window 262144", ms[0])
	}
	if ms[1].ID != "plain" || ms[1].ContextWindow != 0 {
		t.Errorf("plain = %+v, want no window from this package", ms[1])
	}
}

func TestNoModelListStaticModelsCarryContextWindow(t *testing.T) {
	p := New(Options{
		ID:             "x",
		NoModelList:    true,
		Models:         []string{"m1"},
		ContextWindows: map[string]int{"m1": 32768},
	})
	ms, err := p.Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ms) != 1 || ms[0].ContextWindow != 32768 {
		t.Fatalf("models = %+v, want m1 with window 32768", ms)
	}
}
