package chat

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

func TestFailedToolRowDoesNotSayItSucceeded(t *testing.T) {
	v, _ := newView(t, Options{})
	v.Apply(agent.Event{Kind: agent.EvTurnStart, Prompt: "go"})
	v.Apply(agent.Event{Kind: agent.EvToolRun, CallID: "c1", Tool: "rename_symbol", Args: []byte(`{"symbol":"Size","new_name":"Count"}`)})
	v.Apply(agent.Event{Kind: agent.EvToolResult, CallID: "c1", Tool: "rename_symbol", Result: &tools.Result{Output: "tool is deferred", IsError: true}})
	out := plain(v)
	if strings.Contains(out, "Renamed symbol") || !strings.Contains(out, "Failed: renaming symbol") {
		t.Fatalf("failed rename row:\n%s", out)
	}
}
