package agent

import (
	"fmt"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// REAUDIT5 01#54: max_tool_results sets how many tool-result summaries the
// working state keeps. Above the default of 30 it is honoured, not clamped.
func TestToolResultCapFollowsMaxToolResults(t *testing.T) {
	var msgs []llm.Message
	for i := 0; i < 40; i++ {
		id := fmt.Sprintf("s%d", i)
		msgs = append(msgs, callMsg(id, "shell", fmt.Sprintf(`{"command":"echo %d"}`, i)))
		msgs = append(msgs, resultMsg(id, "shell", "ok", false))
	}
	if got := len(buildWorkingState(msgs, 35).ToolResults); got != 35 {
		t.Fatalf("cap 35 kept %d tool results, want 35", got)
	}
	if got := len(BuildWorkingState(msgs).ToolResults); got != defaultMaxToolResults {
		t.Fatalf("default kept %d tool results, want %d", got, defaultMaxToolResults)
	}
	sc := SummaryCompactor{MaxToolResults: 35}
	if sc.toolResultCap() != 35 || (SummaryCompactor{}).toolResultCap() != defaultMaxToolResults {
		t.Fatal("toolResultCap must follow MaxToolResults and fall back to the default")
	}
}
