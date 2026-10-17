package tui

import (
	"reflect"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// w19ReasoningRows returns the Think tab rows for one model with the given
// reasoning style, without a live model lookup.
func w19ReasoningRows(t *testing.T, ref string, style string, reasoning bool) []setRow {
	t.Helper()
	_, b := newFlat7Backend(t)
	b.model = ref
	s := &settings{be: b, styles: map[string]modelStyle{
		ref: {style: style, reasoning: reasoning},
	}}
	return s.reasoningRows()
}

func TestDeepSeekV4ThinkTabShowsEffortOffHighMax(t *testing.T) {
	rows := w19ReasoningRows(t, "deepseek/deepseek-v4-pro", llm.ReasoningEffort, false)
	if !w9Has(rows, "Effort") {
		t.Fatalf("DeepSeek V4 should show an Effort row, got %v", w9Labels(rows))
	}
	if want := []string{"", "high", "max"}; !reflect.DeepEqual(effortOptionsFor("deepseek"), want) {
		t.Fatalf("DeepSeek effort options = %v, want %v", effortOptionsFor("deepseek"), want)
	}
}

func TestDeepSeekV3ThinkTabShowsNoEffortRow(t *testing.T) {
	// deepseek-chat takes no effort, so the tab shows the no-controls row.
	rows := w19ReasoningRows(t, "deepseek/deepseek-chat", llm.ReasoningNone, false)
	if w9Has(rows, "Effort") {
		t.Fatalf("deepseek-chat should show no Effort row, got %v", w9Labels(rows))
	}
	if !w9Has(rows, "No reasoning controls") {
		t.Fatalf("deepseek-chat should say there are no reasoning controls, got %v", w9Labels(rows))
	}
}

func TestXAIThinkTabShowsEffortForEveryGrokModelThatTakesIt(t *testing.T) {
	for _, id := range []string{"xai/grok-3-mini", "xai/grok-4.5", "xai/grok-4.6", "xai/grok-4.7"} {
		rows := w19ReasoningRows(t, id, llm.ReasoningEffort, false)
		if !w9Has(rows, "Effort") {
			t.Errorf("%s should show an Effort row, got %v", id, w9Labels(rows))
		}
	}
	if want := []string{"", "low", "medium", "high"}; !reflect.DeepEqual(effortOptionsFor("xai"), want) {
		t.Fatalf("xAI effort options = %v, want the spec list %v", effortOptionsFor("xai"), want)
	}
}

func TestXAIThinkTabHasNoEffortRowForGrok4(t *testing.T) {
	rows := w19ReasoningRows(t, "xai/grok-4", llm.ReasoningNone, false)
	if w9Has(rows, "Effort") {
		t.Fatalf("grok-4 should show no Effort row, got %v", w9Labels(rows))
	}
}

func TestEffortOptionsForOtherProvidersUnchanged(t *testing.T) {
	if !reflect.DeepEqual(effortOptionsFor("openai"), effortOptions) {
		t.Fatalf("openai effort options changed: %v", effortOptionsFor("openai"))
	}
	if want := []string{"", "low", "medium", "high", "xhigh"}; !reflect.DeepEqual(effortOptionsFor("llmgateway"), want) {
		t.Fatalf("llmgateway effort options = %v", effortOptionsFor("llmgateway"))
	}
}
