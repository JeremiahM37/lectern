package api_test

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestLaunchProfilesCRUDAndCapturedSessionSettings(t *testing.T) {
	h := newHarness(t)
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "profiles", Kind: "mock"})
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "original-codex", "env": obj{"SAME": "agent", "AGENT_ONLY": "kept"}}}, 200, nil)
	project, err := h.App.DB.InsertProject(&store.Project{Name: "profile project", TargetID: target.ID, RepoPath: "/workspace", EnvJSON: `{"SAME":"project","PROJECT_ONLY":"kept"}`})
	if err != nil {
		t.Fatal(err)
	}
	input := obj{"name": "Work account", "agent": "codex", "command": "profile-codex", "model": "profile-model", "env_json": `{"SAME":"profile","CODEX_HOME":"/profile/home","SECRET":"private-sentinel"}`}
	var profile obj
	h.decode("POST", "/api/launch-profiles", input, 201, &profile)
	id := int64(profile["id"].(float64))
	url := fmt.Sprintf("/api/launch-profiles/%d", id)
	duplicate := obj{"name": "work ACCOUNT", "agent": "codex"}
	h.decode("POST", "/api/launch-profiles", duplicate, 409, nil)
	for _, invalid := range []obj{{"name": "", "agent": "codex"}, {"name": "bad", "agent": "missing"}, {"name": "bad", "agent": "codex", "env_json": `{"BAD-NAME":"x"}`}, {"name": "bad", "agent": "codex", "env_json": `null`}} {
		h.decode("POST", "/api/launch-profiles", invalid, 422, nil)
	}
	var row obj
	h.decode("POST", "/api/sessions", obj{"name": "profile session", "project_id": project.ID, "profile_id": id}, 201, &row)
	if row["agent"] != "codex" || row["model"] != "profile-model" || row["launch_profile"] != "Work account" || strings.Contains(fmt.Sprint(row), "private-sentinel") {
		t.Fatalf("wrong public session metadata: %v", row)
	}
	saved, _ := h.App.DB.Session(int64(row["id"].(float64)))
	var cfg sessions.LaunchConfiguration
	if err := json.Unmarshal([]byte(saved.LaunchConfigJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Spec.Command != "profile-codex" || cfg.Spec.Env["SAME"] != "profile" || cfg.Spec.Env["PROJECT_ONLY"] != "kept" || cfg.Spec.Env["AGENT_ONLY"] != "kept" || cfg.ProfileID != id {
		t.Fatal("profile did not override defaults while preserving unrelated settings")
	}
	input["name"], input["command"], input["env_json"] = "Changed", "new-command", `{}`
	h.decode("PUT", url, input, 200, nil)
	h.decode("DELETE", url, nil, 200, nil)
	h.decode("DELETE", url, nil, 404, nil)
	continued, err := h.App.Sessions.SessionLaunchConfiguration(saved)
	if err != nil || continued.Spec.Command != cfg.Spec.Command || continued.Spec.Env["SECRET"] != "private-sentinel" || continued.ProfileName != "Work account" {
		t.Fatal("profile edits/deletion changed an existing continuation")
	}
	var list []obj
	h.decode("GET", "/api/launch-profiles", nil, 200, &list)
	if len(list) != 0 {
		t.Fatal(list)
	}
	h.decode("POST", "/api/sessions", obj{"workdir": "/workspace", "target_id": target.ID, "profile_id": id}, 409, nil)
	all, _ := h.App.DB.Sessions(true)
	if len(all) != 1 {
		t.Fatal("missing profile created a phantom session")
	}
}

func TestLaunchProfileSearchBeforeFirstSession(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, func(c *config.Config) { c.Mock = false })
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "profile search", Kind: "local"})
	home, cache := t.TempDir(), t.TempDir()
	cid := "11111111-2222-4333-8444-555555555555"
	writeSearchFixture(t, home, t.TempDir(), cid, "named profile sentinel")
	h.decode("PUT", "/api/agents", []obj{{"name": "codex", "command": "codex", "env": obj{"CODEX_HOME": t.TempDir(), "LECTERN_NATIVE_SEARCH_CACHE": cache}}}, 200, nil)
	h.decode("POST", "/api/launch-profiles", obj{"name": "Imported account", "agent": "codex", "env_json": store.J(map[string]string{"CODEX_HOME": home, "LECTERN_NATIVE_SEARCH_CACHE": cache})}, 201, nil)
	var job obj
	h.decode("POST", "/api/conversation-search", obj{"query": "named profile sentinel", "target_id": target.ID, "agent": "codex"}, 202, &job)
	result := waitNativeSearch(t, h, job["id"].(string))
	hits := result["results"].([]any)
	if len(hits) != 1 {
		t.Fatal(result)
	}
	var read obj
	h.decode("GET", fmt.Sprintf("/api/conversation-search/%s/results/%s", job["id"], hits[0].(map[string]any)["id"]), nil, 200, &read)
	if !strings.Contains(fmt.Sprint(read["fork_options"]), "Launch profile: Imported account") {
		t.Fatal("named settings missing from global fork choices")
	}
	rows, _ := h.App.DB.Sessions(true)
	if len(rows) != 0 {
		t.Fatal("profile search created session records")
	}
}

