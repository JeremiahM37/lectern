package api_test

// Memory you can see (docs/memory-visibility.md): the delivery log half. These
// drive the real delivery paths — an interactive launch and a dispatched task —
// against a fake Grimoire, then read the log back through the API to prove that
// what the agent was handed is visible and attributed to the right run.

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

// itemMemoryServer is a fake Grimoire that answers /api/memory/context with
// items as well as keys, and hands out a second, different delivery when the
// query names it. The mutex is because a launch primes a pane from a goroutine.
func itemMemoryServer(t *testing.T) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	queries := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/memory/context" {
			http.NotFound(writer, request)
			return
		}
		query := request.URL.Query()
		mu.Lock()
		queries = append(queries, query.Get("q"))
		mu.Unlock()
		// A query that names something else retrieves something else, even when
		// the session has already been given its project note; anything else
		// with an exclusion list is the same context again, and nothing goes
		// out twice.
		if !strings.Contains(query.Get("q"), "SECOND") && query.Get("exclude") != "" {
			json.NewEncoder(writer).Encode(map[string]any{"context": "", "keys": []string{}})
			return
		}
		body := map[string]any{
			"context": "KESTREL_MEMORY_CONTEXT",
			"keys":    []string{"memory/kestrel.md#1"},
			"mode":    "scoped",
			"items": []map[string]any{{
				"id": "kestrel-1", "path": "memory/kestrel.md", "title": "Kestrel",
				"snippet": strings.Repeat("deploys from cold storage ", 20),
				"trusted": true,
			}},
		}
		if strings.Contains(query.Get("q"), "SECOND") {
			body["context"] = "KESTREL_SECOND_CONTEXT"
			body["keys"] = []string{"memory/notes.md#2"}
			body["items"] = []map[string]any{{
				"id": "kestrel-2", "path": "memory/notes.md", "title": "Notes",
				"snippet": "the second thing",
			}}
		}
		json.NewEncoder(writer).Encode(body)
	}))
	t.Cleanup(server.Close)
	return server
}

func memoryHarness(t *testing.T) (*harness, *httptest.Server) {
	t.Helper()
	server := itemMemoryServer(t)
	h := newHarness(t, func(c *config.Config) {
		c.GrimoireURL = server.URL
		c.GrimoireContextProjects = `{"kestrel":{"paths":["teams/kestrel/"]}}`
	})
	return h, server
}

func TestSessionMemoryListsWhatWasDelivered(t *testing.T) {
	h, _ := memoryHarness(t)
	project := h.post("/api/projects", obj{"name": "kestrel",
		"target_id": h.firstTargetID(), "repo_path": "/mock/kestrel"}, 201)
	session := h.session(obj{"project_id": project.id(), "agent": "claude"})

	// A launch types the recalled block in once the pane settles; the log must
	// have the row by then, without anyone asking.
	var deliveries []obj
	h.waitUntil("the launch delivery to be recorded", func() bool {
		deliveries = h.get(fmt.Sprintf("/api/sessions/%d/memory", session.id())).list("deliveries")
		return len(deliveries) == 1
	})
	first := deliveries[0]
	if int64(first.num("session_id")) != session.id() || first.num("task_id") != 0 {
		t.Fatalf("delivery attributed to the wrong run: %v", first)
	}
	if first.str("mode") != "scoped" || first.num("bytes") <= 0 {
		t.Errorf("mode/bytes not recorded: %v", first)
	}
	items := first.list("items")
	if len(items) != 1 || items[0].str("id") != "kestrel-1" ||
		items[0].str("source") != "memory/kestrel.md" || items[0].str("title") != "Kestrel" {
		t.Fatalf("items were not kept for the operator: %v", items)
	}
	if snippet := items[0].str("snippet"); !strings.HasSuffix(snippet, "…") || len(snippet) > 400 {
		t.Errorf("snippet should be a bounded excerpt, got %d bytes", len(snippet))
	}

	// A message that retrieves something new is its own delivery, and the log
	// reads newest first.
	h.post(fmt.Sprintf("/api/sessions/%d/send", session.id()),
		obj{"text": "tell me about the SECOND thing"}, 200)
	h.waitUntil("the send delivery to be recorded", func() bool {
		deliveries = h.get(fmt.Sprintf("/api/sessions/%d/memory", session.id())).list("deliveries")
		return len(deliveries) == 2
	})
	if deliveries[0].num("id") <= deliveries[1].num("id") {
		t.Errorf("deliveries must read newest first: %v", deliveries)
	}
	if got := deliveries[0].list("items")[0].str("id"); got != "kestrel-2" {
		t.Errorf("the newest delivery is not the newest context: %q", got)
	}
	// The session key half of the same endpoint is untouched by any of this.
	if key := h.get(fmt.Sprintf("/api/sessions/%d/memory", session.id())).str("session"); !strings.HasPrefix(key, "lectern-s") {
		t.Errorf("session key lost: %q", key)
	}
}

func TestTaskMemoryListsWhatWasDeliveredToItsAttempt(t *testing.T) {
	h, _ := memoryHarness(t)
	project := h.post("/api/projects", obj{"name": "kestrel",
		"target_id": h.firstTargetID(), "repo_path": "/mock/kestrel"}, 201)
	task := h.run(project.id(), "deployment", "check the deployment certificates", nil)

	body := h.get(fmt.Sprintf("/api/tasks/%d/memory", task.id()))
	deliveries := body.list("deliveries")
	if int64(body.num("task_id")) != task.id() || len(deliveries) != 1 {
		t.Fatalf("task deliveries: %v", deliveries)
	}
	row := deliveries[0]
	if int64(row.num("task_id")) != task.id() || row.num("session_id") != 0 {
		t.Fatalf("delivery attributed to the wrong run: %v", row)
	}
	if row.num("attempt_id") == 0 {
		t.Errorf("a task delivery must name its attempt: %v", row)
	}
	if items := row.list("items"); len(items) != 1 || items[0].str("id") != "kestrel-1" {
		t.Errorf("items were not kept: %v", items)
	}
	// A task that was handed nothing says so with an empty list, not an error.
	other := h.task(project.id(), "quiet", "no memory here", nil)
	if got := h.get(fmt.Sprintf("/api/tasks/%d/memory", other.id())).list("deliveries"); len(got) != 0 {
		t.Errorf("a task that ran nothing recorded something: %v", got)
	}
}
