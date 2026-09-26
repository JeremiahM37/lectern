package drivers

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSelect(t *testing.T) {
	cases := []struct {
		name           string
		agent          string
		builtin        bool
		permissionMode string
		want           string
	}{
		{"claude default", "claude", true, "acceptEdits", KindClaudeExec},
		{"claude steerable", "claude", true, "steerable", KindClaudeSteer},
		{"claude gated stays exec (hook-based, unchanged)", "claude", true, "default", KindClaudeExec},
		{"codex bypass", "codex", true, "bypassPermissions", KindCodexExec},
		{"codex gated selects app-server", "codex", true, "default", KindCodexAppServer},
		{"codex steerable is not a thing; falls through to exec", "codex", true, "steerable", KindCodexExec},
		{"gemini", "gemini", true, "acceptEdits", KindGemini},
		{"custom CLI is always generic", "mycli", false, "steerable", KindGeneric},
		{"unknown builtin name", "cursor", true, "acceptEdits", KindGeneric},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Select(c.agent, c.builtin, c.permissionMode); got != c.want {
				t.Errorf("Select(%q, %v, %q) = %q, want %q", c.agent, c.builtin, c.permissionMode, got, c.want)
			}
		})
	}
}

// Project MCP servers go into ACP session/new only over transports the agent
// advertised; a remote server the agent cannot reach fails the attempt.
func TestACPSessionMCPChecksAdvertisedTransports(t *testing.T) {
	stdio := map[string]any{"name": "ops", "command": "x", "args": []string{}, "env": []any{}}
	web := map[string]any{"type": "http", "name": "web", "url": "https://x", "headers": []any{}}
	none := json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{}}`)
	httpOK := json.RawMessage(`{"protocolVersion":1,"agentCapabilities":{"mcpCapabilities":{"http":true}}}`)
	if got, err := acpSessionMCP(none, nil); err != nil || len(got) != 0 {
		t.Fatalf("no servers must send an empty list, got %v %v", got, err)
	}
	if got, err := acpSessionMCP(none, []any{stdio}); err != nil || len(got) != 1 {
		t.Fatalf("stdio is always supported, got %v %v", got, err)
	}
	if _, err := acpSessionMCP(none, []any{stdio, web}); err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("an http server without the capability must fail, got %v", err)
	}
	if got, err := acpSessionMCP(httpOK, []any{stdio, web}); err != nil || len(got) != 2 {
		t.Fatalf("an advertised http transport must pass both servers, got %v %v", got, err)
	}
}
