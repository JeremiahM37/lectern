package api_test

// Context parity: a dispatched agent gets the same knowledge, tools and
// grantable permissions a local interactive session would — on EVERY target
// kind. Each test asserts against what actually reached the target (the mock
// filesystem and command log), not against the config that was requested.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/internal/config"
)

func allowList(settings obj) []string {
	perms, _ := settings["permissions"].(map[string]any)
	items, _ := perms["allow"].([]any)
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.(string))
	}
	return out
}

func permsList(settings obj, key string) []string {
	perms, _ := settings["permissions"].(map[string]any)
	items, _ := perms[key].([]any)
	out := make([]string, 0, len(items))
	for _, i := range items {
		out = append(out, i.(string))
	}
	return out
}

func hasStr(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

// ---- context bundle ----------------------------------------------------------

func TestTargetContextIsStagedAndAnnounced(t *testing.T) {
	h := newHarness(t)
	house := writeFile(t, filepath.Join(t.TempDir(), "CLAUDE.md"), "never hardcode IPs")
	tid := h.firstTargetID()
	h.decode("PATCH", fmt.Sprintf("/api/targets/%d", tid),
		obj{"context_paths": []string{house}}, 200, nil)
	p := h.post("/api/projects",
		obj{"name": "ctx", "target_id": tid, "repo_path": "/mock/ctx"}, 201)

	h.run(p.id(), "parity", "do the thing", nil)

	if got := string(h.staged("/.lectern/context/CLAUDE.md")); got != "never hardcode IPs" {
		t.Fatalf("staged file: %q", got)
	}
	prompt := string(h.staged("/.lectern/prompt.md"))
	if !strings.Contains(prompt, ".lectern/context/CLAUDE.md") {
		t.Error("the prompt must point at the bundle")
	}
	if strings.Index(prompt, "## Context") > strings.Index(prompt, "do the thing") {
		t.Error("context has to come before the task, or it is read too late")
	}
	if !strings.Contains(string(h.staged("/.lectern/context/INDEX.md")), "CLAUDE.md") {
		t.Error("the index must list what was staged")
	}
}

func TestProjectContextStacksOnTargetContext(t *testing.T) {
	h := newHarness(t)
	dir := t.TempDir()
	hostFile := writeFile(t, filepath.Join(dir, "host.md"), "host rules")
	projFile := writeFile(t, filepath.Join(dir, "proj.md"), "project rules")
	tid := h.firstTargetID()
	h.decode("PATCH", fmt.Sprintf("/api/targets/%d", tid),
		obj{"context_paths": []string{hostFile}}, 200, nil)
	p := h.post("/api/projects", obj{"name": "stack", "target_id": tid,
		"repo_path": "/mock/stack", "context_paths": []string{projFile}}, 201)

	h.run(p.id(), "parity", "do the thing", nil)

	if got := string(h.staged("/context/host.md")); got != "host rules" {
		t.Errorf("target context: %q", got)
	}
	if got := string(h.staged("/context/proj.md")); got != "project rules" {
		t.Errorf("project context: %q", got)
	}
}

func TestMissingContextIsReportedToTheAgent(t *testing.T) {
	h := newHarness(t)
	absent := filepath.Join(t.TempDir(), "absent.md")
	p := h.post("/api/projects", obj{"name": "gone", "target_id": h.firstTargetID(),
		"repo_path": "/mock/gone", "context_paths": []string{absent}}, 201)

	h.run(p.id(), "parity", "do the thing", nil)

	prompt := string(h.staged("/.lectern/prompt.md"))
	if !strings.Contains(prompt, "could NOT be staged") || !strings.Contains(prompt, "absent.md") {
		t.Fatalf("a missing file must be named, not swallowed:\n%s", prompt)
	}
}

func TestNoContextConfiguredLeavesPromptClean(t *testing.T) {
	h := newHarness(t)
	h.run(h.seededProjectID(), "parity", "do the thing", nil)
	if strings.Contains(string(h.staged("/.lectern/prompt.md")), "## Context") {
		t.Error("an unconfigured bundle must add nothing to the prompt")
	}
	for path := range h.mock().Files() {
		if strings.Contains(path, "/.lectern/context/") {
			t.Errorf("nothing should have been staged: %s", path)
		}
	}
}

// ---- MCP ---------------------------------------------------------------------

func TestProjectMCPReachesTheAgent(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{"name": "mcp", "target_id": h.firstTargetID(),
		"repo_path": "/mock/mcp", "strict_mcp": true,
		"mcp": obj{"homelab": obj{"command": "python3", "args": []string{"-m", "srv"}}}}, 201)

	h.run(p.id(), "parity", "do the thing", nil)

	cmd := h.launchCmd()
	if strings.Contains(cmd, "--mcp-config .lectern/mcp.json") ||
		!strings.Contains(cmd, "--mcp-config /tmp/lectern-mcp-state/lectern/mcp/") ||
		!strings.Contains(cmd, "--strict-mcp-config") {
		t.Fatalf("launch: %s", cmd)
	}
}

