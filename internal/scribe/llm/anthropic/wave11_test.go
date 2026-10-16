package anthropic

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// An unset temperature goes out as 0 on models that accept it (spec 4.8).
func TestUnsetTemperatureDefaultsToZeroWhereAccepted(t *testing.T) {
	body := bodyFor(t, llm.Request{Model: "claude-sonnet-4-5"})
	if v, ok := body["temperature"].(float64); !ok || v != 0 {
		t.Fatalf("temperature = %v, want 0", body["temperature"])
	}
}

// Thinking and adaptive-only models reject temperature, so none is sent.
func TestUnsetTemperatureNotSentWithThinkingOrAdaptive(t *testing.T) {
	if body := bodyFor(t, llm.Request{Model: "claude-sonnet-4-5", Reasoning: "medium", MaxTokens: 1000}); body["temperature"] != nil {
		t.Fatalf("temperature sent alongside thinking: %v", body["temperature"])
	}
	if body := bodyFor(t, llm.Request{Model: "claude-opus-5-5"}); body["temperature"] != nil {
		t.Fatalf("temperature sent to an adaptive-only model: %v", body["temperature"])
	}
}

// An explicit temperature still wins over the default.
func TestExplicitTemperatureKept(t *testing.T) {
	temp := 0.7
	body := bodyFor(t, llm.Request{Model: "claude-sonnet-4-5", Temperature: &temp})
	if v, ok := body["temperature"].(float64); !ok || v != 0.7 {
		t.Fatalf("temperature = %v, want 0.7", body["temperature"])
	}
}

func TestUnknownModelGetsNoDefaultTemperature(t *testing.T) {
	for model, want := range map[string]bool{
		"claude-sonnet-4-5": true,
		"claude-3-5-sonnet": true,
		"claude-opus-5-5":   false,
		"claude-fable-5-1":  false,
		"claude-orca-6":     false, // unknown family: leave it out
		"some-proxy-alias":  false,
	} {
		if got := knownTemperatureModel(model); got != want {
			t.Errorf("%s: %v, want %v", model, got, want)
		}
	}
}
