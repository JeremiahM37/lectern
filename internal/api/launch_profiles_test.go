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
