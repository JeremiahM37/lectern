package lecternbridge

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// allowWith is a Lectern allow decision carrying the remember flag and a note.
func allowWith(remember bool, message string) map[string]any {
	dec := map[string]any{"behavior": "allow", "remember": remember}
	if message != "" {
		dec["message"] = message
	}
	return map[string]any{"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": dec}}
}

func TestRememberFlagGrantsTheSession(t *testing.T) {
	f := newFakeLectern(t)
	f.decide = func(map[string]any) map[string]any { return allowWith(true, "") }
	local := newLocalHost(true, tools.Deny)
	h := newTestHost(f, local)
	if got := h.Approve(context.Background(), shellApproval); got != tools.AllowSession {
		t.Fatalf("Approve = %v, want AllowSession", got)
	}
}

func TestDenyReasonReachesTheAgent(t *testing.T) {
	f := newFakeLectern(t)
	f.decide = func(map[string]any) map[string]any {
		return map[string]any{"hookSpecificOutput": map[string]any{"decision": map[string]any{
			"behavior": "deny", "message": "use a dry run first",
		}}}
	}
	h := newTestHost(f, newLocalHost(true, tools.AllowOnce))
	ctx, reason := tools.WithReasonSlot(context.Background())
	if got := h.Approve(ctx, shellApproval); got != tools.Deny {
		t.Fatalf("Approve = %v, want Deny", got)
	}
	if reason() != "use a dry run first" {
		t.Fatalf("reason = %q", reason())
	}
}

func TestUnreachableLecternDeniesEvenWhenHeadless(t *testing.T) {
	f := newFakeLectern(t)
	cfg := f.config()
	cfg.HookURL = "http://127.0.0.1:1/api/hook/session/42" // nothing listens here
	headless := newLocalHost(false, tools.AllowOnce)
	close(headless.release)
	h := &approvalHost{b: newBridge(cfg), inner: headless}
	ctx, reason := tools.WithReasonSlot(context.Background())
	if got := h.Approve(ctx, shellApproval); got != tools.Deny {
		t.Fatalf("Approve = %v, want Deny: the local rule must not allow an unreachable Lectern's approval", got)
	}
	if !strings.Contains(reason(), "could not be reached") {
		t.Fatalf("reason = %q", reason())
	}
}

func TestClosingTheBridgeDeniesWaitingApprovals(t *testing.T) {
	f := newFakeLectern(t)
	// Lectern holds the request open (decide returns nil).
	local := newLocalHost(true, tools.AllowOnce)
	b := newBridge(f.config())
	h := &approvalHost{b: b, inner: local}
	ctx, reason := tools.WithReasonSlot(context.Background())
	done := make(chan tools.Decision, 1)
	go func() { done <- h.Approve(ctx, shellApproval) }()
	f.next(t) // the PermissionRequest is in flight

	b.Close()
	select {
	case d := <-done:
		if d != tools.Deny {
			t.Fatalf("Approve = %v, want Deny on shutdown", d)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("approval still waiting after the bridge closed")
	}
	if reason() != shutdownText {
		t.Fatalf("reason = %q", reason())
	}
}
