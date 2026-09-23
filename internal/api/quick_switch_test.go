package api_test

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
)

func TestQuickSwitchUsesDestinationDefaultAndPreservesPermissions(t *testing.T) {
	h := newHarness(t)
	old := h.session(obj{"project_id": h.seededProjectID(), "name": "quick-switch", "agent": "claude", "model": "fable", "yolo": true})
	path := fmt.Sprintf("/api/sessions/%d/handoff", old.id())
	h.waitSessionStatus(old.id(), "waiting", "idle", "running")
	response := h.post(path, obj{"successor": true, "quick_switch": true, "agent": "codex", "kill_old": false}, 202)
	if response.num("after_wrap_id") != 0 {
		t.Fatal(response)
	}
	var nextID int64
	h.waitUntil("quick switch successor", func() bool {
		wraps, _ := h.App.DB.SessionWraps(old.id())
		if len(wraps) > 0 && wraps[0].NextSessionID != nil {
			nextID = *wraps[0].NextSessionID
			return true
		}
		return false
	})
	next, err := h.App.DB.Session(nextID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Agent != "codex" || next.Model != "" || next.Workdir != old.str("workdir") {
		t.Fatalf("destination: %+v", next)
	}
	var cfg sessions.LaunchConfiguration
	if err := json.Unmarshal([]byte(next.LaunchConfigJSON), &cfg); err != nil || !cfg.Yolo {
		t.Fatalf("permissions lost: %v %+v", err, cfg)
	}
	if h.sessionByID(old.id()).str("status") == "dead" {
		t.Fatal("quick switch killed the original")
	}
}

func TestQuickSwitchUsesSavedProviderAndRejectsMissingProfile(t *testing.T) {
	h := newHarness(t)
	old := h.session(obj{"project_id": h.seededProjectID(), "name": "profile-switch", "agent": "claude", "model": "fable"})
	h.waitSessionStatus(old.id(), "waiting", "idle", "running")
	path := fmt.Sprintf("/api/sessions/%d/handoff", old.id())
	h.post(path, obj{"successor": true, "quick_switch": true, "agent": "codex", "profile_id": 99999}, 409)
	if h.sessionByID(old.id())["handoff_in_flight"] == true {
		t.Fatal("invalid profile left handoff busy")
	}
	profile := h.post("/api/launch-profiles", obj{"name": "DeepSeek", "agent": "codex", "model": "deepseek-chat", "env_json": `{"OPENAI_BASE_URL":"https://models.example/v1"}`}, 201)
	h.post(path, obj{"successor": true, "quick_switch": true, "agent": "codex", "profile_id": profile.id()}, 202)
	h.waitUntil("provider successor", func() bool {
		wraps, _ := h.App.DB.SessionWraps(old.id())
		if len(wraps) == 0 || wraps[0].NextSessionID == nil {
			return false
		}
		next, err := h.App.DB.Session(*wraps[0].NextSessionID)
		if err != nil {
			t.Fatal(err)
		}
		var cfg sessions.LaunchConfiguration
		if err = json.Unmarshal([]byte(next.LaunchConfigJSON), &cfg); err != nil {
			t.Fatal(err)
		}
		if next.Model != "deepseek-chat" || cfg.ProfileID != profile.id() || cfg.Spec.Env["OPENAI_BASE_URL"] != "https://models.example/v1" {
			t.Fatalf("profile not applied: %+v %+v", next, cfg)
		}
		return true
	})
}

func TestFailedSuccessorKeepsOriginalEvenWhenRetirementRequested(t *testing.T) {
	h := newHarness(t)
	project := h.seededProjectID()
	old := h.session(obj{"project_id": project, "name": "failed-switch", "agent": "claude"})
	h.waitSessionStatus(old.id(), "waiting", "idle", "running")
	// Codex cannot launch this project's strict MCP policy. Fail after the wrap
	// has been saved, exercising the old kill-before-launch data-loss ordering.
	if err := h.App.DB.Update("projects", project, map[string]any{"strict_mcp": 1}); err != nil {
		t.Fatal(err)
	}
	h.post(fmt.Sprintf("/api/sessions/%d/handoff", old.id()), obj{"successor": true, "agent": "codex", "kill_old": true}, 202)
	h.waitUntil("failed successor to finish", func() bool {
		wraps, _ := h.App.DB.SessionWraps(old.id())
		return len(wraps) > 0 && !(h.sessionByID(old.id())["handoff_in_flight"] == true)
	})
	if h.sessionByID(old.id()).str("status") == "dead" {
		t.Fatal("failed launch killed original session")
	}
	wraps, _ := h.App.DB.SessionWraps(old.id())
	if wraps[0].NextSessionID != nil {
		t.Fatal("failed launch recorded as successful")
	}
}
