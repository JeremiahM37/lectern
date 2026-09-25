package api_test

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/triggers"
)

func triggersPath(projectID int64) string {
	return "/api/projects/" + strconv.FormatInt(projectID, 10) + "/triggers"
}

func TestCreateTriggerSourceValidatesConfig(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()

	// a repo-less GitHub source is refused up front, not discovered at the
	// first poll
	code := h.status("POST", triggersPath(pid), obj{"kind": "github", "config": obj{}})
	if code != 400 {
		t.Fatalf("expected 400 for a github source with no repo, got %d", code)
	}

	src := h.post(triggersPath(pid), obj{
		"kind": "github", "name": "gh", "config": obj{"repo": "a/b", "allowed_authors": []string{"octocat"}},
	}, 201)
	if src.str("kind") != "github" || src.sub("config").str("repo") != "a/b" {
		t.Fatalf("unexpected source: %v", src)
	}
	if v, _ := src["enabled"].(bool); !v {
		t.Fatalf("expected a newly created source to default to enabled, got %v", src)
	}
}

func TestTriggerSourceSecretsAreNeverReturnedRaw(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	src := h.post(triggersPath(pid), obj{
		"kind": "linear", "name": "lin",
		"config":  obj{"team_key": "ENG", "allowed_users": []string{"a@b.com"}},
		"secrets": obj{"api_key": "lin_super_secret"},
	}, 201)
	raw, _ := json.Marshal(src)
	if strings.Contains(string(raw), "lin_super_secret") {
		t.Fatalf("the raw secret leaked into the response: %s", raw)
	}
	secrets := src.sub("secrets")
	if v, _ := secrets["api_key"].(bool); !v {
		t.Fatalf("expected secrets.api_key to report true (configured), got %v", src)
	}
}

func TestTriggerSourceListPatchDelete(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	src := h.post(triggersPath(pid), obj{
		"kind": "github", "config": obj{"repo": "a/b"},
	}, 201)
	id := src.id()

	list := h.getList(triggersPath(pid))
	if len(list) != 1 || list[0].id() != id {
		t.Fatalf("expected the new source in the project list, got %v", list)
	}

	idPath := "/api/triggers/" + strconv.FormatInt(id, 10)
	patched := h.patch(idPath, obj{"enabled": false}, 200)
	if v, _ := patched["enabled"].(bool); v {
		t.Fatalf("expected enabled=false after patch, got %v", patched)
	}

	if code := h.status("DELETE", idPath, nil); code != 204 {
		t.Fatalf("expected 204 deleting a source, got %d", code)
	}
	if list := h.getList(triggersPath(pid)); len(list) != 0 {
		t.Fatalf("expected no sources after delete, got %v", list)
	}
}

func TestTriggerSourceRejectsUnknownAgent(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	code := h.status("POST", triggersPath(pid), obj{
		"kind": "github", "config": obj{"repo": "a/b", "agent": "no-such-agent"},
	})
	if code != 400 {
		t.Fatalf("expected 400 for an unknown agent in a trigger config, got %d", code)
	}
}

func TestListTriggerEventsEmpty(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	events := h.getList("/api/projects/" + strconv.FormatInt(pid, 10) + "/trigger-events")
	if len(events) != 0 {
		t.Fatalf("expected no events yet, got %v", events)
	}
}

// TestCreateTriggerTaskUsesProjectPermissionMode is the safety requirement
// from requirement 4 made concrete: whatever a trigger's own config might
// ask for, the task it files always carries the project's own default
// permission mode — CreateTriggerTask does not even accept an override.
func TestCreateTriggerTaskUsesProjectPermissionMode(t *testing.T) {
	h := newHarness(t)
	target := h.firstTargetID()
	proj := h.post("/api/projects", obj{
		"name": "gated", "target_id": target, "repo_path": "/mock/gated",
		"default_permission_mode": "plan",
	}, 201)
	project, err := h.App.DB.Project(proj.id())
	if err != nil {
		t.Fatal(err)
	}
	task, err := h.App.Server.CreateTriggerTask(project, triggers.NewTaskSpec{
		Title: "from a trigger", Prompt: "do the thing", CreatedBy: "trigger:github:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if task.PermissionMode != "plan" {
		t.Fatalf("expected the project's own permission mode (plan), got %q", task.PermissionMode)
	}
	if task.CreatedBy != "trigger:github:1" {
		t.Fatalf("expected CreatedBy to be preserved, got %q", task.CreatedBy)
	}
	h.waitStatus(task.ID, "review")
}