func TestLaunchProfileBriefingValidationAndLegacyPreservation(t *testing.T) {
	h := newHarness(t)
	input := obj{"name": "Briefed", "agent": "codex", "description": "A focused builder",
		"instructions": "Line one.\nLine two."}
	var created obj
	h.decode("POST", "/api/launch-profiles", input, 201, &created)
	id := created.id()
	url := fmt.Sprintf("/api/launch-profiles/%d", id)
	if created.str("description") != "A focused builder" || created.str("instructions") != "Line one.\nLine two." {
		t.Fatalf("briefing fields not stored: %v", created)
	}
	// A legacy client PUTs the fields it knows and omits the briefing entirely.
	h.decode("PUT", url, obj{"name": "Renamed", "agent": "codex", "env_json": `{}`}, 200, nil)
	stored, err := h.App.DB.LaunchProfile(id)
	if err != nil || stored.Instructions != "Line one.\nLine two." || stored.Description != "A focused builder" {
		t.Fatalf("legacy PUT erased unseen briefing: %+v %v", stored, err)
	}
	// An explicit empty string is a deliberate clear.
	h.decode("PUT", url, obj{"name": "Renamed", "agent": "codex", "env_json": `{}`, "description": "", "instructions": ""}, 200, nil)
	stored, err = h.App.DB.LaunchProfile(id)
	if err != nil || stored.Instructions != "" || stored.Description != "" {
		t.Fatalf("explicit empty did not clear the briefing: %+v %v", stored, err)
	}
	for _, invalid := range []obj{
		{"name": "bad", "agent": "codex", "description": strings.Repeat("d", 2001)},
		{"name": "bad", "agent": "codex", "instructions": strings.Repeat("i", 16001)},
		{"name": "bad", "agent": "codex", "instructions": "before\x00after"},
		{"name": "bad", "agent": "codex", "description": "before\x00after"},
	} {
		h.decode("POST", "/api/launch-profiles", invalid, 422, nil)
	}
}

func TestLaunchProfilePresetsShipAsEmbeddedCatalog(t *testing.T) {
	h := newHarness(t)
	presets := h.getList("/api/launch-profile-presets")
	if len(presets) != 4 {
		t.Fatalf("expected four presets, got %d", len(presets))
	}
	want := map[string]bool{"lean-builder": false, "reviewed-delivery": false, "debugging-team": false, "research-plan": false}
	for _, p := range presets {
		key := p.str("key")
		if _, ok := want[key]; !ok {
			t.Fatalf("unexpected preset key %q", key)
		}
		want[key] = true
		if p.str("name") == "" || p.str("agent") == "" || p.str("description") == "" || p.str("instructions") == "" {
			t.Fatalf("preset %q is incomplete: %v", key, p)
		}
	}
	for key, seen := range want {
		if !seen {
			t.Fatalf("preset %q missing", key)
		}
	}
}

func TestLaunchProfileBriefingReachesArgvOnceAndStaysOutOfSessionResponse(t *testing.T) {
	h := newHarness(t)
	target, _ := h.App.DB.InsertTarget(&store.Target{Name: "briefing", Kind: "mock"})
	project, err := h.App.DB.InsertProject(&store.Project{Name: "briefing project", TargetID: target.ID, RepoPath: "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	instructions := "BRIEFING-SENTINEL start from the smallest failing test"
	var profile obj
	h.decode("POST", "/api/launch-profiles", obj{"name": "Briefed", "agent": "codex", "instructions": instructions}, 201, &profile)
	var row obj
	h.decode("POST", "/api/sessions", obj{"name": "briefed session", "project_id": project.ID, "profile_id": profile.id(),
		"prime": "CALLER-PRIME-SENTINEL then explain your plan"}, 201, &row)
	if row.str("launch_profile") != "Briefed" {
		t.Fatalf("profile missing from session view: %v", row)
	}
	public, _ := json.Marshal(row)
	if strings.Contains(string(public), "BRIEFING-SENTINEL") || strings.Contains(string(public), "first_line") {
		t.Fatalf("briefing leaked into the public session response: %s", public)
	}
	var launchCmd string
	for _, cmd := range h.mock().CmdLog() {
		if strings.HasPrefix(cmd, "tmux new-session") {
			launchCmd = cmd
		}
	}
	if launchCmd == "" {
		t.Fatal("no launch command was recorded")
	}
	if strings.Count(launchCmd, "BRIEFING-SENTINEL") != 1 {
		t.Fatalf("briefing was not delivered exactly once: %s", launchCmd)
	}
	if !strings.Contains(launchCmd, "CALLER-PRIME-SENTINEL") {
		t.Fatalf("the caller's own prime was dropped: %s", launchCmd)
	}
	saved, err := h.App.DB.Session(row.id())
	if err != nil {
		t.Fatal(err)
	}
	var cfg sessions.LaunchConfiguration
	if err := json.Unmarshal([]byte(saved.LaunchConfigJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.ProfileInstructions != instructions || cfg.ProfileName != "Briefed" {
		t.Fatalf("briefing was not captured with the launch: %+v", cfg)
	}
	if cfg.Spec.Command != "codex" || cfg.Spec.ModelFlag != "-m" {
		t.Fatalf("briefing changed the launch spec: %+v", cfg.Spec)
	}
	// A later edit of the reusable profile must not rewrite the continuation.
	h.decode("PUT", fmt.Sprintf("/api/launch-profiles/%d", profile.id()),
		obj{"name": "Briefed", "agent": "codex", "instructions": "edited briefing"}, 200, nil)
	continued, err := h.App.Sessions.SessionLaunchConfiguration(saved)
	if err != nil || continued.ProfileInstructions != instructions {
		t.Fatalf("continuation consulted a later profile edit: %+v %v", continued, err)
	}
}
