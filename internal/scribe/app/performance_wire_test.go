package app

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm/anthropic"
)

// wireBody sends req through the real Anthropic adapter to a test server and
// returns the JSON body the server received. It proves what reaches the wire,
// not what the request struct holds.
func wireBody(t *testing.T, req llm.Request) map[string]any {
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
	p := anthropic.New(anthropic.Options{APIKey: "k", BaseURL: srv.URL, HTTPClient: srv.Client(), ID: "anthropic"})
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

// tuned builds a request the way the agent does and runs it through the same
// two hooks App.tune calls, so the test follows the production path.
func tuned(model string, perf config.Performance, rs config.Reasoning) llm.Request {
	req := llm.Request{Model: model, System: "be brief", MaxTokens: 4096, Messages: []llm.Message{llm.UserText("hi")}, CacheSystem: true}
	style := anthropic.ReasoningStyle(model)
	applyReasoning(&req, rs, style)
	applyPerformance(&req, perf, rs, style)
	return req
}

func TestPerformanceThinkingOffReachesBody(t *testing.T) {
	// Think tab says high, Claude tab says off: off must win on the wire.
	body := wireBody(t, tuned("claude-opus-4-7", config.Performance{Thinking: "off"}, config.Reasoning{Effort: "high"}))
	if _, ok := body["thinking"]; ok {
		t.Fatalf("thinking off must send no thinking block, got %v", body["thinking"])
	}
}

func TestPerformanceAdaptiveEffortReachesBody(t *testing.T) {
	body := wireBody(t, tuned("claude-opus-4-7", config.Performance{Effort: "xhigh"}, config.Reasoning{}))
	oc, _ := body["output_config"].(map[string]any)
	if oc["effort"] != "xhigh" {
		t.Fatalf("output_config.effort = %v, want xhigh", oc["effort"])
	}
	th, _ := body["thinking"].(map[string]any)
	if th["type"] != "adaptive" {
		t.Fatalf("thinking = %v, want adaptive", th)
	}
}

func TestPerformanceEffortYieldsToProviderEffort(t *testing.T) {
	body := wireBody(t, tuned("claude-opus-4-7", config.Performance{Effort: "low"}, config.Reasoning{Effort: "max"}))
	oc, _ := body["output_config"].(map[string]any)
	if oc["effort"] != "max" {
		t.Fatalf("the Think tab effort must win, got %v", oc["effort"])
	}
}

func TestPerformanceEffortIgnoredOnBudgetModels(t *testing.T) {
	// Budget models take a budget, not an effort level, so the Claude tab's
	// effort must not switch thinking on for them. The settings screen hides
	// the row for these models; this proves the wire agrees.
	body := wireBody(t, tuned("claude-sonnet-4-5", config.Performance{Effort: "xhigh"}, config.Reasoning{}))
	if _, ok := body["thinking"]; ok {
		t.Fatalf("effort must not enable thinking on a budget model, got %v", body["thinking"])
	}
	if _, ok := body["output_config"]; ok {
		t.Fatal("budget models must not send output_config")
	}
	// The Think tab budget still applies to the same model.
	body = wireBody(t, tuned("claude-sonnet-4-5", config.Performance{}, config.Reasoning{Budget: 5000}))
	th, _ := body["thinking"].(map[string]any)
	if th["type"] != "enabled" || th["budget_tokens"] != float64(5000) {
		t.Fatalf("thinking = %v, want enabled with budget 5000", th)
	}
}

func TestPerformanceCacheTTLReachesBody(t *testing.T) {
	body := wireBody(t, tuned("claude-opus-4-7", config.Performance{CacheTTL: "1h"}, config.Reasoning{}))
	sys, _ := body["system"].([]any)
	first, _ := sys[0].(map[string]any)
	cc, _ := first["cache_control"].(map[string]any)
	if cc["ttl"] != "1h" {
		t.Fatalf("system cache_control = %v, want ttl 1h", cc)
	}
}

func TestPerformanceDefaultCacheTTLOmitsTTL(t *testing.T) {
	body := wireBody(t, tuned("claude-opus-4-7", config.Performance{}, config.Reasoning{}))
	sys, _ := body["system"].([]any)
	first, _ := sys[0].(map[string]any)
	cc, _ := first["cache_control"].(map[string]any)
	if _, ok := cc["ttl"]; ok {
		t.Fatalf("default lifetime must not send a ttl, got %v", cc)
	}
}

func TestPerformanceSequentialToolsReachesBody(t *testing.T) {
	req := tuned("claude-opus-4-7", config.Performance{DisableParallelToolUse: boolPtr(true)}, config.Reasoning{})
	req.Tools = []llm.ToolSpec{{Name: "read", Schema: json.RawMessage(`{"type":"object"}`)}}
	req.ToolChoice = "auto"
	body := wireBody(t, req)
	tc, _ := body["tool_choice"].(map[string]any)
	if tc["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice = %v, want disable_parallel_tool_use", tc)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestWatchdogThresholdsReachTheAgentOptions(t *testing.T) {
	var o agent.Options
	applyWatchdog(&o, config.Watchdog{FirstToken: 300, InterToken: 90, Step: 1200})
	if o.FirstTokenTimeout != 300*time.Second || o.InterTokenTimeout != 90*time.Second || o.StepTimeout != 1200*time.Second {
		t.Fatalf("agent options: first=%v inter=%v step=%v", o.FirstTokenTimeout, o.InterTokenTimeout, o.StepTimeout)
	}
	// Zero keeps the agent's defaults.
	var d agent.Options
	applyWatchdog(&d, config.Watchdog{})
	if d.FirstTokenTimeout != 0 || d.InterTokenTimeout != 0 || d.StepTimeout != 0 {
		t.Fatalf("unset thresholds must leave the agent defaults, got %+v", d)
	}
}
