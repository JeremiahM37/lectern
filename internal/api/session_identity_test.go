package api_test

// A session and the memory store used to have no key in common: AgentDeck knew
// a session by its row, the store knew the agent's writes by no run at all, and
// AgentDeck's own writes went in under a display name. These tests follow the
// one key through every place it has to agree.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/JeremiahM37/agentdeck/internal/config"
)

// fakeGrimoire records what AgentDeck writes and answers what it reads.
type fakeGrimoire struct {
	mu      sync.Mutex
	writes  []map[string]any
	queries []string
	changes string
	status  int
	notes   string
}

func (g *fakeGrimoire) server(t *testing.T) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		defer g.mu.Unlock()
		switch {
		case r.URL.Path == "/api/health":
			w.Write([]byte(`{"ok":true}`))
		case r.URL.Path == "/api/memory" && r.Method == "POST":
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			g.writes = append(g.writes, body)
			w.Write([]byte(`{"ok":true}`))
		case r.URL.Path == "/api/memory/changes":
			g.queries = append(g.queries, r.URL.RawQuery)
			if g.status != 0 {
				http.Error(w, "down", g.status)
				return
			}
			w.Write([]byte(g.changes))
		case r.URL.Path == "/api/notes" && r.Method == "GET":
			if g.notes == "" {
				w.Write([]byte(`[]`))
				return
			}
			w.Write([]byte(g.notes))
		default:
			w.Write([]byte(`{"results":[],"memories":[]}`))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestASessionAndTheMemoryStoreShareOneKey(t *testing.T) {
	grimoire := &fakeGrimoire{changes: `{"since":"2026-01-01T00:00:00Z","counts":{"learned":1,"changed":1},
 "changes":[{"kind":"learned","at":"2026-01-01T00:01:00Z","text":"Staging is on 5433","path":"memory/demo.md","agent":"claude-code","topic":"demo"},
 {"kind":"changed","at":"2026-01-01T00:02:00Z","text":"Deploys use the script","replaced_text":"Deploys are manual","agent":"claude-code"}]}`}
	srv := grimoire.server(t)
	h := newHarness(t, func(c *config.Config) { c.GrimoireURL = srv.URL })
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "Same name"})
	twin := h.session(obj{"project_id": pid, "name": "Same name"})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	key := fmt.Sprintf("agentdeck-s%d", sess.id())

	// 1. The agent is launched knowing which session it is, under the name each
	// reader looks for. The memory server stamps the second on every write.
	launched := ""
	for _, cmd := range h.mock().CmdLog() {
		if strings.HasPrefix(cmd, "tmux new-session") && strings.Contains(cmd, sess.str("tmux_session")) {
			launched = cmd
		}
	}
	for _, want := range []string{fmt.Sprintf("AGENTDECK_SESSION_ID=%d ", sess.id()), "GRIMOIRE_SESSION=" + key + " "} {
		if !strings.Contains(launched, want) {
			t.Errorf("launch is missing %s:\n%s", want, launched)
		}
	}

	// 2. AgentDeck's own write at a handoff goes in under that same key. It used
	// the display name, which these two sessions share.
	h.post(fmt.Sprintf("/api/sessions/%d/handoff", sess.id()), obj{}, 202)
	h.waitUntil("the handoff to reach the memory store", func() bool {
		grimoire.mu.Lock()
		defer grimoire.mu.Unlock()
		return len(grimoire.writes) > 0
	})
	grimoire.mu.Lock()
	written := grimoire.writes[0]
	grimoire.mu.Unlock()
	if written["session"] != key {
		t.Errorf("handoff session = %v, want %s", written["session"], key)
	}
	if written["session"] == fmt.Sprintf("agentdeck-s%d", twin.id()) || written["session"] == "Same name" {
		t.Errorf("the key must tell two sessions with one name apart: %v", written["session"])
	}

	// 3. And AgentDeck can read back what the agent wrote on its own, by it.
	got := h.get(fmt.Sprintf("/api/sessions/%d/memory", sess.id()))
	if got.str("status") != "ready" || got.str("session") != key || len(got.list("changes")) != 2 {
		t.Fatalf("read-back: %v", got)
	}
	if got.list("changes")[1].str("replaced_text") != "Deploys are manual" {
		t.Errorf("a correction must show what it replaced: %v", got.list("changes")[1])
	}
	grimoire.mu.Lock()
	asked := grimoire.queries[len(grimoire.queries)-1]
	grimoire.mu.Unlock()
	if !strings.Contains(asked, "session="+key) || !strings.Contains(asked, "since=") {
		t.Errorf("asked the store %q", asked)
	}
}

