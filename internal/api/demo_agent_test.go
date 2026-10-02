package api_test

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// "Try a demo agent" must work with no agent CLI installed: a real tmux
// session running the scripted stand-in, in a new empty folder, that answers a
// message and leaves a change behind to review.
func TestRealDemoAgentAnswersAndLeavesAChange(t *testing.T) {
	requireRealTools(t)
	h := newHarness(t, realLocal)
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "demo-real", Kind: "local"})
	if err != nil {
		t.Fatal(err)
	}
	var row obj
	// Bypass: this test is about the answer and the change; asking is
	// cmd/lectern TestDemoAgentAsksForApproval.
	h.decode("POST", "/api/sessions", obj{"agent": "demo", "scratch": true, "target_id": target.ID, "permission_mode": "bypass"}, 201, &row)
	id := int64(row.num("id"))
	deadline := time.Now().Add(30 * time.Second)
	for {
		s, err := h.App.DB.Session(id)
		if err == nil && strings.Contains(s.PaneTail, "Demo agent") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no greeting: %+v", s)
		}
		time.Sleep(100 * time.Millisecond)
	}
	s, _ := h.App.DB.Session(id)
	h.post(fmt.Sprintf("/api/sessions/%d/send", id), obj{"text": "hello there"}, 200)
	notes := filepath.Join(s.Workdir, "demo-notes.md")
	h.waitUntil("the demo agent writes the message down", func() bool {
		got, _ := os.ReadFile(notes)
		return strings.Contains(string(got), "- hello there")
	})

	// It launches, but is never listed as an agent anyone configured.
	for _, a := range h.getList("/api/agents") {
		if a.str("name") == "demo" {
			t.Fatal("the demo agent must not appear in the agent registry")
		}
	}
}

func TestDemoAgentLaunchesInMockMode(t *testing.T) {
	h := newHarness(t)
	var row obj
	h.decode("POST", "/api/sessions", obj{"agent": "demo", "scratch": true}, 201, &row)
	if row.str("agent") != "demo" || row.str("state") == "" || row.str("state_label") == "" {
		t.Fatalf("demo session: %v", row)
	}
	if code := h.status("POST", "/api/sessions", obj{"agent": "no-such-agent", "scratch": true}); code != 422 {
		t.Fatalf("unknown agents are still refused: %d", code)
	}
}
