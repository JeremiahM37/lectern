package api_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestProjectMCPEndpointRedactsAndRetainsSecrets(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{
		"name": "mcp-editor", "target_id": h.firstTargetID(), "repo_path": "/mock/mcp-editor",
		"mcp": obj{
			"tokenizer": obj{"command": "tokenizer", "env": obj{"TOKEN": "real-secret", "COOKIE": "cookie-secret"}, "headers": obj{"X-Auth": "header-secret"}},
			"token":     obj{"command": "token"},
			"env":       obj{"command": "env"},
		},
	}, 201)
	if rows := h.getList("/api/projects"); strings.Contains(string(mustJSON(rows)), "real-secret") {
		t.Fatal("project list exposed credential-bearing MCP storage")
	}
	path := fmt.Sprintf("/api/projects/%d/mcp", p.id())
	var got obj
	h.decode("GET", path, nil, 200, &got)
	servers := got["mcp"].(map[string]any)
	config := servers["tokenizer"].(map[string]any)
	if config["command"] != "tokenizer" {
		t.Fatalf("server named tokenizer was mistaken for a secret: %#v", config)
	}
	for _, name := range []string{"token", "env"} {
		if got["mcp"].(map[string]any)[name].(map[string]any)["command"] != name {
			t.Fatalf("server named %s was mistaken for a secret", name)
		}
	}
	env := config["env"].(map[string]any)
	marker, ok := env["TOKEN"].(map[string]any)
	if !ok || marker["__lectern_retained"] == nil || strings.Contains(string(mustJSON(marker)), "real-secret") {
		t.Fatalf("token was not safely redacted: %#v", env["TOKEN"])
	}
	// A typed reference is bound to its field path; copying it to COOKIE cannot
	// make the TOKEN value appear there.
	cookieMarker := env["COOKIE"]
	env["COOKIE"] = marker
	if code := h.status("PUT", path, obj{"mcp": servers, "revision": got["revision"], "strict_mcp": false}); code != 409 {
		t.Fatalf("moved retention marker got %d", code)
	}
	env["COOKIE"] = cookieMarker
	if strings.Contains(string(mustJSON(got)), "real-secret") || strings.Contains(string(mustJSON(got)), "cookie-secret") || strings.Contains(string(mustJSON(got)), "header-secret") {
		t.Fatal("MCP response exposed a credential")
	}
	want := obj{}
	want["mcp"] = servers
	want["revision"] = got["revision"]
	want["strict_mcp"] = false
	h.decode("PUT", path, want, 200, nil)
	storedProject, err := h.App.DB.Project(p.id())
	if err != nil {
		t.Fatal(err)
	}
	stored := storedProject.MCPJSON
	if !strings.Contains(stored, "real-secret") || !strings.Contains(stored, "cookie-secret") || !strings.Contains(stored, "header-secret") {
		t.Fatalf("retained values did not round-trip: %s", stored)
	}
	if strings.Contains(stored, "__lectern_retained") {
		t.Fatalf("opaque marker was persisted instead of retained value: %s", stored)
	}
	var second obj
	h.decode("GET", path, nil, 200, &second)
	h.decode("PUT", path, obj{"mcp": second["mcp"], "revision": second["revision"], "strict_mcp": false}, 200, nil)
}

func TestProjectMCPLiteralPlaceholderIsLiteralAndConflictsAreConditional(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{
		"name": "mcp-literal", "target_id": h.firstTargetID(), "repo_path": "/mock/mcp-literal",
		"mcp": obj{"server": obj{"command": "server", "env": obj{"TOKEN": "original"}}},
	}, 201)
	path := fmt.Sprintf("/api/projects/%d/mcp", p.id())
	var got obj
	h.decode("GET", path, nil, 200, &got)
	servers := got["mcp"].(map[string]any)
	servers["server"].(map[string]any)["env"].(map[string]any)["TOKEN"] = "[secret retained]"
	h.decode("PUT", path, obj{"mcp": servers, "revision": got["revision"], "strict_mcp": false}, 200, nil)
	storedProject, err := h.App.DB.Project(p.id())
	if err != nil {
		t.Fatal(err)
	}
	if stored := storedProject.MCPJSON; !strings.Contains(stored, "[secret retained]") || strings.Contains(stored, "original") {
		t.Fatalf("literal placeholder collision was mishandled: %s", stored)
	}

	// A legacy strict_mcp patch participates in the same lock/revision contract.
	h.decode("PATCH", fmt.Sprintf("/api/projects/%d", p.id()), obj{"strict_mcp": true}, 200, nil)
	if code := h.status("PUT", path, obj{"mcp": servers, "revision": got["revision"], "strict_mcp": false}); code != 409 {
		t.Fatalf("stale revision after strict change got %d", code)
	}
}

func TestProjectMCPValidatesTransportTypesAndCodexStrict(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{"name": "mcp-validate", "target_id": h.firstTargetID(), "repo_path": "/mock/mcp-validate"}, 201)
	path := fmt.Sprintf("/api/projects/%d/mcp", p.id())
	var got obj
	h.decode("GET", path, nil, 200, &got)
	for _, servers := range []obj{
		{"bad": obj{"command": 7}},
		{"bad": obj{"url": "ftp://example.test/mcp"}},
		{"bad": obj{"url": "https://example.test/mcp", "headers": obj{"X": 9}}},
	} {
		if code := h.status("PUT", path, obj{"mcp": servers, "revision": got["revision"], "strict_mcp": false}); code != 422 {
			t.Errorf("invalid MCP %#v got %d", servers, code)
		}
	}

	codex := h.post("/api/projects", obj{"name": "mcp-codex", "target_id": h.firstTargetID(), "repo_path": "/mock/mcp-codex", "default_agent": "codex"}, 201)
	codexPath := fmt.Sprintf("/api/projects/%d/mcp", codex.id())
	var codexGot obj
	h.decode("GET", codexPath, nil, 200, &codexGot)
	if code := h.status("PUT", codexPath, obj{"mcp": obj{}, "revision": codexGot["revision"], "strict_mcp": true}); code != 422 {
		t.Fatalf("Codex strict MCP got %d", code)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// Keep the test's import of crypto/sha256/hex useful as a guard that the
// public revision is not an unsalted digest of the secret.
func TestProjectMCPRevisionIsNotPlainSecretDigest(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{"name": "mcp-revision", "target_id": h.firstTargetID(), "repo_path": "/mock/mcp-revision", "mcp": obj{"x": obj{"command": "x", "env": obj{"TOKEN": "guessable"}}}}, 201)
	var got obj
	h.decode("GET", fmt.Sprintf("/api/projects/%d/mcp", p.id()), nil, 200, &got)
	sum := sha256.Sum256([]byte(`{"x":{"command":"x","env":{"TOKEN":"guessable"}}}`))
	if got["revision"] == hex.EncodeToString(sum[:]) {
		t.Fatal("revision is an offline-guessable plain digest")
	}
}
