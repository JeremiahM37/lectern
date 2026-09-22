package api_test

import (
	"fmt"
	"strings"
	"testing"
)

func TestNoOpAgentListRoundTripPreservesBuiltinTaskCapabilities(t *testing.T) {
	h := newHarness(t)
	builtins := h.getList("/api/agents")
	if len(builtins) != 3 {
		t.Fatalf("expected three built-ins, got %d", len(builtins))
	}
	h.decode("PUT", "/api/agents", builtins, 200, nil)
	if got := h.App.DB.Setting("agents"); got != "[]" {
		t.Fatalf("untouched built-ins should remain implicit in settings, got %s", got)
	}
	p := h.project("builtin-roundtrip", obj{"default_agent": "claude"})
	if code := h.status("POST", "/api/tasks", obj{"project_id": p.id(), "title": "task", "prompt": "run"}); code != 201 {
		t.Fatalf("no-op list/save round-trip disabled builtin task: %d", code)
	}
}

func customAgent(name, command string) obj {
	return obj{"name": name, "command": "interactive-" + command, "model_flag": "--model",
		"task": obj{"command": command, "args": []string{"run", "--format", "json"},
			"prompt_template": "--prompt {prompt}", "output_mode": "plain"}}
}

func TestConfiguredCustomAgentRunsTaskWithItsBatchCommand(t *testing.T) {
	h := newHarness(t)
	definition := customAgent("opencode", "opencode")
	definition["env"] = obj{"OPENAI_API_KEY": "task-only-test-key"}
	h.decode("PUT", "/api/agents", []obj{definition}, 200, nil)
	p := h.project("opencode", obj{"default_agent": "opencode",
		"env": obj{"OPENAI_BASE_URL": "http://127.0.0.1:11434/v1"}})
	h.run(p.id(), "batch", "inspect this", obj{"model": "qwen-local"})
	cmd := h.launchCmd()
	for _, want := range []string{"opencode run --format json --model qwen-local", "--prompt", "< /dev/null",
		"OPENAI_BASE_URL=http://127.0.0.1:11434/v1", "OPENAI_API_KEY=task-only-test-key"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("custom launch missing %q: %s", want, cmd)
		}
	}
	if strings.Contains(cmd, "claude -p") || strings.Contains(cmd, "codex exec") {
		t.Fatalf("custom command received a built-in adapter: %s", cmd)
	}
}

func TestCustomAgentTaskConfigurationIsSnapshottedBeforeRegistryEdit(t *testing.T) {
	h := newHarness(t)
	h.decode("PUT", "/api/agents", []obj{customAgent("runner", "runner-v1")}, 200, nil)
	p := h.project("runner", obj{"default_agent": "runner"})
	task := h.task(p.id(), "one-shot", "do work", nil)
	h.decode("POST", fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200, nil)
	h.decode("PUT", "/api/agents", []obj{customAgent("runner", "runner-v2")}, 200, nil)
	h.waitUntil("custom task to finish", func() bool {
		status := h.taskStatus(task.id())
		return status == "review" || status == "failed"
	})
	if cmd := h.launchCmd(); !strings.Contains(cmd, "runner-v1") || strings.Contains(cmd, "runner-v2") {
		t.Fatalf("queued attempt was changed by registry edit: %s", cmd)
	}
}

func TestInteractiveOnlyCustomAgentCannotBeUsedForTaskOrRoutine(t *testing.T) {
	h := newHarness(t)
	h.decode("PUT", "/api/agents", []obj{{"name": "terminal-only", "command": "terminal-only"}}, 200, nil)
	p := h.project("terminal-only", obj{"default_agent": "terminal-only"})
	if code := h.status("POST", "/api/tasks", obj{"project_id": p.id(), "title": "no", "prompt": "no"}); code != 422 {
		t.Fatalf("session-only agent task got status %d", code)
	}
	if code := h.status("POST", "/api/routines", obj{"name": "no", "prompt": "no", "project_ids": []int64{p.id()}}); code != 400 {
		t.Fatalf("session-only agent routine got status %d", code)
	}
	if code := h.status("POST", "/api/sessions", obj{"agent": "terminal-only", "project_id": p.id()}); code != 201 {
		t.Fatalf("session-only agent session got status %d", code)
	}
}