func TestSessionMemorySaysWhichKindOfNothingItIs(t *testing.T) {
	// No provider: disabled, not an error and not an empty list pretending.
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID(), "name": "quiet"})
	if got := h.get(fmt.Sprintf("/api/sessions/%d/memory", sess.id())); got.str("status") != "disabled" {
		t.Errorf("no provider: %v", got)
	}
	if code := h.status("GET", "/api/sessions/99999/memory", nil); code != 404 {
		t.Errorf("unknown session: %d", code)
	}

	grimoire := &fakeGrimoire{changes: `{"since":"x","counts":{},"changes":[]}`}
	srv := grimoire.server(t)
	h2 := newHarness(t, func(c *config.Config) { c.GrimoireURL = srv.URL })
	s2 := h2.session(obj{"project_id": h2.seededProjectID(), "name": "quiet"})
	if got := h2.get(fmt.Sprintf("/api/sessions/%d/memory", s2.id())); got.str("status") != "empty" {
		t.Errorf("nothing written: %v", got)
	}
	grimoire.mu.Lock()
	grimoire.status = 503
	grimoire.mu.Unlock()
	got := h2.get(fmt.Sprintf("/api/sessions/%d/memory", s2.id()))
	if got.str("status") != "unavailable" || got.str("message") == "" {
		t.Errorf("an outage must not read as 'wrote nothing': %v", got)
	}
}

func TestAnInheritedSessionIdIsAHintAndATypedOneIsAClaim(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID(), "name": "poster"})
	// Inherited from the environment and real: used.
	hit := h.post("/api/media", obj{"url": "https://example.com", "title": "a", "hint_session_id": sess.id()}, 201)
	if int64(hit.num("session_id")) != sess.id() {
		t.Errorf("a valid hint must attribute the post: %v", hit)
	}
	// Inherited from a session of some other AgentDeck: the post still lands.
	stray := h.post("/api/media", obj{"url": "https://example.com", "title": "b", "hint_session_id": 99999}, 201)
	if stray["session_id"] != nil {
		t.Errorf("an unknown hint must post unattributed: %v", stray)
	}
	// Typed by a person and wrong: that is an error they need to see.
	if code := h.status("POST", "/api/media", obj{"url": "https://example.com", "title": "c", "session_id": 99999}); code != 422 {
		t.Errorf("an unknown explicit session: %d", code)
	}
}

func TestAProjectsMemoryLinkIsReportedAsItIsNotAsItIsAssumed(t *testing.T) {
	grimoire := &fakeGrimoire{notes: `[{"path":"Agent Memory/project_demo_app.md"},{"path":"memory/other.md"}]`}
	srv := grimoire.server(t)
	h := newHarness(t, func(c *config.Config) { c.GrimoireURL = srv.URL; c.GrimoireContextMode = "project" })
	pid := h.seededProjectID()
	link := h.get(fmt.Sprintf("/api/projects/%d/memory", pid))
	detail := link.sub("link")
	// The seeded project has a managed topic, so its link is exact — and the
	// note it names is not in the store, which the report has to say.
	if detail.str("basis") != "managed" || link.str("status") != "unlinked" {
		t.Fatalf("managed but unprovisioned: %v", link)
	}
	if paths := detail.list("paths"); len(paths) != 1 || paths[0]["exists"] != false ||
		!strings.HasPrefix(paths[0].str("path"), "memory/agentdeck-") {
		t.Fatalf("paths: %v", detail)
	}
	// Without a topic the paths are guessed from the name. One of the guesses
	// is right here, and the report says which.
	if err := h.App.DB.Update("projects", pid, map[string]any{"memory_topic": ""}); err != nil {
		t.Fatal(err)
	}
	link = h.get(fmt.Sprintf("/api/projects/%d/memory", pid))
	detail = link.sub("link")
	if detail.str("basis") != "guessed" || link.str("status") != "linked" {
		t.Fatalf("guessed and found: %v", link)
	}
	found := map[string]bool{}
	for _, p := range detail.list("paths") {
		found[p.str("path")] = p["exists"] == true
	}
	if !found["Agent Memory/project_demo_app.md"] || found["memory/demo-app.md"] {
		t.Errorf("which guess was right: %v", found)
	}
	// And when none is, it is "unlinked", not a project that merely has no memory.
	grimoire.mu.Lock()
	grimoire.notes = `[{"path":"memory/other.md"}]`
	grimoire.mu.Unlock()
	if got := h.get(fmt.Sprintf("/api/projects/%d/memory", pid)); got.str("status") != "unlinked" {
		t.Errorf("no guess matched: %v", got)
	}
}
