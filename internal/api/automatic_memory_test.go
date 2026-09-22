package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/JeremiahM37/lectern/internal/config"
)

func scopedMemoryServer(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	calls := &atomic.Int64{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == "POST" && request.URL.Path == "/api/notes" {
			writer.WriteHeader(http.StatusCreated)
			return
		}
		if request.URL.Path != "/api/memory/context" {
			t.Errorf("automatic flow reached unscoped endpoint: %s", request.URL.Path)
			http.NotFound(writer, request)
			return
		}
		calls.Add(1)
		query := request.URL.Query()
		if query.Get("scope") != "scoped" || query.Get("max_bytes") != "2400" {
			t.Errorf("bad scope: %v", query)
		}
		paths := query["path"]
		if len(paths) != 2 || paths[0] != "teams/kestrel/" || !strings.HasPrefix(paths[1], "memory/lectern-") {
			t.Errorf("wrong assigned project: %v", paths)
		}
		if query.Get("exclude") != "" {
			json.NewEncoder(writer).Encode(map[string]any{"context": "", "keys": []string{}})
			return
		}
		json.NewEncoder(writer).Encode(map[string]any{"context": "AUTOMATIC_KESTREL_REFERENCE", "keys": []string{"kestrel-key"}})
	}))
	t.Cleanup(server.Close)
	return server, calls
}

func TestAssignedProjectAutomaticallyPrimesAndDeduplicatesMessages(t *testing.T) {
	server, calls := scopedMemoryServer(t)
	harness := newHarness(t, func(configuration *config.Config) {
		configuration.GrimoireURL = server.URL
		configuration.GrimoireContextProjects = `{"kestrel":{"paths":["teams/kestrel/"]}}`
	})
	project := harness.post("/api/projects", obj{"name": "kestrel", "target_id": harness.firstTargetID(), "repo_path": "/mock/kestrel"}, 201)
	session := harness.session(obj{"project_id": project.id(), "agent": "claude"})
	if !strings.Contains(harness.launchCmd(), "AUTOMATIC_KESTREL_REFERENCE") {
		t.Fatal("launch did not inject project memory without brief:true")
	}
	if calls.Load() != 1 {
		t.Fatalf("launch calls: %d", calls.Load())
	}
	harness.post(fmt.Sprintf("/api/sessions/%d/send", session.id()), obj{"text": "What are the deployment constraints?"}, 200)
	for path, content := range harness.mock().Files() {
		if strings.Contains(path, "lectern-send-") && strings.Contains(string(content), "AUTOMATIC_KESTREL_REFERENCE") {
			t.Fatal("repeated context burned tokens")
		}
	}
	before := calls.Load()
	harness.post(fmt.Sprintf("/api/sessions/%d/send", session.id()), obj{"text": "continue"}, 200)
	if calls.Load() != before {
		t.Fatal("continue spent an unnecessary lookup")
	}
}

func TestDispatchedTaskAutomaticallyUsesAssignedProjectMemory(t *testing.T) {
	server, calls := scopedMemoryServer(t)
	harness := newHarness(t, func(configuration *config.Config) {
		configuration.GrimoireURL = server.URL
		configuration.GrimoireContextProjects = `{"kestrel":{"paths":["teams/kestrel/"]}}`
	})
	project := harness.post("/api/projects", obj{"name": "kestrel", "target_id": harness.firstTargetID(), "repo_path": "/mock/kestrel"}, 201)
	harness.run(project.id(), "deployment", "check the deployment certificates", nil)
	prompt := string(harness.staged("/.lectern/prompt.md"))
	if !strings.Contains(prompt, "AUTOMATIC_KESTREL_REFERENCE") || calls.Load() != 1 {
		t.Fatalf("missing automatic task context (%d calls): %s", calls.Load(), prompt)
	}
}

func TestManualModeLaunchDoesNotConsultMemory(t *testing.T) {
	server, calls := scopedMemoryServer(t)
	harness := newHarness(t, func(configuration *config.Config) {
		configuration.GrimoireURL = server.URL
		configuration.GrimoireContextMode = "manual"
	})
	harness.session(obj{"project_id": harness.seededProjectID(), "agent": "claude", "brief": true})
	if calls.Load() != 0 || strings.Contains(harness.launchCmd(), "AUTOMATIC_KESTREL_REFERENCE") {
		t.Fatal("manual mode consulted memory")
	}
}
