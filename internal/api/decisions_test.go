package api_test

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// The decision-log read routes return what the store holds, filter by kind,
// and refuse an unknown kind instead of answering with an empty list.
// Uses the full API harness, which needs the reviewed isolated runner; this
// test is written and vetted but has not been run in the authoring environment.
func TestDecisionRoutesReadTheLog(t *testing.T) {
	h := newHarness(t)
	db := h.App.DB
	lat := 12.0
	sid := int64(3)
	if _, err := db.InsertDecision(&store.Decision{Kind: "turn_response", Decision: "reply",
		Via: "terminal", SessionID: &sid, Agent: "claude", LatencyS: &lat, ActiveSessions: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertDecision(&store.Decision{Kind: "interrupt", Decision: "interrupt", Via: "terminal"}); err != nil {
		t.Fatal(err)
	}

	all := h.get("/api/decisions?days=30")
	if rows, _ := all["rows"].([]any); len(rows) < 2 {
		t.Fatalf("all rows: %v", all)
	}
	replies := h.get("/api/decisions?days=30&kind=turn_response")
	if rows, _ := replies["rows"].([]any); len(rows) != 1 {
		t.Fatalf("kind filter: %v", replies)
	}
	if code := h.status("GET", "/api/decisions?kind=bogus", nil); code != 422 {
		t.Fatalf("unknown kind: status %d, want 422", code)
	}
	if code := h.status("GET", "/api/decisions?days=0", nil); code != 422 {
		t.Fatalf("days=0: status %d, want 422", code)
	}

	summary := h.get("/api/decisions/summary?days=30")
	kinds, _ := summary["kinds"].([]any)
	if len(kinds) != 2 {
		t.Fatalf("summary kinds: %v", summary)
	}
}
