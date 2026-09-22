package api_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestDelegationIsOffUntilAWorkerIsChosenAndThePresetInstallsOne(t *testing.T) {
	h := newHarness(t)
	view := h.get("/api/delegation")
	settings, _ := view["settings"].(map[string]any)
	if settings["enabled"] != false || view["worker_ready"] != false {
		t.Fatalf("default view: %v", view)
	}
	if code := h.status("PUT", "/api/delegation", obj{"enabled": true}); code != 400 {
		t.Fatalf("enabling without a worker: %d", code)
	}
	if code := h.status("POST", "/api/delegation/preset", obj{}); code != 400 {
		t.Fatalf("preset without a key the first time: %d", code)
	}
	view = h.post("/api/delegation/preset", obj{"api_key": "sk-secret-value"}, 200)
	settings, _ = view["settings"].(map[string]any)
	if settings["worker_agent"] != "flash-builder" || settings["worker_model"] != "deepseek-flash" || view["worker_ready"] != true {
		t.Fatalf("after preset: %v", view)
	}
	// The key is in the registry, masked: GET /api/agents must not leak it.
	agents := h.getList("/api/agents")
	var found bool
	for _, a := range agents {
		if a["name"] == "flash-builder" {
			found = true
			env, _ := a["env"].(map[string]any)
			if raw, _ := env["DEEPSEEK_API_KEY"].(string); raw == "sk-secret-value" {
				t.Fatal("API key returned in clear by GET /api/agents")
			}
		}
	}
	if !found {
		t.Fatal("preset agent missing from the registry")
	}
	// Re-running the preset keeps the stored key.
	view = h.post("/api/delegation/preset", obj{}, 200)
	if view["worker_ready"] != true {
		t.Fatalf("re-run without a key: %v", view)
	}
	on, _ := h.request("PUT", "/api/delegation", obj{"enabled": true}, nil)
	if on != 200 {
		t.Fatalf("enable: %d", on)
	}
	view = h.get("/api/delegation")
	settings, _ = view["settings"].(map[string]any)
	if settings["enabled"] != true {
		t.Fatalf("not enabled: %v", view)
	}
}

func TestTaskWaitReportAndIntegrateShape(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	task := h.task(pid, "wait me", "do the thing", obj{})
	// Not started: the wait returns at once with done:true (backlog is not queued/running).
	res := h.get(fmt.Sprintf("/api/tasks/%d/wait?timeout=1", task.id()))
	if res["done"] != true {
		t.Fatalf("backlog wait: %v", res)
	}
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	// A running task with a one-second wait comes back done:false or, with the
	// mock agent, already finished; either is a valid shape.
	res = h.get(fmt.Sprintf("/api/tasks/%d/wait?timeout=1", task.id()))
	if _, ok := res["done"].(bool); !ok {
		t.Fatalf("wait shape: %v", res)
	}
	h.waitStatus(task.id(), "review")
	report := h.get(fmt.Sprintf("/api/tasks/%d/report", task.id()))
	if _, ok := report["report"].(string); !ok || report["status"] != "review" {
		t.Fatalf("report shape: %v", report)
	}
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/integrate", task.id()), obj{"workdir": "relative/path"}); code != 400 {
		t.Fatalf("relative workdir accepted: %d", code)
	}
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/integrate", task.id()), obj{"workdir": "/tmp/x", "mode": "sideways"}); code != 400 {
		t.Fatalf("bad mode accepted: %d", code)
	}
	if code := h.status("GET", "/api/tasks/999999/wait", nil); code != 404 {
		t.Fatalf("unknown task wait: %d", code)
	}
	_ = strings.TrimSpace
}

func TestOrchestratedTaskNeedsDelegationOnAndRunsTheLead(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	// Off: the board's Orchestrate entry must fail loudly, not queue a task
	// whose lead would only discover the worker is missing.
	code, body := h.request("POST", "/api/tasks", obj{"project_id": pid, "title": "Add /health", "orchestrate": true}, nil)
	if code != 400 || !strings.Contains(string(body), "Delegated builds ON") {
		t.Fatalf("orchestrate while off: %d %s", code, body)
	}
	h.post("/api/delegation/preset", obj{"api_key": "sk-secret-value"}, 200)
	h.request2("PUT", "/api/delegation", obj{"enabled": true, "lead_agent": "codex", "lead_model": "gpt-5.3-codex", "correction_cycles": 2}, 200)
	if code := h.status("PUT", "/api/delegation", obj{"lead_agent": "flash-builder"}); code != 400 {
		t.Fatalf("a custom agent cannot lead (no MCP mapping): %d", code)
	}
	view := h.get("/api/delegation")
	if view["orchestrate_ready"] != true {
		t.Fatalf("orchestrate_ready: %v", view)
	}
	task := h.post("/api/tasks", obj{"project_id": pid, "title": "Add /health", "prompt": "Add a /health endpoint returning 200", "orchestrate": true}, 201)
	if task["agent"] != "codex" || task["model"] != "gpt-5.3-codex" {
		t.Fatalf("lead agent/model not applied: %v %v", task["agent"], task["model"])
	}
	labels, _ := task["labels"].([]any)
	if len(labels) != 1 || labels[0] != "orchestrated" {
		t.Fatalf("labels: %v", labels)
	}
	prompt, _ := task["prompt"].(string)
	for _, want := range []string{"You are the LEAD", `delegate_build with project "`, "at most 2 correction", "REQUEST:\n\nAdd a /health endpoint returning 200"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("lead prompt lacks %q:\n%s", want, prompt)
		}
	}
	// An explicit agent on the request overrides the configured lead, and the
	// title stands in for an empty description (the quick bar sends only one).
	task = h.post("/api/tasks", obj{"project_id": pid, "title": "Rename the thing", "orchestrate": true, "agent": "claude"}, 201)
	if task["agent"] != "claude" || !strings.Contains(task["prompt"].(string), "REQUEST:\n\nRename the thing") {
		t.Fatalf("override/title fallback: %v", task)
	}
	// A plain task is untouched by the feature being on.
	task = h.post("/api/tasks", obj{"project_id": pid, "title": "plain", "prompt": "just do it"}, 201)
	if task["prompt"] != "just do it" || len(task["labels"].([]any)) != 0 {
		t.Fatalf("plain task changed: %v", task)
	}
}

