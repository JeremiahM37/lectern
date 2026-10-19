package agent

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// A subagent that wrote no answer must not report the code map's
// acknowledgement as its answer (QA41 item 7).
func TestLastAssistantTextSkipsLatchAcks(t *testing.T) {
	msgs := []llm.Message{
		llm.UserText("<soul_map>x</soul_map>"),
		llm.AssistantText(codeMapAck),
		llm.UserText("find the entry point"),
		llm.AssistantText(""),
	}
	if got := lastAssistantText(msgs); got != "" {
		t.Fatalf("report = %q, want no report", got)
	}
	msgs = append(msgs, llm.AssistantText("main is in cmd/app/main.go"))
	if got := lastAssistantText(msgs); got != "main is in cmd/app/main.go" {
		t.Fatalf("report = %q", got)
	}
}