func TestMCPAcceptsAFullMCPServersDocument(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{"name": "mcpfull", "target_id": h.firstTargetID(),
		"repo_path": "/mock/mcpfull",
		"mcp":       obj{"mcpServers": obj{"x": obj{"command": "x"}}}}, 201)

	h.run(p.id(), "parity", "do the thing", nil)

	if strings.Contains(h.launchCmd(), "--mcp-config .lectern/mcp.json") ||
		!strings.Contains(h.launchCmd(), "/tmp/lectern-mcp-state/lectern/mcp/") {
		t.Fatalf("MCP config must stay outside the worktree: %s", h.launchCmd())
	}
	if strings.Contains(h.launchCmd(), "--strict-mcp-config") {
		t.Error("strict must stay opt-in")
	}
}

func TestNoMCPConfiguredPassesNoFlag(t *testing.T) {
	h := newHarness(t)
	h.run(h.seededProjectID(), "parity", "do the thing", nil)
	if strings.Contains(h.launchCmd(), "--mcp-config") {
		t.Fatalf("launch: %s", h.launchCmd())
	}
}

func TestStrictMCPEmptyClaudeStillUsesPrivateEmptyDocument(t *testing.T) {
	h := newHarness(t)
	p := h.project("strict-empty", obj{"strict_mcp": true, "mcp": obj{}})
	h.run(p.id(), "strict empty", "do the thing", nil)
	cmd := h.launchCmd()
	if strings.Contains(cmd, "--mcp-config .lectern/mcp.json") ||
		!strings.Contains(cmd, "--mcp-config /tmp/lectern-mcp-state/lectern/mcp/") ||
		!strings.Contains(cmd, "--strict-mcp-config") {
		t.Fatalf("strict empty Claude launch: %s", cmd)
	}
}

// ---- permissions -------------------------------------------------------------

func TestPermissionRulesShipInUngatedMode(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{"name": "perms", "target_id": h.firstTargetID(),
		"repo_path": "/mock/perms",
		"permissions": obj{"allow": []string{"Bash(pytest*)"},
			"deny": []string{"Bash(rm *)"}}}, 201)

	h.run(p.id(), "parity", "do the thing", obj{"permission_mode": "acceptEdits"})

	settings := h.stagedJSON("/.lectern/settings.json")
	if got := allowList(settings); len(got) != 1 || got[0] != "Bash(pytest*)" {
		t.Fatalf("allow: %v", got)
	}
	// rules without the approval gate: this is how an acceptEdits run gets Bash
	if _, ok := settings["hooks"]; ok {
		t.Error("an ungated run must not get the hook")
	}
	if !strings.Contains(h.launchCmd(), "--settings .lectern/settings.json") {
		t.Errorf("launch: %s", h.launchCmd())
	}
}

func TestGatedModeInterceptsEveryToolByDefault(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	task := h.task(pid, "parity", "do the thing", obj{"permission_mode": "default"})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "review")

	settings := h.stagedJSON("/.lectern/settings.json")
	hooks := settings.sub("hooks")
	pre, _ := hooks["PreToolUse"].([]any)
	first, _ := pre[0].(map[string]any)
	// anything the matcher misses is denied with no prompt and no explanation
	if first["matcher"] != "*" {
		t.Fatalf("matcher: %v", first["matcher"])
	}
}

func TestGateMatcherIsNarrowablePerProject(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{"name": "narrow", "target_id": h.firstTargetID(),
		"repo_path": "/mock/narrow", "gate_matcher": "Bash|Edit"}, 201)
	task := h.task(p.id(), "parity", "do the thing", obj{"permission_mode": "default"})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "review")

	settings := h.stagedJSON("/.lectern/settings.json")
	pre, _ := settings.sub("hooks")["PreToolUse"].([]any)
	first, _ := pre[0].(map[string]any)
	if first["matcher"] != "Bash|Edit" {
		t.Fatalf("matcher: %v", first["matcher"])
	}
}

func TestTypoInPermissionKeysIsRejectedAtConfigTime(t *testing.T) {
	h := newHarness(t)
	code, body := h.request("POST", "/api/projects",
		obj{"name": "typo", "target_id": h.firstTargetID(), "repo_path": "/mock/typo",
			"permissions": obj{"allowed": []string{"Bash"}}}, nil)
	if code != 400 || !strings.Contains(string(body), "allowed") {
		t.Fatalf("a typo'd key must fail now, not as a mystery denial: %d %s", code, body)
	}
}