func TestOrchestratedDispatchAttachesTheLecternMCPServer(t *testing.T) {
	// Codex lead: the server arrives as additive -c overrides with the tool
	// timeout a 25-minute build wait needs.
	h := newHarness(t)
	h.post("/api/delegation/preset", obj{"api_key": "sk-secret-value"}, 200)
	h.request2("PUT", "/api/delegation", obj{"enabled": true}, 200)
	p := h.project("orch-codex", obj{"default_agent": "codex"})
	h.runToEnd(p.id(), obj{"orchestrate": true})
	cmd := h.launchCmd()
	for _, want := range []string{`mcp_servers.lectern.args=["mcp"]`, `mcp_servers.lectern.command=`,
		`mcp_servers.lectern.tool_timeout_sec=3600`, `mcp_servers.lectern.env={LECTERN_API = "http://`} {
		if !strings.Contains(cmd, want) {
			t.Errorf("codex lead launch missing %q: %s", want, cmd)
		}
	}
}

func TestOrchestratedClaudeLeadGetsPrivateMCPConfigAndTimeout(t *testing.T) {
	h := newHarness(t)
	h.post("/api/delegation/preset", obj{"api_key": "sk-secret-value"}, 200)
	h.request2("PUT", "/api/delegation", obj{"enabled": true}, 200)
	p := h.project("orch-claude", obj{"default_agent": "claude"})
	h.runToEnd(p.id(), obj{"orchestrate": true})
	cmd := h.launchCmd()
	for _, want := range []string{"--mcp-config", "MCP_TOOL_TIMEOUT=3600000"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("claude lead launch missing %q: %s", want, cmd)
		}
	}
	if strings.Contains(cmd, "--strict-mcp-config") {
		t.Errorf("the lead must keep the host's other servers: %s", cmd)
	}
	// A plain task on the same project gets no server and no timeout.
	h2 := newHarness(t)
	p2 := h2.project("plain-claude", obj{"default_agent": "claude"})
	h2.runToEnd(p2.id(), nil)
	if plain := h2.launchCmd(); strings.Contains(plain, "--mcp-config") || strings.Contains(plain, "MCP_TOOL_TIMEOUT") {
		t.Errorf("plain task inherited the lead's MCP: %s", plain)
	}
}

func TestDelegateBuildResolvesALeadsTaskWorktreeToItsProject(t *testing.T) {
	h := newHarness(t)
	h.post("/api/delegation/preset", obj{"api_key": "sk-secret-value"}, 200)
	h.request2("PUT", "/api/delegation", obj{"enabled": true}, 200)
	p := h.project("orch-wt", obj{"default_agent": "codex"})
	lead := h.runToEnd(p.id(), obj{"orchestrate": true})
	att, _ := lead["attempt"].(map[string]any)
	worktree, _ := att["worktree_path"].(string)
	if worktree == "" {
		t.Fatalf("lead has no worktree: %v", lead)
	}
	// The lead names its worktree, not the registered repo path, and gets a
	// worker task on the same project; a timeout of 1s keeps the test short
	// (the mock worker may or may not have finished, both are valid).
	frames := mcpCall(t, h,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		fmt.Sprintf(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"delegate_build","arguments":{"workdir":%q,"title":"worker bundle","brief":"do the bundle","timeout_s":1}}}`, worktree))
	text := toolText(t, frames[len(frames)-1])
	if strings.Contains(text, "no project is registered") {
		t.Fatalf("worktree not resolved: %s", text)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("delegate_build reply: %s", text)
	}
	id, _ := out["task_id"].(float64)
	if id == 0 {
		t.Fatalf("no task id in %v", out)
	}
	worker := h.get(fmt.Sprintf("/api/tasks/%d", int64(id)))
	if worker["project_id"] != p["id"] || worker["agent"] != "flash-builder" {
		t.Fatalf("worker task: project %v agent %v", worker["project_id"], worker["agent"])
	}
	// An unknown path is still refused with the registered names.
	frames = mcpCall(t, h,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"delegate_build","arguments":{"workdir":"/nowhere/at/all","title":"x","brief":"y"}}}`)
	if text := toolText(t, frames[len(frames)-1]); !strings.Contains(text, "no project is registered at /nowhere/at/all") {
		t.Fatalf("unknown path: %s", text)
	}
}
