package agent

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

func TestShrinkToolResultsFitsTheWindowAndKeepsTheHistory(t *testing.T) {
	big := strings.Repeat("x", 120000) // about 30k tokens
	hist := []llm.Message{{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartToolResult, ToolCallID: "c", Result: big}}}}
	req := llm.Request{System: "sys", Messages: hist}
	if !shrinkToolResults(&req, 8000) {
		t.Fatal("nothing was cut")
	}
	if est := estimateRequestTokens(req); est > 8000 {
		t.Fatalf("request still %d tokens", est)
	}
	if hist[0].Parts[0].Result != big {
		t.Fatal("the history was changed")
	}
	if !strings.Contains(req.Messages[0].Parts[0].Result, "cut to fit the context window") {
		t.Fatal("the cut result does not say it was cut")
	}
}
