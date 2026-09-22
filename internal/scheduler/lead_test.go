package scheduler

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/delegation"
)

func TestWithLeadMCPAddsTheServerAndKeepsAnOperatorsOwn(t *testing.T) {
	got := withLeadMCP(nil, "claude", "/opt/lectern/lectern", "http://127.0.0.1:9110", "")
	entry, ok := got["lectern"].(map[string]any)
	if !ok || entry["command"] != "/opt/lectern/lectern" {
		t.Fatalf("empty declaration: %v", got)
	}
	// The wrapped form stays wrapped, and the project's other servers survive.
	wrapped := map[string]any{"mcpServers": map[string]any{"grimoire": map[string]any{"command": "grimoire-mcp"}}}
	got = withLeadMCP(wrapped, "codex", "/x", "http://h", "tok")
	servers := got["mcpServers"].(map[string]any)
	if _, ok := servers["grimoire"]; !ok {
		t.Fatalf("project server dropped: %v", got)
	}
	if servers["lectern"].(map[string]any)["tool_timeout_sec"] != float64(delegation.LeadToolTimeoutSeconds) {
		t.Fatalf("codex needs the tool timeout: %v", servers["lectern"])
	}
	if inner := wrapped["mcpServers"].(map[string]any); len(inner) != 1 {
		t.Fatal("the project's declaration must not be mutated")
	}
	// A deliberately declared "lectern" server is the operator's to keep.
	own := map[string]any{"lectern": map[string]any{"command": "/their/lectern"}}
	got = withLeadMCP(own, "claude", "/x", "http://h", "")
	if got["lectern"].(map[string]any)["command"] != "/their/lectern" {
		t.Fatalf("operator's server replaced: %v", got)
	}
}
