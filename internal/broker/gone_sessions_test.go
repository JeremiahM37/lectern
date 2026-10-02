package broker

import (
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// An approval cannot outlive the session it belongs to: once the session has
// ended, or its agent has exited, nothing can receive a decision.
func TestApprovalsExpireWhenTheirSessionIsGone(t *testing.T) {
	br, db, _, _ := newBroker(t)
	target, _ := db.InsertTarget(&store.Target{Name: "s", Kind: "local"})
	session := func(name string) int64 {
		s, err := db.InsertSession(&store.Session{TargetID: target.ID, Name: name, TmuxSession: "lec-" + name, Status: "waiting"})
		if err != nil {
			t.Fatal(err)
		}
		return s.ID
	}
	live, ended, exited := session("live"), session("ended"), session("exited")
	ids := map[int64]int64{}
	for _, sid := range []int64{live, ended, exited} {
		id, err := db.InsertSessionApproval(sid, "Bash", `{"command":"ls"}`)
		if err != nil {
			t.Fatal(err)
		}
		ids[sid] = id
	}
	now := store.Now()
	_ = db.Update("sessions", ended, map[string]any{"ended_at": now, "status": "dead"})
	_ = db.Update("sessions", exited, map[string]any{"agent_exited_at": now})

	if n := br.ExpireForGoneSessions(); n != 2 {
		t.Fatalf("expired %d approvals, want 2", n)
	}
	for sid, want := range map[int64]string{live: "pending", ended: "expired", exited: "expired"} {
		row, _ := db.Approval(ids[sid])
		if row.Status != want {
			t.Errorf("session %d approval: %s, want %s", sid, row.Status, want)
		}
	}
	if n := br.ExpireForGoneSessions(); n != 0 {
		t.Fatalf("a second sweep expired %d more", n)
	}
}
