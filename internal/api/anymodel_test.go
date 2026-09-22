package api_test

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// The any-model door: a project can point its agent at any Anthropic-compatible
// endpoint through plain environment variables.
func TestProjectEnvReachesLaunchCommand(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{"name": "localmodel", "target_id": h.firstTargetID(),
		"repo_path": "/mock/lm",
		"env": obj{"ANTHROPIC_BASE_URL": "http://ollama-host:11434",
			"ANTHROPIC_AUTH_TOKEN": "ollama"}}, 201)
	h.run(p.id(), "local run", "x", obj{"model": "qwen3.5:4b"})

	cmd := h.launchCmd()
	for _, want := range []string{
		"ANTHROPIC_BASE_URL=http://ollama-host:11434",
		"ANTHROPIC_AUTH_TOKEN=ollama", "--model qwen3.5:4b"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("launch missing %q: %s", want, cmd)
		}
	}
}

func TestProjectEnvPatchable(t *testing.T) {
	h := newHarness(t)
	var patched obj
	h.decode("PATCH", fmt.Sprintf("/api/projects/%d", h.seededProjectID()),
		obj{"env": obj{"OPENAI_BASE_URL": "http://x:8000/v1"}}, 200, &patched)
	if !strings.Contains(patched.str("env_json"), "OPENAI_BASE_URL") {
		t.Fatalf("env_json: %v", patched.str("env_json"))
	}
}

// Every ssh/pct dispatch must push CURRENT credentials before launch, so an
// agent never runs on a rotated-out copy — the recurring remote 401.
func TestDispatchProvisionsCredentials(t *testing.T) {
	h := newHarness(t, withAgentCreds(t))
	tgt := h.post("/api/targets", obj{"name": "cred-ssh", "kind": "ssh", "host": "192.0.2.30"}, 201)
	p := h.post("/api/projects",
		obj{"name": "creddispatch", "target_id": tgt.id(), "repo_path": "/mock/cd"}, 201)
	h.run(p.id(), "cred dispatch", "x", nil)
	if !h.cmdLogHas(".claude/.credentials.json") {
		t.Fatalf("credentials were not provisioned before launch: %v", h.mock().CmdLog())
	}
}

func withAPIKey(key string) func(*config.Config) {
	return func(c *config.Config) { c.AnthropicAPIKey = key }
}

func TestAPIKeyInjectedIntoLaunch(t *testing.T) {
	h := newHarness(t, withAPIKey("sk-ant-xyz"))
	h.run(h.seededProjectID(), "keyed", "x", nil)
	if !strings.Contains(h.launchCmd(), "ANTHROPIC_API_KEY=sk-ant-xyz") {
		t.Fatalf("launch: %s", h.launchCmd())
	}
}

// A project can point at a different endpoint; its env wins over the base auth.
func TestProjectEnvOverridesBaseAuth(t *testing.T) {
	h := newHarness(t, withAPIKey("sk-base"))
	p := h.post("/api/projects", obj{"name": "altkey", "target_id": h.firstTargetID(),
		"repo_path": "/mock/a", "env": obj{"ANTHROPIC_API_KEY": "sk-project"}}, 201)
	h.run(p.id(), "override", "x", nil)
	cmd := h.launchCmd()
	if !strings.Contains(cmd, "ANTHROPIC_API_KEY=sk-project") {
		t.Fatalf("the project's key must win: %s", cmd)
	}
	if strings.Contains(cmd, "sk-base") {
		t.Errorf("the base key leaked into the launch: %s", cmd)
	}
}

func TestAPIKeySkipsCredentialPush(t *testing.T) {
	h := newHarness(t, withAPIKey("sk-ant-xyz"), func(c *config.Config) {
		c.ClaudeCredsPath = writeFile(t, filepath.Join(t.TempDir(), "creds.json"), "{}")
	})
	tgt := h.post("/api/targets", obj{"name": "keyed-ssh", "kind": "ssh", "host": "192.0.2.31"}, 201)
	p := h.post("/api/projects",
		obj{"name": "keyedremote", "target_id": tgt.id(), "repo_path": "/mock/kr"}, 201)
	h.run(p.id(), "keyed remote", "x", nil)
	if h.cmdLogHas(".claude/.credentials.json") {
		t.Fatal("with an API key there is nothing to rotate, so nothing should be pushed")
	}
}
