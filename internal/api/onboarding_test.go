package api_test

import "testing"

func TestOnboardingStatusShape(t *testing.T) {
	h := newHarness(t)
	body := h.get("/api/onboarding")
	if _, ok := body["agents"]; !ok {
		t.Errorf("missing agents: %v", body)
	}
	agents, ok := body["agents"].([]any)
	if !ok || len(agents) == 0 {
		t.Fatalf("agents should be a non-empty list: %v", body["agents"])
	}
	first, ok := agents[0].(map[string]any)
	if !ok {
		t.Fatalf("agent entries should be objects: %v", agents[0])
	}
	for _, key := range []string{"name", "found", "builtin"} {
		if _, ok := first[key]; !ok {
			t.Errorf("agent entry missing %q: %v", key, first)
		}
	}
	for _, key := range []string{"tmux", "git"} {
		check, ok := body[key].(map[string]any)
		if !ok {
			t.Fatalf("%s should be an object: %v", key, body[key])
		}
		if _, ok := check["ok"]; !ok {
			t.Errorf("%s missing ok: %v", key, check)
		}
	}
	if _, ok := body["projects"]; !ok {
		t.Errorf("missing projects count: %v", body)
	}
	if _, ok := body["sessions"]; !ok {
		t.Errorf("missing sessions count: %v", body)
	}
}

func TestOnboardingStatusCountsSeededProject(t *testing.T) {
	h := newHarness(t)
	h.seededProjectID()
	body := h.get("/api/onboarding")
	n, ok := body["projects"].(float64)
	if !ok || n < 1 {
		t.Errorf("expected at least one project after seeding: %v", body["projects"])
	}
}
