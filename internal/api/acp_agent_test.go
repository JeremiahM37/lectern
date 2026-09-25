package api_test

// Registry-level coverage for the `acp: {command, args, env}` agent
// definition (internal/sessions.ACPSpec): secret masking/retention parity
// with the agent-wide `env`, task/acp mutual exclusivity, and that an
// ACP-only agent (no `task`) is still a valid dispatch target. Real-process
// protocol coverage (the actual JSON-RPC turn, permissions, fs confinement,
// steering) lives in internal/drivers/acp_real_test.go — this harness's mock
// executor has no ACP agent to actually speak the protocol to.

import (
	"fmt"
	"strings"
	"testing"
)

func acpAgent(name string) obj {
	return obj{"name": name, "command": name,
		"acp": obj{"command": "npx", "args": []string{"-y", "@zed-industries/claude-code-acp"}}}
}

func TestACPAgentSecretsUseTypedRetentionMarkers(t *testing.T) {
	h := newHarness(t)
	first := obj{"name": "claude-code-acp", "command": "claude-code-acp",
		"acp": obj{"command": "npx", "args": []string{"-y", "@zed-industries/claude-code-acp"},
			"env": obj{"ACP_PROXY_TOKEN": "test-only-acp-secret", "ACP_LOG_LEVEL": "debug"}}}
	var response []obj
	h.decode("PUT", "/api/agents", []obj{first}, 200, &response)
	var saved obj
	for _, item := range response {
		if item.str("name") == "claude-code-acp" {
			saved = item
		}
	}
	if saved == nil {
		t.Fatal("saved acp agent missing")
	}
	acpView, ok := saved["acp"].(map[string]any)
	if !ok {
		t.Fatalf("agent acp view has type %T", saved["acp"])
	}
	env, ok := acpView["env"].(map[string]any)
	if !ok {
		t.Fatalf("acp env view has type %T", acpView["env"])
	}
	marker, ok := env["ACP_PROXY_TOKEN"].(map[string]any)
	if !ok || marker["__lectern_retained"] == nil {
		t.Fatalf("acp proxy token was not returned as a typed retention marker: %#v", env["ACP_PROXY_TOKEN"])
	}
	if env["ACP_LOG_LEVEL"] != "debug" {
		t.Fatalf("non-secret acp env value should pass through plainly: %#v", env["ACP_LOG_LEVEL"])
	}
	if strings.Contains(fmt.Sprint(saved), "test-only-acp-secret") {
		t.Fatal("agent response leaked the acp proxy secret")
	}

	// Round-trip the marker back — it must resolve to the real value, and a
	// marker minted for the AGENT-WIDE env must NOT unlock an acp.env secret
	// (the field is folded into the HMAC input, not just a map key).
	updated := obj{"name": "claude-code-acp", "command": "claude-code-acp",
		"acp": obj{"command": "npx", "args": []string{"-y", "@zed-industries/claude-code-acp"},
			"env": obj{"ACP_PROXY_TOKEN": marker, "ACP_LOG_LEVEL": "debug"}}}
	h.decode("PUT", "/api/agents", []obj{updated}, 200, nil)

	if code := h.status("PUT", "/api/agents", []obj{{
		"name": "claude-code-acp", "command": "claude-code-acp",
		"env": obj{"OPENAI_API_KEY": marker}, // wrong field: env, not acp.env
	}}); code != 400 {
		t.Fatalf("a marker minted for acp.env should not unlock an agent-wide env secret: %d", code)
	}

	for _, placeholder := range []string{"[secret retained]", "__KEEP__", "••••"} {
		if code := h.status("PUT", "/api/agents", []obj{{
			"name": "bad", "command": "bad",
			"acp": obj{"command": "npx", "env": obj{"ACP_PROXY_TOKEN": placeholder}},
		}}); code != 400 {
			t.Fatalf("placeholder %q should be rejected in acp.env rather than stored as a key: %d", placeholder, code)
		}
	}
}

func TestACPAndTaskAreMutuallyExclusive(t *testing.T) {
	h := newHarness(t)
	both := obj{"name": "conflicted", "command": "conflicted",
		"acp":  obj{"command": "npx"},
		"task": obj{"prompt_template": "{prompt}"}}
	if code := h.status("PUT", "/api/agents", []obj{both}); code != 400 {
		t.Fatalf("an agent declaring both acp and task should be rejected: %d", code)
	}
}

func TestACPOnlyAgentIsAValidTaskDispatchTarget(t *testing.T) {
	h := newHarness(t)
	h.decode("PUT", "/api/agents", []obj{acpAgent("claude-code-acp")}, 200, nil)
	p := h.project("acp-only", obj{"default_agent": "claude-code-acp"})
	if code := h.status("POST", "/api/tasks", obj{"project_id": p.id(), "title": "t", "prompt": "run"}); code != 201 {
		t.Fatalf("an acp-only agent (no task definition) should still be a valid task target: %d", code)
	}
}

// attempt.driver's selection (regardless of permission mode) is covered as a
// pure function in internal/scheduler/acp_select_test.go — exercising it
// through a full dispatch here would also exercise the mock executor's tmux
// launch path for a binary ("npx") the mock does not simulate, which is not
// what this test is for.
