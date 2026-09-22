package api_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func TestProjectMemoryProvisioningIsPersistentScopedAndRetryable(t *testing.T) {
	var lock sync.Mutex
	notes := map[string]obj{}
	fail := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		lock.Lock()
		defer lock.Unlock()
		if fail {
			http.Error(writer, "offline", 503)
			return
		}
		switch {
		case request.Method == "POST" && request.URL.Path == "/api/notes":
			var note obj
			if err := json.NewDecoder(request.Body).Decode(&note); err != nil {
				t.Error(err)
			}
			path := note.str("path")
			if _, exists := notes[path]; exists {
				writer.WriteHeader(http.StatusConflict)
				return
			}
			notes[path] = note
			writer.WriteHeader(http.StatusCreated)
		case strings.HasPrefix(request.URL.Path, "/api/notes/"):
			json.NewEncoder(writer).Encode(notes[strings.TrimPrefix(request.URL.Path, "/api/notes/")])
		case request.URL.Path == "/api/memory/context":
			paths := request.URL.Query()["path"]
			if len(paths) != 1 || notes[paths[0]] == nil {
				t.Errorf("not scoped to the provisioned location: %v", paths)
			}
			json.NewEncoder(writer).Encode(obj{"context": "SCOPED_PROJECT_REFERENCE"})
		default:
			t.Errorf("unexpected endpoint: %s", request.URL)
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	harness := newHarness(t, func(configuration *config.Config) { configuration.GrimoireURL = server.URL })
	create := func(name string) obj {
		return harness.post("/api/projects", obj{"name": name, "target_id": harness.firstTargetID(), "repo_path": "/mock/" + name}, 201)
	}
	project := create("Kestrel.One")
	other := create("Kestrel-One")
	if project.str("memory_topic") == other.str("memory_topic") || project.str("memory_status") != "ready" {
		t.Fatalf("missing unique association: %v %v", project, other)
	}
	topic := project.str("memory_topic")
	renamed := harness.patch(fmt.Sprintf("/api/projects/%d", project.id()), obj{"name": "Renamed"}, 200)
	if renamed.str("memory_topic") != topic {
		t.Fatal("rename changed memory location")
	}
	brief := harness.get(fmt.Sprintf("/api/projects/%d/brief", project.id()))
	if !strings.Contains(brief.str("brief"), "SCOPED_PROJECT_REFERENCE") || !strings.Contains(brief.str("brief"), topic) {
		t.Fatal("preview lost scoped memory or write destination")
	}
	lock.Lock()
	fail = true
	lock.Unlock()
	unavailable := create("Offline")
	if unavailable.str("memory_status") != "unavailable" {
		t.Fatal("provisioning failure was hidden")
	}
	lock.Lock()
	fail = false
	lock.Unlock()
	for count := 0; count < 2; count++ {
		retried := harness.post(fmt.Sprintf("/api/projects/%d/memory", unavailable.id()), obj{}, 200)
		if retried.str("memory_status") != "ready" || retried.str("memory_topic") != unavailable.str("memory_topic") {
			t.Fatal("retry was not idempotent")
		}
	}
}

func TestManualProjectCreationDoesNotProvisionMemory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		t.Error("manual mode contacted the memory server")
	}))
	defer server.Close()
	harness := newHarness(t, func(configuration *config.Config) {
		configuration.GrimoireURL = server.URL
		configuration.GrimoireContextMode = "manual"
	})
	project := harness.post("/api/projects", obj{"name": "manual", "target_id": harness.firstTargetID(), "repo_path": "/mock/manual"}, 201)
	if project.str("memory_status") != "disabled" {
		t.Fatal("manual memory status is not disabled")
	}
}
