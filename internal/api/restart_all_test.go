package api_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// "Restart all sessions" restarts idle ones in place with the same
// conversation and record, waits for busy ones, skips shells and sessions
// with nothing to resume, and brings claude back with Remote Control unless
// the session opted out.
func TestRestartAllSessions(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	idle := h.session(obj{"project_id": pid, "name": "idle one", "agent": "claude", "group_path": "Work"})
	optOut := h.session(obj{"project_id": pid, "name": "no phone", "agent": "claude", "remote_control": false})
	busy := h.session(obj{"project_id": pid, "name": "busy one", "agent": "claude"})
	fresh := h.session(obj{"project_id": pid, "name": "no conversation yet", "agent": "claude"})
	shell := h.post("/api/shells", obj{"target_id": h.firstTargetID()}, 201)
	for _, row := range []obj{idle, optOut, busy, fresh} {
		h.waitSessionStatus(row.id(), "waiting", "idle")
	}
	for i, row := range []obj{idle, optOut, busy} {
		h.setRow(row.id(), map[string]any{"native_recovery_cid": fmt.Sprintf("00000000-0000-4000-8000-00000000000%d", i+1)})
	}
	// The agent's own hook says it is mid-turn, which the screen does not
	// overrule.
	working := map[string]any{"agent_state": "working", "state_source": "hook", "hook_seen_at": store.Now(), "updated_at": store.Now()}
	h.setRow(busy.id(), working)
	h.setRow(idle.id(), map[string]any{"remote_control": false})

	var plan obj
	h.decode("GET", "/api/restart-all", nil, 200, &plan)
	want := map[int64]string{idle.id(): "restarting", optOut.id(): "restarting", busy.id(): "pending",
		fresh.id(): "skipped", shell.id(): "skipped"}
	for _, row := range plan["plan"].([]any) {
		r := obj(row.(map[string]any))
		if w, ok := want[r.id()]; ok && r.str("status") != w {
			t.Fatalf("plan for #%d: %v, want %s", r.id(), r, w)
		}
	}
	before := len(h.getList("/api/sessions"))
	cmdsBefore := len(h.mock().CmdLog())
	h.decode("POST", "/api/restart-all", obj{}, 202, nil)
	status := func(id int64) string {
		var res obj
		h.decode("GET", "/api/restart-all", nil, 200, &res)
		for _, row := range res["last"].([]any) {
			if r := obj(row.(map[string]any)); r.id() == id {
				return r.str("status")
			}
		}
		return ""
	}
	deadline := time.Now().Add(60 * time.Second)
	for status(idle.id()) != "restarted" || status(optOut.id()) != "restarted" {
		if time.Now().After(deadline) {
			t.Fatalf("idle sessions not restarted: %s %s", status(idle.id()), status(optOut.id()))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := status(busy.id()); got != "pending" {
		t.Fatalf("a busy session must wait, got %s", got)
	}
	if after := len(h.getList("/api/sessions")); after != before {
		t.Fatalf("session count changed: %d → %d", before, after)
	}
	launches := map[string]string{}
	for _, c := range h.mock().CmdLog()[cmdsBefore:] {
		if strings.HasPrefix(c, "tmux new-session") {
			for _, row := range []obj{idle, optOut, busy, fresh} {
				if strings.Contains(c, fmt.Sprintf("lec-s%d", row.id())) {
					launches[row.str("name")] = c
				}
			}
		}
	}
	if c := launches["idle one"]; !strings.Contains(c, "00000000-0000-4000-8000-000000000001") || !strings.Contains(c, "--remote-control=idle one") {
		t.Fatalf("idle session did not resume its conversation with Remote Control: %s", c)
	}
	if c := launches["no phone"]; c == "" || strings.Contains(c, "--remote-control") {
		t.Fatalf("an opted-out session must come back without Remote Control: %s", c)
	}
	if _, ok := launches["busy one"]; ok {
		t.Fatal("a busy session was restarted mid-turn")
	}
	if _, ok := launches["no conversation yet"]; ok {
		t.Fatal("a session with nothing to resume was restarted")
	}
	got := h.sessionByID(idle.id())
	if got.str("name") != "idle one" || got.str("group_path") != "Work" || int64(got.num("project_id")) != pid {
		t.Fatalf("the record changed: %v", got)
	}
	// Once the busy one waits, the next pass restarts it.
	h.setRow(busy.id(), map[string]any{"agent_state": "waiting_input", "hook_seen_at": store.Now(), "status": "waiting", "updated_at": store.Now()})
	h.App.Sessions.RetryPending(t.Context())
	if got := status(busy.id()); got != "restarted" {
		t.Fatalf("the busy session was not restarted once idle: %s", got)
	}
}
