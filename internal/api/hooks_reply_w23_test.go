package api_test

// Wave 23 (REAUDIT3-05-06 #16, #17): what a held PermissionRequest hook tells
// the agent. An approval carries the person's note, and an approval nobody
// decided in time is an explicit deny rather than "no opinion".

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

// decisionOf parses a PermissionRequest reply and returns its decision object,
// failing if the reply is not the hookSpecificOutput shape at all.
func decisionOf(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatalf("reply not JSON: %s", body)
	}
	hso, ok := raw["hookSpecificOutput"].(map[string]any)
	if !ok || hso["hookEventName"] != "PermissionRequest" {
		t.Fatalf("reply is not a PermissionRequest decision: %s", body)
	}
	decision, ok := hso["decision"].(map[string]any)
	if !ok {
		t.Fatalf("reply has no decision object: %s", body)
	}
	return decision
}

// An approval with a note returns allow with that note as the message, so the
// agent sees the condition the person attached.
func TestW23ApprovedWithNoteReturnsAllowWithMessage(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SessionApprovalHold = 5 * time.Second })
	sess, tok := askSession(t, h, "w23-allow-note")
	results := postPermissionRequest(h, sess.id(), tok, "Bash", map[string]any{"command": "make test"})

	appr := waitPendingSessionApproval(t, h, sess.id())
	h.post(fmt.Sprintf("/api/approvals/%d/decision", appr.id()),
		obj{"decision": "approved", "note": "only against the staging database"}, 200)

	select {
	case r := <-results:
		if r.code != 200 {
			t.Fatalf("hook response code = %d: %s", r.code, r.body)
		}
		decision := decisionOf(t, r.body)
		if decision["behavior"] != "allow" {
			t.Fatalf("behavior = %v, want allow: %s", decision["behavior"], r.body)
		}
		if decision["message"] != "only against the staging database" {
			t.Fatalf("message = %v: %s", decision["message"], r.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the held hook never returned after being decided")
	}
}

// A plain approve with no note keeps the bare allow: no empty message key.
func TestW23ApprovedWithoutNoteOmitsMessage(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SessionApprovalHold = 5 * time.Second })
	sess, tok := askSession(t, h, "w23-allow-bare")
	results := postPermissionRequest(h, sess.id(), tok, "Bash", map[string]any{"command": "ls"})

	appr := waitPendingSessionApproval(t, h, sess.id())
	h.post(fmt.Sprintf("/api/approvals/%d/decision", appr.id()), obj{"decision": "approved"}, 200)

	select {
	case r := <-results:
		decision := decisionOf(t, r.body)
		if decision["behavior"] != "allow" {
			t.Fatalf("behavior = %v: %s", decision["behavior"], r.body)
		}
		if _, has := decision["message"]; has {
			t.Fatalf("a bare approve must not carry a message: %s", r.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the held hook never returned after being decided")
	}
}
