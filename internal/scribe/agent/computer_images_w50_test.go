package agent

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// Only the newest screenshot is sent to the model, so an older one is cleared
// from history when a new one arrives.
func TestDropOldImagesClearsEarlierScreenshots(t *testing.T) {
	a := &Agent{msgs: []llm.Message{
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartToolResult, ToolName: "computer", Data: []byte("old"), MediaType: "image/png"}}},
		{Role: llm.RoleUser, Parts: []llm.Part{{Type: llm.PartText, Text: "keep"}}},
	}}
	a.dropOldImages()
	if got := a.msgs[0].Parts[0]; len(got.Data) != 0 || got.MediaType != "" {
		t.Fatalf("old screenshot still attached: %+v", got)
	}
	if a.msgs[1].Parts[0].Text != "keep" {
		t.Fatal("text parts must not change")
	}
}