// ---- memory ------------------------------------------------------------------

func TestMemoryDirLinksTheAttemptSession(t *testing.T) {
	h := newHarness(t)
	tid := h.firstTargetID()
	h.decode("PATCH", fmt.Sprintf("/api/targets/%d", tid),
		obj{"memory_dir": "/store/memory"}, 200, nil)
	p := h.post("/api/projects",
		obj{"name": "mem", "target_id": tid, "repo_path": "/mock/mem"}, 201)

	h.run(p.id(), "parity", "do the thing", nil)

	var link string
	for _, c := range h.mock().CmdLog() {
		if strings.Contains(c, "ln -sfn") {
			link = c
		}
	}
	if link == "" {
		t.Fatal("no memory link was attempted")
	}
	if !strings.Contains(link, "/store/memory") {
		t.Errorf("link target: %s", link)
	}
	// keyed to the git MAIN worktree, resolved on the target — not the cwd slug
	if !strings.Contains(link, "--git-common-dir") ||
		!strings.Contains(link, `$HOME/.claude/projects/$slug`) {
		t.Errorf("link command: %s", link)
	}
}

func TestNoMemoryDirTouchesNothing(t *testing.T) {
	h := newHarness(t)
	h.run(h.seededProjectID(), "parity", "do the thing", nil)
	for _, c := range h.mock().CmdLog() {
		if strings.Contains(c, "ln -sfn") {
			t.Fatalf("nothing should have been linked: %s", c)
		}
	}
}

// ---- both launch paths --------------------------------------------------------

func TestSandboxDispatchGetsTheSameContextAndMemory(t *testing.T) {
	h := newHarness(t, withCreds(t))
	house := writeFile(t, filepath.Join(t.TempDir(), "CLAUDE.md"), "sandbox needs this too")
	tgt := h.post("/api/targets", obj{"name": "sb-parity", "kind": "sandbox",
		"host": "110", "sandbox": true, "context_paths": []string{house}}, 201)
	p := h.post("/api/projects", obj{"name": "sbctx", "target_id": tgt.id(),
		"repo_path":   "/root/demo",
		"permissions": obj{"allow": []string{"Bash(ls*)"}}}, 201)
	if _, err := h.App.DB.InsertNote(p.id(), "remember the sandbox", nil); err != nil {
		t.Fatal(err)
	}

	task := h.task(p.id(), "parity", "do the thing",
		obj{"permission_mode": "bypassPermissions"})
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "review")

	// the sandbox path used to skip project memory entirely
	if !strings.Contains(string(h.staged("/.lectern/prompt.md")), "remember the sandbox") {
		t.Error("project memory never reached the sandbox")
	}
	if got := string(h.staged("/.lectern/context/CLAUDE.md")); got != "sandbox needs this too" {
		t.Errorf("staged context: %q", got)
	}
	settings := h.stagedJSON("/.lectern/settings.json")
	if got := allowList(settings); len(got) != 1 || got[0] != "Bash(ls*)" {
		t.Errorf("allow: %v", got)
	}
}

// ---- capability parity --------------------------------------------------------
// A dispatched agent used to be handed a memory store it could not read and MCP
// servers it could not call. Both failed silently: the run just came back worse.

func TestMemoryDirIsReachableNotJustLinked(t *testing.T) {
	h := newHarness(t)
	tid := h.firstTargetID()
	h.decode("PATCH", fmt.Sprintf("/api/targets/%d", tid),
		obj{"memory_dir": "/store/memory"}, 200, nil)
	p := h.post("/api/projects",
		obj{"name": "memreach", "target_id": tid, "repo_path": "/mock/memreach"}, 201)

	h.run(p.id(), "parity", "do the thing", nil)

	dirs := permsList(h.stagedJSON("/.lectern/settings.json"), "additionalDirectories")
	if !hasStr(dirs, "/store/memory") {
		t.Fatalf("a store outside the worktree is unreadable unless the sandbox allows it: %v", dirs)
	}
}

// withHostMCP fakes the control plane user's own Claude config.
func withHostMCP(t *testing.T, servers ...string) func(*config.Config) {
	entries := make([]string, 0, len(servers))
	for _, s := range servers {
		entries = append(entries, fmt.Sprintf(`%q:{"command":"x"}`, s))
	}
	body := fmt.Sprintf(`{"mcpServers":{%s}}`, strings.Join(entries, ","))
	path := writeFile(t, filepath.Join(t.TempDir(), "claude.json"), body)
	return func(c *config.Config) { c.HostClaudeConfig = path }
}

