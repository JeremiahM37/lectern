package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/openai"
)

// openAIWireBody sends req through the real OpenAI-compatible adapter, with
// the provider id given, to a test server and returns the JSON body received.
func openAIWireBody(t *testing.T, id string, req llm.Request) map[string]any {
	t.Helper()
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Errorf("request body is not JSON: %v", err)
		}
		http.Error(w, "test server", http.StatusBadRequest)
	}))
	defer srv.Close()
	p := openai.New(openai.Options{ID: id, BaseURL: srv.URL, HTTPClient: srv.Client()})
	ch, err := p.Stream(t.Context(), req)
	if err == nil {
		for range ch {
		}
	}
	if got == nil {
		t.Fatal("the test server received no request")
	}
	return got
}

// tunedProvider builds a request the way App.tune does for one provider and
// model, with the same three hooks in the same order.
func tunedProvider(provider, model string, info llm.ModelInfo, perf config.Performance, rs config.Reasoning) llm.Request {
	req := llm.Request{Model: model, System: "be brief", MaxTokens: 4096, Messages: []llm.Message{llm.UserText("hi")}}
	applyReasoning(&req, rs, info.ReasoningStyle)
	applyPerformance(&req, perf, rs, info.ReasoningStyle)
	applyProviderReasoning(&req, provider, rs, info.ReasoningStyle)
	return req
}

func TestOpenRouterReasoningRowsReachBody(t *testing.T) {
	info := llm.ModelInfo{ID: "openai/o3", Reasoning: true}
	rs := config.Reasoning{Effort: "xhigh", Exclude: true}
	body := openAIWireBody(t, "openrouter", tunedProvider("openrouter", "openai/o3", info, config.Performance{}, rs))
	rr, _ := body["reasoning"].(map[string]any)
	if rr["effort"] != "xhigh" || rr["exclude"] != true {
		t.Fatalf("reasoning = %v, want effort xhigh and exclude true", rr)
	}
}

func TestOpenRouterMaxTokensRowReachesBody(t *testing.T) {
	// A non-OpenAI model whose style is none still takes the token budget.
	info := llm.ModelInfo{ID: "anthropic/claude-sonnet-4.5", Reasoning: true}
	body := openAIWireBody(t, "openrouter", tunedProvider("openrouter", "anthropic/claude-sonnet-4.5", info, config.Performance{}, config.Reasoning{Budget: 8192}))
	rr, _ := body["reasoning"].(map[string]any)
	if rr["max_tokens"] != float64(8192) {
		t.Fatalf("reasoning = %v, want max_tokens 8192", rr)
	}
}

func TestOpenRouterHideReasoningOffIsNotSent(t *testing.T) {
	info := llm.ModelInfo{ID: "openai/o3", Reasoning: true}
	body := openAIWireBody(t, "openrouter", tunedProvider("openrouter", "openai/o3", info, config.Performance{}, config.Reasoning{Effort: "low"}))
	rr, _ := body["reasoning"].(map[string]any)
	if _, has := rr["exclude"]; has {
		t.Fatalf("exclude sent while off: %v", rr)
	}
}

func TestOpenAIVerbosityReachesGPT5Body(t *testing.T) {
	rs := config.Reasoning{Verbosity: "low"}
	info := llm.ModelInfo{ID: "gpt-5.4", ReasoningStyle: llm.ReasoningEffort}
	body := openAIWireBody(t, "openai", tunedProvider("openai", "gpt-5.4", info, config.Performance{}, rs))
	if body["verbosity"] != "low" {
		t.Fatalf("verbosity = %v, want low", body["verbosity"])
	}
	// A model without the control gets no verbosity, even with the row set.
	info = llm.ModelInfo{ID: "gpt-4.1"}
	body = openAIWireBody(t, "openai", tunedProvider("openai", "gpt-4.1", info, config.Performance{}, rs))
	if _, has := body["verbosity"]; has {
		t.Fatalf("verbosity sent to gpt-4.1: %v", body["verbosity"])
	}
}

func TestXAIEffortRowReachesBody(t *testing.T) {
	info := llm.ModelInfo{ID: "grok-3-mini", ReasoningStyle: llm.ReasoningEffort}
	body := openAIWireBody(t, "xai", tunedProvider("xai", "grok-3-mini", info, config.Performance{}, config.Reasoning{Effort: "low"}))
	if body["reasoning_effort"] != "low" {
		t.Fatalf("reasoning_effort = %v, want low", body["reasoning_effort"])
	}
}

