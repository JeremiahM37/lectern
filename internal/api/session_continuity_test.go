package api_test

import (
	"fmt"
	"testing"
)

func truthy(o obj, key string) bool {
	value, _ := o[key].(bool)
	return value
}

// A switch is two operations, and the API has to say which one is running:
// "saving" while the predecessor writes its wrap, then "starting" with the
// destination named. Today the sheet could only say "Requesting switch…", and a
// reload lost even that.
func TestQuickSwitchExposesPhaseDestinationAndLineage(t *testing.T) {
	h := newHarness(t)
	old := h.session(obj{"project_id": h.seededProjectID(), "name": "continuity", "agent": "claude", "model": "fable"})
	h.waitSessionStatus(old.id(), "waiting", "idle", "running")
	path := fmt.Sprintf("/api/sessions/%d/handoff", old.id())
	h.post(path, obj{"successor": true, "quick_switch": true, "agent": "codex", "model": "astra-test", "kill_old": false}, 202)

	inflight := h.get(fmt.Sprintf("/api/sessions/%d", old.id()))
	if !truthy(inflight, "handoff_in_flight") {
		t.Fatalf("switch not in flight: %+v", inflight)
	}
	if inflight.str("handoff_phase") != "saving" {
		t.Fatalf("phase: %+v", inflight)
	}
	if inflight.str("handoff_destination") != "Codex · astra-test" {
		t.Fatalf("destination: %+v", inflight)
	}
	// A reload refetches the list, so the same progress must be there too.
	var list []obj
	h.decode("GET", "/api/sessions", nil, 200, &list)
	found := false
	for _, row := range list {
		if row.id() == old.id() {
			found = true
			if row.str("handoff_phase") != "saving" || row.str("handoff_destination") == "" {
				t.Fatalf("list lost switch progress: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("original session missing from the list mid-switch")
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
	source := h.get(fmt.Sprintf("/api/sessions/%d", old.id()))
	if source.str("handoff_phase") != "" || truthy(source, "handoff_in_flight") {
		t.Fatalf("finished switch still in flight: %+v", source)
	}
	if int64(source.num("successor_id")) != nextID {
		t.Fatalf("original does not link forward: %+v", source)
	}
	if source.str("handoff_error") != "" {
		t.Fatalf("successful switch kept an error: %+v", source)
	}
	successor := h.get(fmt.Sprintf("/api/sessions/%d", nextID))
	if int64(successor.num("predecessor_id")) != old.id() {
		t.Fatalf("successor does not link back: %+v", successor)
	}
	if h.sessionByID(old.id()).str("status") == "dead" {
		t.Fatal("switch retired the original session")
	}
}

// A failed successor must leave a reload-safe explanation and must not claim a
// link to a session that never started.
func TestQuickSwitchFailureExplainsItselfAndKeepsOriginal(t *testing.T) {
	h := newHarness(t)
	project := h.seededProjectID()
	old := h.session(obj{"project_id": project, "name": "continuity-failure", "agent": "claude"})
	h.waitSessionStatus(old.id(), "waiting", "idle", "running")
	// Codex cannot launch this project's strict MCP policy, so the wrap is
	// written and the successor launch fails.
	if err := h.App.DB.Update("projects", project, map[string]any{"strict_mcp": 1}); err != nil {
		t.Fatal(err)
	}
	h.post(fmt.Sprintf("/api/sessions/%d/handoff", old.id()), obj{"successor": true, "quick_switch": true, "agent": "codex"}, 202)
	h.waitUntil("failed switch to finish", func() bool {
		return !truthy(h.get(fmt.Sprintf("/api/sessions/%d", old.id())), "handoff_in_flight")
	})
	view := h.get(fmt.Sprintf("/api/sessions/%d", old.id()))
	if view.str("handoff_error") == "" {
		t.Fatalf("failure not explained: %+v", view)
	}
	if view["successor_id"] != nil {
		t.Fatalf("failed switch claimed a successor: %+v", view)
	}
	if h.sessionByID(old.id()).str("status") == "dead" {
		t.Fatal("failed switch killed the original session")
	}
}
