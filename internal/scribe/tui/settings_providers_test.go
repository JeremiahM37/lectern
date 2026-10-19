package tui

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// providerSettings returns a settings modal on the fake backend with the
// given model selected and its reasoning style already known.
func providerSettings(t *testing.T, ref string, st modelStyle) *settings {
	t.Helper()
	be := NewFakeBackend()
	if err := be.SetModel(ref); err != nil {
		t.Fatal(err)
	}
	return &settings{be: be, styles: map[string]modelStyle{ref: st}, loading: map[string]bool{}}
}

func providerRow(rows []setRow, label string) (setRow, bool) {
	for _, r := range rows {
		if r.label == label {
			return r, true
		}
	}
	return setRow{}, false
}

func TestOpenRouterRowsShowOnlyForReasoningModels(t *testing.T) {
	on := providerSettings(t, "openrouter/openai/o3", modelStyle{reasoning: true})
	rows := on.reasoningRows()
	for _, want := range []string{"Effort", "Max reasoning tokens", "Hide reasoning"} {
		if !w9Has(rows, want) {
			t.Fatalf("reasoning OpenRouter model: want %q, got %v", want, w9Labels(rows))
		}
	}
	off := providerSettings(t, "openrouter/meta/llama", modelStyle{})
	if rows := off.reasoningRows(); w9Has(rows, "Effort") || w9Has(rows, "Hide reasoning") {
		t.Fatalf("model without reasoning must show no controls, got %v", w9Labels(rows))
	}
}

func TestOpenRouterRowsUseTheSavedKeys(t *testing.T) {
	s := providerSettings(t, "openrouter/openai/o3", modelStyle{reasoning: true})
	s.rsGlobal = config.Config{ReasoningByProvider: map[string]config.Reasoning{
		"openrouter": {Effort: "low", Budget: 2048, Exclude: true},
	}}
	rows := s.reasoningRows()
	eff, _ := providerRow(rows, "Effort")
	if eff.key != "reasoning_by_provider.openrouter.effort" || eff.value != "low" {
		t.Fatalf("effort row = %q %q", eff.key, eff.value)
	}
	bud, _ := providerRow(rows, "Max reasoning tokens")
	if bud.key != "reasoning_by_provider.openrouter.budget" || bud.value != "2048" {
		t.Fatalf("budget row = %q %q", bud.key, bud.value)
	}
	hide, _ := providerRow(rows, "Hide reasoning")
	if hide.key != "reasoning_by_provider.openrouter.exclude" || hide.value != "on" || !hide.toggle {
		t.Fatalf("hide row = %+v", hide)
	}
	// Each option list starts at the provider default.
	if openRouterEffortOptions[0] != "" || openRouterBudgetOptions[0] != 0 {
		t.Fatal("first OpenRouter option must be the default")
	}
}

func TestVerbosityOnlyForGPT5EffortModels(t *testing.T) {
	s := providerSettings(t, "openai/gpt-5.4", modelStyle{style: llm.ReasoningEffort})
	v, ok := providerRow(s.reasoningRows(), "Verbosity")
	if !ok || v.key != "reasoning_by_provider.openai.verbosity" {
		t.Fatalf("GPT-5 model: want Verbosity row, got %v", w9Labels(s.reasoningRows()))
	}
	for _, id := range []string{"gpt-4.1", "gpt-5-chat-latest"} {
		s := providerSettings(t, "openai/"+id, modelStyle{style: llm.ReasoningNone})
		if w9Has(s.reasoningRows(), "Verbosity") {
			t.Fatalf("%s: verbosity must not be shown", id)
		}
	}
	o3 := providerSettings(t, "openai/o3", modelStyle{style: llm.ReasoningEffort})
	if w9Has(o3.reasoningRows(), "Verbosity") {
		t.Fatal("o3 takes no verbosity; the row must not be shown")
	}
}

func TestLLMGatewayEffortOffersXHigh(t *testing.T) {
	s := providerSettings(t, "llmgateway/openai/gpt-5", modelStyle{style: llm.ReasoningEffort})
	eff, ok := providerRow(s.reasoningRows(), "Effort")
	if !ok {
		t.Fatalf("no Effort row: %v", w9Labels(s.reasoningRows()))
	}
	if got := eff.act; got == nil {
		t.Fatal("effort row has no action")
	}
	if effortOptionsFor("llmgateway")[len(gatewayEffortOptions)-1] != "xhigh" {
		t.Fatal("LLM Gateway must offer xhigh")
	}
	if effortOptionsFor("openai")[len(effortOptions)-1] == "xhigh" {
		t.Fatal("other providers keep the three-level list")
	}
}

func TestClaudeRuntimeRowsUseTheExistingKeys(t *testing.T) {
	s := &settings{}
	rows := s.claudeRuntimeRows(config.Performance{})
	want := map[string]string{
		"Tool streaming": "performance.tool_streaming",
		"Send reasoning": "performance.send_reasoning",
	}
	if len(rows) != len(want) {
		t.Fatalf("want %d rows, got %d", len(want), len(rows))
	}
	for _, r := range rows {
		if want[r.label] != r.key {
			t.Fatalf("row %q has key %q, want %q", r.label, r.key, want[r.label])
		}
	}
	if rows[0].value != "on" || rows[1].value != "off" {
		t.Fatalf("defaults should show tool streaming on, reasoning off; got %q %q", rows[0].value, rows[1].value)
	}
	on := true
	set := s.claudeRuntimeRows(config.Performance{ToolStreaming: &on, SendReasoning: &on})
	if set[0].value != "on" || set[1].value != "on" {
		t.Fatalf("set values should show, got %q %q", set[0].value, set[1].value)
	}
}
