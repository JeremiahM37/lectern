package api_test

// codex as a first-class agent: the project-level toggle, correct launch flags,
// and its own credential provisioning.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// runToEnd dispatches and waits for the task to finish either way.
func (h *harness) runToEnd(projectID int64, extra obj) obj {
	h.t.Helper()
	task := h.task(projectID, "codex run", "do it", extra)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitUntil("the run to finish", func() bool {
		s := h.taskStatus(task.id())
		return s == "review" || s == "failed"
	})
	return h.get(fmt.Sprintf("/api/tasks/%d", task.id()))
}

// ---- the toggle --------------------------------------------------------------

func TestProjectDefaultAgentDrivesTasks(t *testing.T) {
	h := newHarness(t)
	p := h.project("codexproj", obj{"default_agent": "codex"})
	if got := h.task(p.id(), "inherit", "", nil).str("agent"); got != "codex" {
		t.Fatalf("agent: %q", got)
	}
}

func TestTaskAgentOverridesProjectDefault(t *testing.T) {
	h := newHarness(t)
	p := h.project("override", obj{"default_agent": "codex"})
	if got := h.task(p.id(), "explicit", "", obj{"agent": "claude"}).str("agent"); got != "claude" {
		t.Fatalf("agent: %q", got)
	}
}

func TestDefaultAgentDefaultsToClaude(t *testing.T) {
	h := newHarness(t)
	if got := h.task(h.seededProjectID(), "plain", "", nil).str("agent"); got != "claude" {
		t.Fatalf("agent: %q", got)
	}
}

func TestDefaultAgentIsPatchable(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	var patched obj
	h.decode("PATCH", fmt.Sprintf("/api/projects/%d", pid),
		obj{"default_agent": "codex"}, 200, &patched)
	if patched.str("default_agent") != "codex" {
		t.Fatalf("patched: %v", patched)
	}
	if code := h.status("PATCH", fmt.Sprintf("/api/projects/%d", pid),
		obj{"default_agent": "cursor"}); code != 422 {
		t.Errorf("an unknown agent must fail schema validation, got %d", code)
	}
}

func TestGatedModeRejectedForCodexIncludingViaProjectDefault(t *testing.T) {
	h := newHarness(t)
	p := h.project("gatedcodex", obj{"default_agent": "codex"})
	code, body := h.request("POST", "/api/tasks",
		obj{"project_id": p.id(), "title": "nope", "permission_mode": "default"}, nil)
	if code != 400 || !strings.Contains(string(body), "gated approvals") {
		t.Fatalf("got %d %s", code, body)
	}
}

// ---- dispatch ----------------------------------------------------------------

func TestCodexDispatchUsesCodexFlags(t *testing.T) {
	h := newHarness(t)
	p := h.project("cxdispatch", obj{"default_agent": "codex"})
	h.runToEnd(p.id(), nil)
	cmd := h.launchCmd()
	for _, want := range []string{"codex exec --json", "--sandbox workspace-write", "< /dev/null"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("launch missing %q: %s", want, cmd)
		}
	}
	if strings.Contains(cmd, "claude -p") {
		t.Errorf("a codex dispatch must not launch claude: %s", cmd)
	}
}

func TestCodexInteractiveSessionGetsProjectMCPAdditively(t *testing.T) {
	h := newHarness(t)
	p := h.project("cxinteractive", obj{"default_agent": "codex",
		"mcp": obj{"ops_tools": obj{"command": "python3", "args": []string{"-m", "ops"}}}})
	h.session(obj{"agent": "codex", "project_id": p.id()})
	cmd := h.launchCmd()
	if !strings.Contains(cmd, `codex -c`) || !strings.Contains(cmd, `mcp_servers.ops_tools.args=["-m","ops"]`) ||
		!strings.Contains(cmd, `mcp_servers.ops_tools.command="python3"`) {
		t.Fatalf("interactive Codex launch missing additive MCP overrides: %s", cmd)
	}
	if strings.Contains(cmd, "CODEX_HOME") {
		t.Fatalf("interactive launch must preserve the ambient Codex home: %s", cmd)
	}
}

func TestCodexGetsStagedContextLikeClaude(t *testing.T) {
	h := newHarness(t)
	house := writeFile(t, filepath.Join(t.TempDir(), "CLAUDE.md"), "codex should read this too")
	tid := h.firstTargetID()
	h.decode("PATCH", fmt.Sprintf("/api/targets/%d", tid),
		obj{"context_paths": []string{house}}, 200, nil)
	p := h.project("cxctx", obj{"default_agent": "codex"})

	h.runToEnd(p.id(), nil)

	if got := string(h.staged("/context/CLAUDE.md")); got != "codex should read this too" {
		t.Fatalf("staged: %q", got)
	}
	if !strings.Contains(string(h.staged("/prompt.md")), ".lectern/context/CLAUDE.md") {
		t.Error("the prompt prefix is how a non-claude agent learns about the bundle")
	}
}