func TestLLMGatewayXHighEffortReachesBody(t *testing.T) {
	info := llm.ModelInfo{ID: "openai/gpt-5", ReasoningStyle: llm.ReasoningEffort}
	body := openAIWireBody(t, "llmgateway", tunedProvider("llmgateway", "openai/gpt-5", info, config.Performance{}, config.Reasoning{Effort: "xhigh"}))
	if body["reasoning_effort"] != "xhigh" {
		t.Fatalf("reasoning_effort = %v, want xhigh", body["reasoning_effort"])
	}
}

// TestProviderRowsSavedThroughBackendReachBody goes the whole way: the value is
// written through the real backend (what the Think tab's rows call), read back
// by App.tune, and the body the adapter sends is checked.
func TestProviderRowsSavedThroughBackendReachBody(t *testing.T) {
	cases := []struct {
		name, provider, model, key string
		value                      any
		info                       llm.ModelInfo
		check                      func(t *testing.T, body map[string]any)
	}{
		{"openrouter effort", "openrouter", "openai/o3", "reasoning_by_provider.openrouter.effort", "high",
			llm.ModelInfo{ID: "openai/o3", Reasoning: true}, func(t *testing.T, b map[string]any) {
				if rr, _ := b["reasoning"].(map[string]any); rr["effort"] != "high" {
					t.Fatalf("reasoning = %v", b["reasoning"])
				}
			}},
		{"openrouter budget", "openrouter", "anthropic/claude-sonnet-4.5", "reasoning_by_provider.openrouter.budget", 4096,
			llm.ModelInfo{ID: "anthropic/claude-sonnet-4.5", Reasoning: true}, func(t *testing.T, b map[string]any) {
				if rr, _ := b["reasoning"].(map[string]any); rr["max_tokens"] != float64(4096) {
					t.Fatalf("reasoning = %v", b["reasoning"])
				}
			}},
		{"openrouter exclude", "openrouter", "openai/o3", "reasoning_by_provider.openrouter.exclude", true,
			llm.ModelInfo{ID: "openai/o3", Reasoning: true}, func(t *testing.T, b map[string]any) {
				if rr, _ := b["reasoning"].(map[string]any); rr["exclude"] != true {
					t.Fatalf("reasoning = %v", b["reasoning"])
				}
			}},
		{"openai verbosity", "openai", "gpt-5.4", "reasoning_by_provider.openai.verbosity", "medium",
			llm.ModelInfo{ID: "gpt-5.4", ReasoningStyle: llm.ReasoningEffort}, func(t *testing.T, b map[string]any) {
				if b["verbosity"] != "medium" {
					t.Fatalf("verbosity = %v", b["verbosity"])
				}
			}},
		{"xai effort", "xai", "grok-3-mini", "reasoning_by_provider.xai.effort", "high",
			llm.ModelInfo{ID: "grok-3-mini", ReasoningStyle: llm.ReasoningEffort}, func(t *testing.T, b map[string]any) {
				if b["reasoning_effort"] != "high" {
					t.Fatalf("reasoning_effort = %v", b["reasoning_effort"])
				}
			}},
		{"llmgateway xhigh", "llmgateway", "openai/gpt-5", "reasoning_by_provider.llmgateway.effort", "xhigh",
			llm.ModelInfo{ID: "openai/gpt-5", ReasoningStyle: llm.ReasoningEffort}, func(t *testing.T, b map[string]any) {
				if b["reasoning_effort"] != "xhigh" {
					t.Fatalf("reasoning_effort = %v", b["reasoning_effort"])
				}
			}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			be, _ := testBackend(t)
			sb, ok := be.(*scribeBackend)
			if !ok {
				t.Fatalf("backend is %T, want *scribeBackend", be)
			}
			if err := be.SetConfig(c.key, c.value, false); err != nil {
				t.Fatalf("SetConfig %s: %v", c.key, err)
			}
			req := llm.Request{Model: c.model, System: "s", MaxTokens: 512, Messages: []llm.Message{llm.UserText("hi")}}
			sb.app.tune(agent.Model{Ref: c.provider + "/" + c.model, ID: c.model, Info: c.info}, &req)
			c.check(t, openAIWireBody(t, c.provider, req))
		})
	}
}
