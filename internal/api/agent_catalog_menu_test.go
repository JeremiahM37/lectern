package api_test

import (
	"os"
	"path/filepath"
	"testing"
)

// TestAgentCatalogListsPresetsWithInstalledAndAdded checks GET
// /api/agents/catalog returns the preset list with the two computed fields
// Settings → Agents needs: whether the binary is on PATH, and whether an
// agent by that name is already registered (so "Add" can grey out a preset
// that would just collide).
func TestAgentCatalogListsPresetsWithInstalledAndAdded(t *testing.T) {
	h := newHarness(t)
	rows := h.getList("/api/agents/catalog")
	if len(rows) < 12 {
		t.Fatalf("expected at least a dozen catalog presets, got %d", len(rows))
	}
	byName := map[string]obj{}
	for _, r := range rows {
		name, _ := r["name"].(string)
		byName[name] = r
		if _, ok := r["installed"].(bool); !ok {
			t.Errorf("%s: catalog entry missing an installed bool", name)
		}
		if _, ok := r["added"].(bool); !ok {
			t.Errorf("%s: catalog entry missing an added bool", name)
		}
		if _, ok := r["source"].(string); !ok {
			t.Errorf("%s: catalog entry missing its source citation", name)
		}
	}
	aider, ok := byName["aider"]
	if !ok {
		t.Fatal("expected an aider preset in the catalog")
	}
	if aider["added"] != false {
		t.Errorf("aider should not be marked added before it is saved to /api/agents: %#v", aider)
	}
	// claude/codex/gemini are built-ins, not catalog entries: a preset named
	// after one would be unreachable via "Add from catalog" anyway.
	for _, builtin := range []string{"claude", "codex", "gemini"} {
		if _, ok := byName[builtin]; ok {
			t.Errorf("catalog should not duplicate the built-in agent %q", builtin)
		}
	}
}

// TestAgentCatalogAddedFlagFlipsOnceSaved proves "added" tracks the live
// /api/agents registry rather than being a static catalog property.
func TestAgentCatalogAddedFlagFlipsOnceSaved(t *testing.T) {
	h := newHarness(t)
	before := h.getList("/api/agents/catalog")
	var aiderBefore obj
	for _, r := range before {
		if r["name"] == "aider" {
			aiderBefore = r
		}
	}
	if aiderBefore["added"] != false {
		t.Fatalf("expected aider not yet added: %#v", aiderBefore)
	}
	h.decode("PUT", "/api/agents", []obj{{"name": "aider", "command": "aider", "model_flag": "--model"}}, 200, new([]obj))
	after := h.getList("/api/agents/catalog")
	var aiderAfter obj
	for _, r := range after {
		if r["name"] == "aider" {
			aiderAfter = r
		}
	}
	if aiderAfter["added"] != true {
		t.Fatalf("expected aider marked added once saved to /api/agents: %#v", aiderAfter)
	}
}

// TestAgentCapabilitiesEndpointDegradesPerAgent checks GET
// /api/agents/capabilities reports claude's resume/fork/model/yolo as
// available and a plain custom agent's as not, with reasons.
func TestAgentCapabilitiesEndpointDegradesPerAgent(t *testing.T) {
	h := newHarness(t)
	h.decode("PUT", "/api/agents", []obj{{"name": "bare", "command": "bare-cli"}}, 200, new([]obj))
	var out map[string]obj
	h.decode("GET", "/api/agents/capabilities", nil, 200, &out)
	claude, ok := out["claude"]
	if !ok {
		t.Fatal("expected claude in the capabilities response")
	}
	claudeCaps, _ := claude["capabilities"].(map[string]any)
	resume, _ := claudeCaps["resume"].(map[string]any)
	if resume["available"] != true {
		t.Errorf("claude should report resume available: %#v", resume)
	}
	bare, ok := out["bare"]
	if !ok {
		t.Fatal("expected the newly saved 'bare' agent in the capabilities response")
	}
	bareCaps, _ := bare["capabilities"].(map[string]any)
	bareResume, _ := bareCaps["resume"].(map[string]any)
	if bareResume["available"] != false {
		t.Errorf("a plain agent with no resume_args should report resume unavailable: %#v", bareResume)
	}
	if reason, _ := bareResume["reason"].(string); reason == "" {
		t.Error("expected a non-empty reason for an unavailable capability")
	}
}

// TestAgentMenuDefaultsAndRoundTrips exercises GET/PUT /api/agents/menu:
// unset falls back to a default, a valid PUT is reflected on the next GET,
// and an unknown agent name is rejected outright.
func TestAgentMenuDefaultsAndRoundTrips(t *testing.T) {
	h := newHarness(t)
	initial := h.get("/api/agents/menu")
	shown, _ := initial["agents"].([]any)
	if len(shown) == 0 {
		t.Fatal("expected a non-empty default agent menu")
	}
	h.decode("PUT", "/api/agents", []obj{{"name": "aider", "command": "aider"}}, 200, new([]obj))
	var saved obj
	h.decode("PUT", "/api/agents/menu", obj{"agents": []string{"aider", "claude"}}, 200, &saved)
	got, _ := saved["agents"].([]any)
	if len(got) != 2 || got[0] != "aider" || got[1] != "claude" {
		t.Fatalf("expected the menu order preserved as saved, got %#v", got)
	}
	roundTripped := h.get("/api/agents/menu")
	got2, _ := roundTripped["agents"].([]any)
	if len(got2) != 2 || got2[0] != "aider" || got2[1] != "claude" {
		t.Fatalf("menu did not round-trip: %#v", got2)
	}
	if status := h.status("PUT", "/api/agents/menu", obj{"agents": []string{"not-a-real-agent"}}); status != 422 {
		t.Fatalf("expected 422 for an unknown agent name in the menu, got %d", status)
	}
}

// TestAgentMenuDefaultTracksOverriddenBuiltinsByName locks in a real
// regression: overriding claude/codex with a same-named custom definition
// (a corrected command, or — as the whole e2e suite does — a local test
// stub) must not silently evict them from the default shown-agents list.
// ParseSpecs' own contract is that a same-named override is a transparent
// swap; the default menu has to honor that by matching sessions.Builtins()'
// fixed NAMES, not the merged spec's current Builtin flag.
func TestAgentMenuDefaultTracksOverriddenBuiltinsByName(t *testing.T) {
	h := newHarness(t)
	stub := filepath.Join(t.TempDir(), "stub-agent.sh")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\necho stub\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	h.decode("PUT", "/api/agents", []obj{
		{"name": "claude", "command": stub},
		{"name": "codex", "command": stub},
	}, 200, new([]obj))
	menu := h.get("/api/agents/menu")
	shown, _ := menu["agents"].([]any)
	names := map[string]bool{}
	for _, n := range shown {
		names[n.(string)] = true
	}
	if !names["claude"] || !names["codex"] {
		t.Fatalf("overriding claude/codex by name must not remove them from the default menu, got %#v", shown)
	}
}
