package anthropic

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func decodeBody(t *testing.T, req llm.Request) map[string]any {
	t.Helper()
	b, err := buildBody(req)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestDisableParallelToolUseSetsToolChoiceFlag(t *testing.T) {
	tools := []llm.ToolSpec{{Name: "read", Schema: json.RawMessage(`{"type":"object"}`)}}
	m := decodeBody(t, llm.Request{Model: "claude-haiku-4-5", Tools: tools, DisableParallelToolUse: true})
	tc, _ := m["tool_choice"].(map[string]any)
	if tc["type"] != "auto" || tc["disable_parallel_tool_use"] != true {
		t.Fatalf("tool_choice = %v", tc)
	}

	m = decodeBody(t, llm.Request{Model: "claude-haiku-4-5", Tools: tools, ToolChoice: "none", DisableParallelToolUse: true})
	if tc, _ := m["tool_choice"].(map[string]any); tc["disable_parallel_tool_use"] != nil {
		t.Fatalf("tool_choice none must not carry the flag: %v", tc)
	}

	m = decodeBody(t, llm.Request{Model: "claude-haiku-4-5", Tools: tools})
	if tc, _ := m["tool_choice"].(map[string]any); tc["disable_parallel_tool_use"] != nil {
		t.Fatalf("default must not carry the flag: %v", tc)
	}
}

func TestOneHourCacheTTLOnBreakpoints(t *testing.T) {
	req := llm.Request{
		Model:       "claude-haiku-4-5",
		System:      "sys",
		Messages:    []llm.Message{llm.UserText("hi")},
		CacheSystem: true,
		CacheTTL:    "1h",
	}
	raw, _ := json.Marshal(decodeBody(t, req))
	if n := strings.Count(string(raw), `"ttl":"1h"`); n != 2 {
		t.Fatalf("1h breakpoints = %d, want 2 (system and last message): %s", n, raw)
	}

	req.CacheTTL = ""
	raw, _ = json.Marshal(decodeBody(t, req))
	if strings.Contains(string(raw), `"ttl"`) {
		t.Fatalf("default TTL must not name a ttl: %s", raw)
	}
	if !strings.Contains(string(raw), `"cache_control":{"type":"ephemeral"}`) {
		t.Fatalf("default breakpoints missing: %s", raw)
	}
}

func TestCurrentModelsHaveMillionTokenWindows(t *testing.T) {
	for _, id := range []string{"claude-opus-5-5", "claude-sonnet-5-5", "claude-fable-5-1"} {
		k, ok := lookupKnown(id)
		if !ok || k.ctxWindow != 1000000 {
			t.Errorf("%s window = %d (known %v), want 1000000", id, k.ctxWindow, ok)
		}
	}
	if k, _ := lookupKnown("claude-haiku-5-5"); k.ctxWindow != 200000 {
		t.Errorf("haiku window = %d, want 200000", k.ctxWindow)
	}
}

func TestAdaptiveModelsOmitTemperatureAndBudget(t *testing.T) {
	temp := 0.2
	m := decodeBody(t, llm.Request{Model: "claude-opus-4-7", Reasoning: "high", ThinkingBudget: 8000, Temperature: &temp})
	if _, ok := m["temperature"]; ok {
		t.Fatalf("adaptive model sent temperature: %v", m)
	}
	th, _ := m["thinking"].(map[string]any)
	if th["type"] != "adaptive" || th["budget_tokens"] != nil {
		t.Fatalf("thinking = %v, want adaptive without a budget", th)
	}
}
