package delegation

import (
	"strings"
	"testing"
)

func TestLeadPromptNamesProjectAndRequest(t *testing.T) {
	p := LeadPrompt("librarr", "Add an /opds/recent feed", 2)
	for _, want := range []string{`project "librarr"`, `delegate_build with project "librarr"`,
		"at most 2 correction\n   cycle(s)", "REQUEST:\n\nAdd an /opds/recent feed", "accept_build"} {
		if !strings.Contains(p, want) {
			t.Fatalf("lead prompt lacks %q:\n%s", want, p)
		}
	}
	if !strings.HasSuffix(p, "\n") {
		t.Fatal("prompt should end with a newline")
	}
	if strings.Contains(LeadPrompt("x", "y", -3), "-3") {
		t.Fatal("negative cycles should clamp to 0")
	}
}

func TestIsLead(t *testing.T) {
	if !IsLead([]string{"bug", LeadLabel}) || IsLead([]string{"bug"}) || IsLead(nil) {
		t.Fatal("IsLead should key on the label alone")
	}
}

func TestLeadMCPPerAgent(t *testing.T) {
	codex := LeadMCP("codex", "/usr/local/bin/lectern", "http://127.0.0.1:9110", "")
	entry := codex[MCPServerName].(map[string]any)
	if entry["command"] != "/usr/local/bin/lectern" || entry["tool_timeout_sec"] != float64(LeadToolTimeoutSeconds) {
		t.Fatalf("codex entry: %v", entry)
	}
	env := entry["env"].(map[string]any)
	if env["LECTERN_API"] != "http://127.0.0.1:9110" {
		t.Fatalf("env: %v", env)
	}
	if _, ok := env["LECTERN_AUTH_TOKEN"]; ok {
		t.Fatal("an open server must not pass an empty token")
	}
	claude := LeadMCP("claude", "/x", "http://h", "tok")[MCPServerName].(map[string]any)
	if _, ok := claude["tool_timeout_sec"]; ok {
		t.Fatal("Claude Code does not read tool_timeout_sec from the MCP config")
	}
	if claude["env"].(map[string]any)["LECTERN_AUTH_TOKEN"] != "tok" {
		t.Fatal("token should be passed when the server has one")
	}
	if LeadEnv("claude")["MCP_TOOL_TIMEOUT"] != "3600000" || LeadEnv("codex") != nil {
		t.Fatalf("lead env: %v / %v", LeadEnv("claude"), LeadEnv("codex"))
	}
}

func TestValidateLeadAgent(t *testing.T) {
	s := Defaults()
	s.LeadAgent = "gemini"
	if err := s.Validate(); err == nil {
		t.Fatal("a custom agent cannot be the lead: it has no MCP mapping")
	}
	s.LeadAgent = "codex"
	if err := s.Validate(); err != nil {
		t.Fatal(err)
	}
}