func TestAgentProviderSecretsUseTypedRetentionMarkers(t *testing.T) {
	h := newHarness(t)
	first := obj{"name": "local-runner", "command": "runner", "model_flag": "--model",
		"env":  obj{"OPENAI_API_KEY": "test-only-secret", "OPENAI_BASE_URL": "http://127.0.0.1:11434/v1"},
		"task": obj{"command": "runner", "prompt_template": "stdin", "output_mode": "plain"}}
	var response []obj
	h.decode("PUT", "/api/agents", []obj{first}, 200, &response)
	var saved obj
	for _, item := range response {
		if item.str("name") == "local-runner" {
			saved = item
		}
	}
	if saved == nil {
		t.Fatal("saved custom agent missing")
	}
	env, ok := saved["env"].(map[string]any)
	if !ok {
		t.Fatalf("agent env view has type %T", saved["env"])
	}
	marker, ok := env["OPENAI_API_KEY"].(map[string]any)
	if !ok || marker["__lectern_retained"] == nil {
		t.Fatalf("provider key was not returned as a typed retention marker: %#v", env["OPENAI_API_KEY"])
	}
	if strings.Contains(fmt.Sprint(saved), "test-only-secret") {
		t.Fatal("agent response leaked provider secret")
	}

	updated := obj{"name": "local-runner", "command": "runner-updated", "model_flag": "--model",
		"env":  obj{"OPENAI_API_KEY": marker, "OPENAI_BASE_URL": "http://127.0.0.1:11434/v1"},
		"task": obj{"command": "runner-updated", "prompt_template": "stdin", "output_mode": "plain"}}
	h.decode("PUT", "/api/agents", []obj{updated}, 200, nil)
	p := h.project("local-runner", obj{"default_agent": "local-runner"})
	h.session(obj{"agent": "local-runner", "model": "local-model", "project_id": p.id()})
	if cmd := h.launchCmd(); !strings.Contains(cmd, "OPENAI_API_KEY=test-only-secret") {
		t.Fatalf("retained provider key did not reach the session launcher: %s", cmd)
	}
	for _, placeholder := range []string{"[secret retained]", "__KEEP__", "••••"} {
		if code := h.status("PUT", "/api/agents", []obj{{"name": "bad", "command": "runner", "env": obj{"OPENAI_API_KEY": placeholder}}}); code != 400 {
			t.Fatalf("placeholder %q should be rejected rather than stored as a key: %d", placeholder, code)
		}
	}
}

func TestCustomTaskRejectsUnmappedProjectMCP(t *testing.T) {
	h := newHarness(t)
	h.decode("PUT", "/api/agents", []obj{customAgent("plain-runner", "plain-runner")}, 200, nil)
	p := h.project("plain-runner", obj{"default_agent": "plain-runner",
		"mcp": obj{"tools": obj{"command": "mcp-tools"}}})
	task := h.task(p.id(), "mcp", "run", nil)
	h.decode("POST", fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200, nil)
	h.waitUntil("custom MCP task to fail explicitly", func() bool { return h.taskStatus(task.id()) == "failed" })
	got := h.get(fmt.Sprintf("/api/tasks/%d", task.id()))
	if !strings.Contains(got.sub("attempt").sub("result").str("error"), "MCP capability mapping") {
		t.Fatalf("unsupported custom MCP was not explained: %#v", got)
	}
}

func TestCustomClaudeOverrideCannotClaimGatedApprovals(t *testing.T) {
	h := newHarness(t)
	h.decode("PUT", "/api/agents", []obj{{"name": "claude", "command": "my-claude", "builtin": true,
		"task": obj{"command": "my-claude", "prompt_template": "stdin", "output_mode": "plain"}}}, 200, nil)
	p := h.project("custom-claude", obj{"default_agent": "claude", "default_permission_mode": "default"})
	if code := h.status("POST", "/api/tasks", obj{"project_id": p.id(), "title": "gated", "prompt": "run"}); code != 400 {
		t.Fatalf("custom claude override was allowed to claim gated approvals: %d", code)
	}
	if code := h.status("POST", "/api/routines", obj{"name": "gated", "prompt": "run", "agent": "claude",
		"permission_mode": "default", "project_ids": []int64{p.id()}}); code != 400 {
		t.Fatalf("custom claude override routine was allowed to claim gated approvals: %d", code)
	}
}

func TestConfiguredCustomAgentRunsRoutine(t *testing.T) {
	h := newHarness(t)
	h.decode("PUT", "/api/agents", []obj{customAgent("routine-runner", "routine-runner")}, 200, nil)
	p := h.project("routine-runner", obj{"default_agent": "routine-runner"})
	routine := h.post("/api/routines", obj{"name": "custom routine", "prompt": "inspect",
		"project_ids": []int64{p.id()}, "agent": "routine-runner", "dispatch": true}, 201)
	run := h.post(fmt.Sprintf("/api/routines/%d/run", int64(routine.num("id"))), obj{}, 200)
	tasks, ok := run["tasks"].([]any)
	if !ok || len(tasks) != 1 {
		t.Fatalf("custom routine did not create one task: %#v", run)
	}
	taskID := int64(tasks[0].(float64))
	h.waitUntil("custom routine task to finish", func() bool {
		status := h.taskStatus(taskID)
		return status == "review" || status == "done" || status == "failed"
	})
	if cmd := h.launchCmd(); !strings.Contains(cmd, "routine-runner run --format json") {
		t.Fatalf("routine used a built-in adapter: %s", cmd)
	}
}
