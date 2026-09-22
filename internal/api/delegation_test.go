package api_test

import (
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
