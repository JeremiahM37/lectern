package api_test

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// installedMCPPayload decodes the private runtime document the launcher
// published on the (mock) target — the last argument of the install helper.
func installedMCPPayload(t *testing.T, h *harness) map[string]any {
	t.Helper()
	for _, c := range h.mock().CmdLog() {
		if !strings.Contains(c, "python3 -c ") || !strings.Contains(c, "interactive MCP") {
			continue
		}
		fields := strings.Fields(c)
		last := strings.Trim(fields[len(fields)-1], "'")
		raw, err := base64.StdEncoding.DecodeString(last)
		if err != nil {
			t.Fatalf("install payload is not base64: %v", err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("install payload is not JSON: %s", raw)
		}
		return out
	}
	t.Fatalf("no MCP install command was issued: %v", h.mock().CmdLog())
	return nil
}

// OpenCode, Qwen Code and Copilot CLI receive a project's MCP servers through
// one extra private file their CLI reads beside the user's own config.
func TestCatalogAgentsGetProjectMCPThroughTheirOwnConfig(t *testing.T) {
	mcp := obj{"ops": obj{"command": "python3", "args": []string{"-m", "ops"}},
		"web": obj{"type": "http", "url": "https://mcp.example/mcp"}}
	for _, tc := range []struct {
		agent, want string
		check       func(map[string]any) bool
	}{
		{"opencode", "OPENCODE_CONFIG=", func(doc map[string]any) bool {
			m, _ := doc["mcp"].(map[string]any)
			ops, _ := m["ops"].(map[string]any)
			return ops["type"] == "local" && fmt.Sprint(ops["command"]) == "[python3 -m ops]"
		}},
		{"qwen", "QWEN_CODE_SYSTEM_DEFAULTS_PATH=", func(doc map[string]any) bool {
			m, _ := doc["mcpServers"].(map[string]any)
			web, _ := m["web"].(map[string]any)
			return web["httpUrl"] == "https://mcp.example/mcp"
		}},
		{"copilot", "--additional-mcp-config", func(doc map[string]any) bool {
			m, _ := doc["mcpServers"].(map[string]any)
			return m["ops"] != nil && m["web"] != nil
		}},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			h := newHarness(t)
			h.decode("PUT", "/api/agents", []obj{{"name": tc.agent, "command": tc.agent}}, 200, nil)
			p := h.project("mcp-"+tc.agent, obj{"default_agent": tc.agent, "mcp": mcp})
			h.session(obj{"agent": tc.agent, "project_id": p.id()})
			cmd := h.launchCmd()
			if !strings.Contains(cmd, tc.want) || !strings.Contains(cmd, "/lectern/mcp/mock/mcp.json") {
				t.Fatalf("%s launch does not point at the private MCP file: %s", tc.agent, cmd)
			}
			if strings.Contains(cmd, "mcp_servers.") {
				t.Fatalf("%s must not receive Codex overrides: %s", tc.agent, cmd)
			}
			if doc := installedMCPPayload(t, h); !tc.check(doc) {
				t.Fatalf("%s payload not translated: %v", tc.agent, doc)
			}
		})
	}
}

func TestCatalogAgentMCPRejectsStrictMode(t *testing.T) {
	h := newHarness(t)
	h.decode("PUT", "/api/agents", []obj{{"name": "opencode", "command": "opencode"}}, 200, nil)
	p := h.project("strict-oc", obj{"default_agent": "opencode", "strict_mcp": true,
		"mcp": obj{"ops": obj{"command": "x"}}})
	code, body := h.rawRequest("POST", "/api/sessions", fmt.Sprintf(`{"agent":"opencode","project_id":%d}`, p.id()), "")
	if code < 400 || !strings.Contains(string(body), "strict_mcp") {
		t.Fatalf("strict_mcp must be refused for additive OpenCode config, got %d %s", code, body)
	}
}

// Gemini CLI has no per-session MCP file (docs/context-parity.md), so its
// launch carries no MCP arguments at all, not Codex's.
func TestGeminiInteractiveLaunchGetsNoMCPTranslation(t *testing.T) {
	h := newHarness(t)
	p := h.project("mcp-gemini", obj{"default_agent": "gemini", "mcp": obj{"ops": obj{"command": "x"}}})
	h.session(obj{"agent": "gemini", "project_id": p.id()})
	cmd := h.launchCmd()
	if strings.Contains(cmd, "mcp_servers") || strings.Contains(cmd, "--mcp") || strings.Contains(cmd, "/lectern/mcp/") {
		t.Fatalf("gemini launch should carry no MCP translation: %s", cmd)
	}
}
