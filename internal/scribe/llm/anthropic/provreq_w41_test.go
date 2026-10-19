package anthropic

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// compactEditsOf returns the body's context_management edits as decoded JSON.
func compactEditsOf(t *testing.T, req llm.Request) []map[string]any {
	t.Helper()
	w, err := buildWire(req)
	if err != nil {
		t.Fatal(err)
	}
	if w.ContextManagement == nil {
		return nil
	}
	raw, _ := json.Marshal(w.ContextManagement)
	var cm struct {
		Edits []map[string]any `json:"edits"`
	}
	if err := json.Unmarshal(raw, &cm); err != nil {
		t.Fatal(err)
	}
	return cm.Edits
}

func TestCompactionOffByDefault(t *testing.T) {
	req := llm.Request{Model: "claude-opus-5-5", ContextWindow: 1000000}
	if edits := compactEditsOf(t, req); edits != nil {
		t.Fatalf("no compaction asked, got edits %v", edits)
	}
	if got := betaFeatures(req); slices.Contains(got, compactionBeta) {
		t.Fatalf("compaction beta sent without a compaction edit: %v", got)
	}
}

func TestCompactionEditOnLargeWindow(t *testing.T) {
	req := llm.Request{Model: "claude-opus-5-5", ContextWindow: 1000000, ServerCompaction: true}
	edits := compactEditsOf(t, req)
	if len(edits) != 1 {
		t.Fatalf("want one compaction edit, got %v", edits)
	}
	e := edits[0]
	if e["type"] != "compact_20260112" {
		t.Fatalf("edit type = %v", e["type"])
	}
	trig := e["trigger"].(map[string]any)
	// 0.8 x 1 000 000 is above the 160 000 floor.
	if trig["type"] != "input_tokens" || trig["value"] != float64(800000) {
		t.Fatalf("trigger = %v, want input_tokens 800000", trig)
	}
	if ins, _ := e["instructions"].(string); !strings.Contains(ins, "<summary>") {
		t.Fatalf("instructions must ask for summary tags, got %q", ins)
	}
	if got := betaFeatures(req); !slices.Contains(got, compactionBeta) {
		t.Fatalf("compaction beta missing: %v", got)
	}
	// Compaction alone needs only its own beta, not the context-management one.
	if got := betaFeatures(req); slices.Contains(got, contextManagementBeta) {
		t.Fatalf("compaction alone must not add %s: %v", contextManagementBeta, got)
	}
}

func TestCompactionTriggerFloor(t *testing.T) {
	// A 200 000 window gives 0.8 x 200 000 = 160 000, the floor.
	req := llm.Request{Model: "claude-sonnet-4-5", ContextWindow: 200000, ServerCompaction: true}
	edits := compactEditsOf(t, req)
	if len(edits) != 1 {
		t.Fatalf("200k window should compact, got %v", edits)
	}
	if v := edits[0]["trigger"].(map[string]any)["value"]; v != float64(160000) {
		t.Fatalf("trigger value = %v, want 160000", v)
	}
}

func TestCompactionSkippedBelowWindowFloor(t *testing.T) {
	req := llm.Request{Model: "claude-haiku-4-5", ContextWindow: 128000, ServerCompaction: true}
	if edits := compactEditsOf(t, req); edits != nil {
		t.Fatalf("a 128k window must not get compaction, got %v", edits)
	}
	if got := betaFeatures(req); slices.Contains(got, compactionBeta) {
		t.Fatalf("no edit, no beta: %v", got)
	}
}

func TestCompactionFollowsClearEditsInOrder(t *testing.T) {
	req := llm.Request{
		Model: "claude-opus-5-5", ContextWindow: 1000000, Reasoning: "high",
		ClearThinking: true, ClearToolUses: true, ServerCompaction: true,
	}
	edits := compactEditsOf(t, req)
	var types []string
	for _, e := range edits {
		types = append(types, e["type"].(string))
	}
	want := []string{"clear_thinking_20251015", "clear_tool_uses_20250919", "compact_20260112"}
	if !slices.Equal(types, want) {
		t.Fatalf("edit order = %v, want %v", types, want)
	}
	got := betaFeatures(req)
	if !slices.Contains(got, compactionBeta) || !slices.Contains(got, contextManagementBeta) {
		t.Fatalf("both betas needed with clear and compact edits: %v", got)
	}
}

func TestExistingClearEditsUnchangedWithoutCompaction(t *testing.T) {
	// The clear-only body must match what it was before compaction existed.
	// Thinking must be on for the thinking edit to appear.
	req := llm.Request{Model: "claude-opus-5-5", ContextWindow: 1000000, Reasoning: "high", ClearThinking: true, ClearToolUses: true}
	edits := compactEditsOf(t, req)
	if len(edits) != 2 {
		t.Fatalf("want the two clear edits, got %v", edits)
	}
	if got := betaFeatures(req); slices.Contains(got, compactionBeta) {
		t.Fatalf("clear-only request must not get the compaction beta: %v", got)
	}
}