func TestCodexLaunchSkipsClaudeOnlyFlags(t *testing.T) {
	h := newHarness(t)
	p := h.project("cxflags", obj{"default_agent": "codex",
		"mcp":         obj{"x": obj{"command": "x"}},
		"permissions": obj{"allow": []string{"Bash(ls*)"}}})
	h.runToEnd(p.id(), nil)
	cmd := h.launchCmd()
	// codex has no --settings/--mcp-config equivalent; passing them aborts it
	if strings.Contains(cmd, "--settings") || strings.Contains(cmd, "--mcp-config") {
		t.Fatalf("launch: %s", cmd)
	}
}

func TestCodexRejectsStrictMCPWithEmptyDeclaration(t *testing.T) {
	h := newHarness(t)
	p := h.project("cx-strict-empty", obj{"default_agent": "codex", "strict_mcp": true, "mcp": obj{}})
	result := h.runToEnd(p.id(), nil)
	if result.str("status") != "failed" {
		t.Fatalf("strict MCP must fail for Codex even when empty: %v", result)
	}
}

// ---- credentials -------------------------------------------------------------

func withAgentCreds(t *testing.T) func(*config.Config) {
	dir := t.TempDir()
	claude := writeFile(t, filepath.Join(dir, "creds.json"),
		`{"access_token": "t", "refresh_token": "t"}`)
	codex := writeFile(t, filepath.Join(dir, "auth.json"),
		`{"tokens": {"access_token": "codex-test"}}`)
	return func(c *config.Config) {
		c.ClaudeCredsPath = claude
		c.CodexCredsPath = codex
	}
}

func TestCodexCredentialsAreProvisionedToRemoteTargets(t *testing.T) {
	h := newHarness(t, withAgentCreds(t))
	tgt := h.post("/api/targets", obj{"name": "cx-ssh", "kind": "ssh", "host": "192.0.2.9"}, 201)
	p := h.post("/api/projects", obj{"name": "cxremote", "target_id": tgt.id(),
		"repo_path": "/mock/cxremote", "default_agent": "codex"}, 201)
	h.runToEnd(p.id(), nil)

	if !h.cmdLogHas("~/.codex/auth.json") {
		t.Fatalf("codex auth was never pushed: %v", h.mock().CmdLog())
	}
	// and claude's credentials are NOT pushed for a codex run
	if h.cmdLogHas(".claude/.credentials.json") {
		t.Error("a codex run must not push claude credentials")
	}
}

func TestClaudeRunStillProvisionsClaudeCredentials(t *testing.T) {
	h := newHarness(t, withAgentCreds(t))
	tgt := h.post("/api/targets", obj{"name": "cl-ssh", "kind": "ssh", "host": "192.0.2.10"}, 201)
	p := h.post("/api/projects",
		obj{"name": "clremote", "target_id": tgt.id(), "repo_path": "/mock/clremote"}, 201)
	h.runToEnd(p.id(), nil)
	if !h.cmdLogHas(".claude/.credentials.json") {
		t.Fatalf("claude auth was never pushed: %v", h.mock().CmdLog())
	}
}

func TestGeminiHasNoCredentialsToPushAtDispatch(t *testing.T) {
	h := newHarness(t, withAgentCreds(t))
	tgt := h.post("/api/targets", obj{"name": "gm-ssh", "kind": "ssh", "host": "192.0.2.11"}, 201)
	p := h.post("/api/projects", obj{"name": "gmremote", "target_id": tgt.id(),
		"repo_path": "/mock/gmremote", "default_agent": "gemini"}, 201)
	h.runToEnd(p.id(), nil)
	if h.cmdLogHas("auth.json") {
		t.Fatalf("gemini has no known credential file: %v", h.mock().CmdLog())
	}
}

// A follow-up attempt must not silently fall back to claude.
func TestAgentColumnSurvivesFollowups(t *testing.T) {
	h := newHarness(t)
	p := h.project("cxfollow", obj{"default_agent": "codex"})
	task := h.runToEnd(p.id(), nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	fresh, err := h.App.DB.Task(task.id())
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Agent != "codex" {
		t.Fatalf("agent: %q", fresh.Agent)
	}
}
