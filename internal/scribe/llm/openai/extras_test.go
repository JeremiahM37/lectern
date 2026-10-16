package openai

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// streamText collects the text and reasoning a stream produced.
func streamText(t *testing.T, p *Provider, req llm.Request) (text, reason string) {
	t.Helper()
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var tb, rb strings.Builder
	for ev := range ch {
		switch ev.Type {
		case llm.EvText:
			tb.WriteString(ev.Text)
		case llm.EvReasoning:
			rb.WriteString(ev.Text)
		case llm.EvError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	return tb.String(), rb.String()
}

func TestThinkingTagVariants(t *testing.T) {
	var sp splitter
	var got []piece
	for _, s := range []string{"<think>a</think>b<thin", "king>c</thinking>d"} {
		got = append(got, sp.push(s)...)
	}
	got = append(got, sp.flush()...)
	var text, reason strings.Builder
	for _, p := range got {
		if p.reasoning {
			reason.WriteString(p.text)
		} else {
			text.WriteString(p.text)
		}
	}
	if text.String() != "bd" || reason.String() != "ac" {
		t.Fatalf("text=%q reason=%q, want bd / ac", text.String(), reason.String())
	}
}

func TestClosingTagMustMatchItsOpener(t *testing.T) {
	var sp splitter
	got := sp.push("<thinking>x</think>y</thinking>z")
	got = append(got, sp.flush()...)
	var text, reason strings.Builder
	for _, p := range got {
		if p.reasoning {
			reason.WriteString(p.text)
		} else {
			text.WriteString(p.text)
		}
	}
	if reason.String() != "x</think>y" || text.String() != "z" {
		t.Fatalf("text=%q reason=%q", text.String(), reason.String())
	}
}

func TestNativeFamiliesKeepLiteralTags(t *testing.T) {
	native := []string{"claude-sonnet-5-5", "anthropic/claude-x", "gpt-5.4", "o3-mini", "o1", "gemini-2.5-flash", "grok-4", "deepseek-reasoner"}
	for _, m := range native {
		if extractsThinkTags(m) {
			t.Errorf("%s should not be wrapped", m)
		}
	}
	for _, m := range []string{"qwen3-coder", "openrouter/qwen/qwen3", "deepseek-chat", "minimax-m2"} {
		if !extractsThinkTags(m) {
			t.Errorf("%s should be wrapped", m)
		}
	}
}

func TestLiteralTagsSurviveOnNativeModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sse(w, `{"choices":[{"delta":{"content":"use <think>tags</think> here"}}]}`, `[DONE]`)
	}))
	t.Cleanup(srv.Close)
	p := New(Options{ID: "lab", BaseURL: srv.URL})
	text, reason := streamText(t, p, llm.Request{Model: "gpt-5.4", Messages: []llm.Message{llm.UserText("hi")}})
	if text != "use <think>tags</think> here" || reason != "" {
		t.Fatalf("text=%q reason=%q", text, reason)
	}
	text, reason = streamText(t, p, llm.Request{Model: "qwen3-coder", Messages: []llm.Message{llm.UserText("hi")}})
	if text != "use  here" || reason != "tags" {
		t.Fatalf("wrapped model: text=%q reason=%q", text, reason)
	}
}

