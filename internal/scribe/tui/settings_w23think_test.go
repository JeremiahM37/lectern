package tui

import (
	"reflect"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestDeepSeekThinkingRowIsOnOffDefault(t *testing.T) {
	s := &settings{}
	row := s.deepSeekThinkingRow("reasoning_by_provider.deepseek.", config.Reasoning{})
	if row.label != "Thinking" || row.key != "reasoning_by_provider.deepseek.thinking" {
		t.Fatalf("row = %q / %q", row.label, row.key)
	}
	if row.value != "default" {
		t.Errorf("unset value = %q, want default", row.value)
	}
	if want := []string{"", "on", "off"}; !reflect.DeepEqual(deepSeekThinkingOptions, want) {
		t.Errorf("options = %v, want %v", deepSeekThinkingOptions, want)
	}
	on := s.deepSeekThinkingRow("reasoning_by_provider.deepseek.", config.Reasoning{Thinking: "off"})
	if on.value != "off" {
		t.Errorf("off value = %q, want off", on.value)
	}
}

func TestDeepSeekThinkingRowOnlyForV4(t *testing.T) {
	if !deepSeekThinkingModel("deepseek-v4-pro") {
		t.Error("deepseek-v4-pro should show the thinking row")
	}
	for _, id := range []string{"deepseek-chat", "deepseek-reasoner", "deepseek/deepseek-v4-pro"} {
		if deepSeekThinkingModel(id) {
			t.Errorf("%s should show no thinking row", id)
		}
	}
}

func TestGeminiEffortOptions(t *testing.T) {
	if want := []string{"", "minimal", "low", "medium", "high"}; !reflect.DeepEqual(effortOptionsFor("gemini"), want) {
		t.Errorf("gemini effort options = %v, want %v", effortOptionsFor("gemini"), want)
	}
	// The effort-style switch in reasoningRows uses effortOptionsFor, so an
	// effort-style Gemini model gets its Effort row from the existing path.
	if llm.ReasoningEffort != "effort" {
		t.Error("effort style constant changed")
	}
}