func TestParityProfileGrantsBashAndHostMCPServers(t *testing.T) {
	h := newHarness(t, withHostMCP(t, "grimoire", "homelab"))
	p := h.post("/api/projects", obj{"name": "par", "target_id": h.firstTargetID(),
		"repo_path": "/mock/par", "capability_profile": "parity"}, 201)

	h.run(p.id(), "parity", "do the thing", nil)

	allow := allowList(h.stagedJSON("/.lectern/settings.json"))
	// bare Bash, not Bash(...): a prefix rule makes the CLI split compound
	// commands and refuse the parts it cannot match
	if !hasStr(allow, "Bash") {
		t.Errorf("parity must grant bare Bash: %v", allow)
	}
	if !hasStr(allow, "mcp__grimoire") || !hasStr(allow, "mcp__homelab") {
		t.Errorf("host MCP servers must be enumerated: %v", allow)
	}
}

func TestRestrictedProfileStaysEmpty(t *testing.T) {
	// the default must not silently widen an existing install's permissions
	h := newHarness(t, withHostMCP(t, "grimoire"))
	p := h.post("/api/projects",
		obj{"name": "restr", "target_id": h.firstTargetID(), "repo_path": "/mock/restr"}, 201)

	h.run(p.id(), "parity", "do the thing", nil)

	if got := h.stagedJSON("/.lectern/settings.json"); len(got) != 0 {
		t.Fatalf("restricted must grant nothing implicitly: %v", got)
	}
}

func TestExplicitDenyBeatsTheProfile(t *testing.T) {
	h := newHarness(t, withHostMCP(t, "grimoire"))
	p := h.post("/api/projects", obj{"name": "deny", "target_id": h.firstTargetID(),
		"repo_path": "/mock/deny", "capability_profile": "parity",
		"permissions": obj{"deny": []string{"Bash"}}}, 201)

	h.run(p.id(), "parity", "do the thing", nil)

	settings := h.stagedJSON("/.lectern/settings.json")
	if hasStr(allowList(settings), "Bash") {
		t.Error("the profile re-granted something the operator denied")
	}
	if !hasStr(permsList(settings, "deny"), "Bash") {
		t.Error("the explicit deny was dropped")
	}
}

func TestCapabilityEndpointStatesTheResolvedView(t *testing.T) {
	h := newHarness(t, withHostMCP(t, "grimoire"))
	p := h.post("/api/projects", obj{"name": "cap", "target_id": h.firstTargetID(),
		"repo_path": "/mock/cap", "capability_profile": "parity"}, 201)

	got := h.get(fmt.Sprintf("/api/projects/%d/capability", p.id()))
	if got.str("profile") != "parity" {
		t.Fatalf("profile: %v", got)
	}
	servers, _ := got["mcp_servers"].([]any)
	if len(servers) != 1 || servers[0] != "grimoire" {
		t.Errorf("mcp_servers: %v", got["mcp_servers"])
	}
	allow, _ := got["allow"].([]any)
	found := false
	for _, a := range allow {
		if a == "Bash" {
			found = true
		}
	}
	if !found {
		t.Errorf("allow: %v", allow)
	}
	notes, _ := got["notes"].([]any)
	if len(notes) == 0 {
		t.Error("the gaps must be named, not left for the operator to infer")
	}
}

// ---- default permission mode --------------------------------------------------
// A project whose blast radius is infrastructure should gate by default, or a
// quick-dispatch from the phone silently runs unattended with full parity.

func TestTaskInheritsTheProjectsDefaultPermissionMode(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{"name": "infra", "target_id": h.firstTargetID(),
		"repo_path": "/mock/infra", "default_permission_mode": "default"}, 201)
	task := h.task(p.id(), "no mode given", "", nil)
	if task.str("permission_mode") != "default" {
		t.Fatalf("permission mode: %v", task.str("permission_mode"))
	}
}

func TestExplicitPermissionModeStillWins(t *testing.T) {
	h := newHarness(t)
	p := h.post("/api/projects", obj{"name": "infra2", "target_id": h.firstTargetID(),
		"repo_path": "/mock/infra2", "default_permission_mode": "default"}, 201)
	task := h.task(p.id(), "explicit", "", obj{"permission_mode": "plan"})
	if task.str("permission_mode") != "plan" {
		t.Fatalf("permission mode: %v", task.str("permission_mode"))
	}
}

func TestNoProjectDefaultKeepsAcceptEdits(t *testing.T) {
	// existing installs must not start gating tasks that never gated before
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "unchanged", "", nil)
	if task.str("permission_mode") != "acceptEdits" {
		t.Fatalf("permission mode: %v", task.str("permission_mode"))
	}
}