func TestModelsReadContextAndCatalogPricing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"data":[
			{"id":"a/b","context_length":200000,"pricing":{"prompt":"0.000001","completion":"0.000002"}},
			{"id":"c","max_context_length":8192},
			{"id":"free","pricing":{"prompt":"0","completion":"0"}},
			{"id":"dyn","pricing":{"prompt":"-1","completion":"-1"}},
			{"id":"lab","meta":{"n_ctx_train":4096}},
			{"id":"mi","model_info":{"max_input_tokens":32000}}
		]}`)
	}))
	t.Cleanup(srv.Close)
	ms, err := New(Options{ID: "cat", BaseURL: srv.URL}).Models(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]llm.ModelInfo{}
	for _, m := range ms {
		by[m.ID] = m
	}
	if m := by["a/b"]; m.ContextWindow != 200000 || math.Abs(m.InputPerMTok-1) > 1e-9 || math.Abs(m.OutputPerMTok-2) > 1e-9 || !m.CatalogPriced {
		t.Fatalf("a/b = %+v", m)
	}
	if by["c"].ContextWindow != 8192 || by["lab"].ContextWindow != 4096 || by["mi"].ContextWindow != 32000 {
		t.Fatalf("context fields: c=%d lab=%d mi=%d", by["c"].ContextWindow, by["lab"].ContextWindow, by["mi"].ContextWindow)
	}
	if f := by["free"]; !f.CatalogPriced || f.InputPerMTok != 0 {
		t.Fatalf("zero catalog price must be kept as free: %+v", f)
	}
	if d := by["dyn"]; d.CatalogPriced {
		t.Fatalf("negative price must not count as priced: %+v", d)
	}
}

func TestModelsTimeoutFallsBackToStatic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		_, _ = io.WriteString(w, `{"data":[{"id":"late"}]}`)
	}))
	t.Cleanup(srv.Close)
	p := New(Options{ID: "lab", BaseURL: srv.URL, Models: []string{"static-1"}, ModelsTimeout: 50 * time.Millisecond})
	ms, err := p.Models(context.Background())
	if err != nil || len(ms) != 1 || ms[0].ID != "static-1" {
		t.Fatalf("models = %+v, err = %v; want the static list", ms, err)
	}
}

func TestTokenSourceSuppliesBearer(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`)
	}))
	t.Cleanup(srv.Close)
	p := New(Options{ID: "lab", BaseURL: srv.URL, APIKey: "static", TokenSource: func(context.Context) (string, error) {
		return "dyn-1", nil
	}})
	streamText(t, p, llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("hi")}})
	if auth != "Bearer dyn-1" {
		t.Fatalf("Authorization = %q, want the token source's bearer", auth)
	}

	bad := New(Options{ID: "lab", BaseURL: srv.URL, TokenSource: func(context.Context) (string, error) {
		return "", io.ErrUnexpectedEOF
	}})
	if _, err := bad.Stream(context.Background(), llm.Request{Model: "m", Messages: []llm.Message{llm.UserText("hi")}}); err == nil {
		t.Fatal("a failing token source must fail the call")
	}
}

func TestPromptCacheKeyOnlyOnOpenAI(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("x-prompt-cache-key"))
		sse(w, `{"choices":[{"delta":{"content":"ok"}}]}`, `[DONE]`)
	}))
	t.Cleanup(srv.Close)
	req := llm.Request{Model: "gpt-5.4", Messages: []llm.Message{llm.UserText("hi")}, Headers: map[string]string{"X-Session-Id": "sess-9"}}
	streamText(t, New(Options{ID: "openai", BaseURL: srv.URL}), req)
	streamText(t, New(Options{ID: "lab", BaseURL: srv.URL}), req)
	if len(got) != 2 || got[0] != "sess-9" || got[1] != "" {
		t.Fatalf("x-prompt-cache-key = %q, want [sess-9 \"\"]", got)
	}
}

func TestParallelToolCallsDisabledOnRequest(t *testing.T) {
	tools := []llm.ToolSpec{{Name: "a", Schema: json.RawMessage(`{"type":"object"}`)}}
	p := New(Options{ID: "lab", BaseURL: "http://unused"})

	body, err := p.buildBody(llm.Request{Model: "m", Tools: tools, DisableParallelToolUse: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"parallel_tool_calls":false`) {
		t.Fatalf("body = %s, want parallel_tool_calls false", body)
	}
	body, _ = p.buildBody(llm.Request{Model: "m", Tools: tools})
	if strings.Contains(string(body), "parallel_tool_calls") {
		t.Fatalf("default body must not set parallel_tool_calls: %s", body)
	}
}
