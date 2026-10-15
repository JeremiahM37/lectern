package store

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func openDecisionDB(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestDecisionRoundTripKeepsNullsNull(t *testing.T) {
	db := openDecisionDB(t)
	waited, latency := 100.0, 7.5
	sid := int64(42)
	if _, err := db.InsertDecision(&Decision{At: 200, Kind: "turn_response", Decision: "reply",
		Via: "terminal", SessionID: &sid, Agent: "claude", WaitedSince: &waited, LatencyS: &latency,
		ActiveSessions: 2, Pending: 1, Detail: json.RawMessage(`{"prompt_chars":12}`)}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.InsertDecision(&Decision{At: 300, Kind: "interrupt", Decision: "interrupt", Via: "terminal"}); err != nil {
		t.Fatal(err)
	}

	rows, err := db.Decisions(0, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].Kind != "interrupt" || rows[1].Kind != "turn_response" {
		t.Fatalf("rows newest first: %+v", rows)
	}
	interrupt := rows[0]
	if interrupt.SessionID != nil || interrupt.AttemptID != nil || interrupt.ProjectID != nil ||
		interrupt.WaitedSince != nil || interrupt.LatencyS != nil {
		t.Fatalf("an interrupt with no session or wait must read back NULL: %+v", interrupt)
	}
	if string(interrupt.Detail) != "{}" {
		t.Fatalf("empty detail should default to {}, got %s", interrupt.Detail)
	}
	reply := rows[1]
	if reply.SessionID == nil || *reply.SessionID != 42 || reply.LatencyS == nil || *reply.LatencyS != 7.5 ||
		reply.WaitedSince == nil || *reply.WaitedSince != 100 || reply.Agent != "claude" ||
		reply.ActiveSessions != 2 || reply.Pending != 1 {
		t.Fatalf("reply did not round-trip: %+v", reply)
	}
	if string(reply.Detail) != `{"prompt_chars":12}` {
		t.Fatalf("detail: %s", reply.Detail)
	}
}

func TestDecisionsFilterByKindSinceAndLimit(t *testing.T) {
	db := openDecisionDB(t)
	for i, kind := range []string{"approval", "review_commit", "approval", "approval"} {
		if _, err := db.InsertDecision(&Decision{At: float64(100 + i), Kind: kind, Decision: "done"}); err != nil {
			t.Fatal(err)
		}
	}
	approvals, _ := db.Decisions(0, "approval", 0)
	if len(approvals) != 3 {
		t.Fatalf("kind filter: got %d approvals", len(approvals))
	}
	recent, _ := db.Decisions(102, "", 0)
	if len(recent) != 2 {
		t.Fatalf("since filter: got %d rows at or after 102", len(recent))
	}
	capped, _ := db.Decisions(0, "", 2)
	if len(capped) != 2 {
		t.Fatalf("limit: got %d rows", len(capped))
	}
	none, err := db.Decisions(0, "", 0)
	if err != nil || len(none) != 4 {
		t.Fatalf("unfiltered: %d rows, err %v", len(none), err)
	}
}

func TestInsertDecisionDefaultsTimeToNow(t *testing.T) {
	db := openDecisionDB(t)
	d := &Decision{Kind: "approval", Decision: "approved"}
	if _, err := db.InsertDecision(d); err != nil {
		t.Fatal(err)
	}
	if d.At == 0 {
		t.Fatal("At should default to now")
	}
}
