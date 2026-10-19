package app

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// The server-side features are opt-in: an empty config leaves every request
// field unset, and each setting reaches its request field only when set.
func TestServerFeatureSettingsReachTheRequest(t *testing.T) {
	var off llm.Request
	applyPerformance(&off, config.Performance{}, config.Reasoning{}, llm.ReasoningEffort)
	if off.CodeExecution || off.ComputerUse || off.ServerCompaction {
		t.Fatalf("defaults must be off: %+v", off)
	}

	on := config.Performance{
		AnthropicCodeExecution:    w50Bool(true),
		AnthropicComputerUse:      w50Bool(true),
		AnthropicServerCompaction: w50Bool(true),
	}
	var r llm.Request
	applyPerformance(&r, on, config.Reasoning{}, llm.ReasoningEffort)
	if !r.CodeExecution || !r.ComputerUse || !r.ServerCompaction {
		t.Fatalf("settings did not reach the request: %+v", r)
	}
}

func TestOpenAIReasoningSummaryOnlyForOpenAI(t *testing.T) {
	p := config.Performance{OpenAIReasoningSummary: "auto"}
	var oa llm.Request
	applyPerProviderPerformance(&oa, "openai", p)
	if oa.ReasoningSummary != "auto" {
		t.Fatalf("openai summary = %q, want auto", oa.ReasoningSummary)
	}
	var other llm.Request
	applyPerProviderPerformance(&other, "anthropic", p)
	if other.ReasoningSummary != "" {
		t.Fatalf("summary leaked to anthropic: %q", other.ReasoningSummary)
	}
}

func w50Bool(v bool) *bool { return &v }
