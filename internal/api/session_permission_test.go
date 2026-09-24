package api_test

// Session permission mode and the PermissionRequest hold/decide flow
// (docs/agent-events.md section 3). These exercise the real HTTP server and
// internal/broker together, the same way hooks_agentevents_test.go does for
// section 2 — hookSessionEvent's PermissionRequest branch is the extension
// point that file's comment pointed at.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func TestSessionPermissionModeDefaultsToBypass(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	if got := sess.str("permission_mode"); got != "bypass" {
		t.Fatalf("permission_mode = %q, want bypass", got)
	}
	if got := h.sessionByID(sess.id()).str("permission_mode"); got != "bypass" {
		t.Fatalf("persisted permission_mode = %q, want bypass", got)
	}
}

func TestSessionPermissionModeExplicitAsk(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "a", "agent": "claude", "permission_mode": "ask"})
	if got := sess.str("permission_mode"); got != "ask" {
		t.Fatalf("permission_mode = %q, want ask", got)
	}
}

// yolo:false predates permission_mode entirely; a caller that only knows the
// old field must still get the new hook wired up, since "let the agent ask"
// is exactly what that field always meant.
func TestSessionPermissionModeDerivedFromLegacyYoloFalse(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "a", "agent": "claude", "yolo": false})
	if got := sess.str("permission_mode"); got != "ask" {
		t.Fatalf("permission_mode = %q, want ask", got)
	}
}

func TestSessionPermissionModeFollowsGlobalDefaultSetting(t *testing.T) {
	h := newHarness(t)
	h.decode("PUT", "/api/settings", obj{"session_permission_mode": "ask"}, 200, nil)
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	if got := sess.str("permission_mode"); got != "ask" {
		t.Fatalf("permission_mode = %q, want ask (from the global default)", got)
	}
	// An explicit per-launch override still wins over the global default.
	explicit := h.session(obj{"project_id": pid, "name": "b", "agent": "claude", "permission_mode": "bypass"})
	if got := explicit.str("permission_mode"); got != "bypass" {
		t.Fatalf("per-launch override ignored: %q", got)
	}
}

func TestSessionPermissionModeRejectsUnknownValue(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	if code := h.status("POST", "/api/sessions",
		obj{"project_id": pid, "name": "a", "agent": "claude", "permission_mode": "sometimes"}); code != 400 {
		t.Fatalf("got %d, want 400", code)
	}
}

// askSession launches an "ask"-mode session and returns it plus its hook token.
func askSession(t *testing.T, h *harness, name string) (obj, string) {
	t.Helper()
	pid := h.seededProjectID()
	sess := h.session(obj{"project_id": pid, "name": name, "agent": "claude", "permission_mode": "ask"})
	return sess, hookToken(t, h, sess.id())
}

type hookResult struct {
	code int
	body []byte
}

// postPermissionRequest fires the hook in a goroutine — Claude's real
// PermissionRequest hook is exactly one held HTTP request, so the test has
// to be on the other side of it to decide while it is still in flight.
func postPermissionRequest(h *harness, sessID int64, tok string, toolName string, toolInput map[string]any) <-chan hookResult {
	body, _ := json.Marshal(obj{
		"hook_event_name": "PermissionRequest", "tool_name": toolName, "tool_input": toolInput,
	})
	out := make(chan hookResult, 1)
	go func() {
		code, respBody := h.rawRequest("POST",
			fmt.Sprintf("/api/hook/session/%d/PermissionRequest", sessID), string(body), tok)
		out <- hookResult{code, respBody}
	}()
	return out
}

func waitPendingSessionApproval(t *testing.T, h *harness, sessID int64) obj {
	t.Helper()
	var found obj
	h.waitUntil(fmt.Sprintf("a pending approval on session %d", sessID), func() bool {
		for _, a := range h.pendingApprovals() {
			if int64(a.num("session_id")) == sessID {
				found = a
				return true
			}
		}
		return false
	})
	return found
}

func TestPermissionRequestHeldThenApprovedReturnsAllow(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SessionApprovalHold = 5 * time.Second })
	sess, tok := askSession(t, h, "ask-allow")
	results := postPermissionRequest(h, sess.id(), tok, "Bash", map[string]any{"command": "ls -la"})

	appr := waitPendingSessionApproval(t, h, sess.id())
	if appr.str("tool_name") != "Bash" {
		t.Fatalf("approval tool_name = %v", appr)
	}
	if int64(appr.num("attempt_id")) != 0 {
		t.Fatalf("a session approval must not carry an attempt_id: %v", appr)
	}
	decided := h.post(fmt.Sprintf("/api/approvals/%d/decision", appr.id()), obj{"decision": "approved"}, 200)
	if decided.str("status") != "approved" {
		t.Fatalf("decide: %v", decided)
	}

	select {
	case r := <-results:
		if r.code != 200 {
			t.Fatalf("hook response code = %d: %s", r.code, r.body)
		}
		var out struct {
			HookSpecificOutput struct {
				HookEventName string `json:"hookEventName"`
				Decision      struct {
					Behavior string `json:"behavior"`
				} `json:"decision"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(r.body, &out); err != nil {
			t.Fatalf("response not JSON: %s", r.body)
		}
		if out.HookSpecificOutput.HookEventName != "PermissionRequest" || out.HookSpecificOutput.Decision.Behavior != "allow" {
			t.Fatalf("hook response = %s", r.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the held hook never returned after being decided")
	}
}

func TestPermissionRequestHeldThenDeniedCarriesNoteAsMessage(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SessionApprovalHold = 5 * time.Second })
	sess, tok := askSession(t, h, "ask-deny")
	results := postPermissionRequest(h, sess.id(), tok, "Edit", map[string]any{"file_path": "prod.env"})

	appr := waitPendingSessionApproval(t, h, sess.id())
	h.post(fmt.Sprintf("/api/approvals/%d/decision", appr.id()),
		obj{"decision": "denied", "note": "not touching prod.env from a phone"}, 200)

	select {
	case r := <-results:
		var out struct {
			HookSpecificOutput struct {
				Decision struct {
					Behavior string `json:"behavior"`
					Message  string `json:"message"`
				} `json:"decision"`
			} `json:"hookSpecificOutput"`
		}
		if err := json.Unmarshal(r.body, &out); err != nil {
			t.Fatalf("response not JSON: %s", r.body)
		}
		if out.HookSpecificOutput.Decision.Behavior != "deny" {
			t.Fatalf("behavior = %q", out.HookSpecificOutput.Decision.Behavior)
		}
		if out.HookSpecificOutput.Decision.Message != "not touching prod.env from a phone" {
			t.Fatalf("message = %q", out.HookSpecificOutput.Decision.Message)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the held hook never returned after being decided")
	}
}

func TestPermissionRequestTimesOutToEmptyObjectAndExpires(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SessionApprovalHold = 150 * time.Millisecond })
	sess, tok := askSession(t, h, "ask-timeout")
	results := postPermissionRequest(h, sess.id(), tok, "Bash", map[string]any{"command": "rm -rf /tmp/x"})
	appr := waitPendingSessionApproval(t, h, sess.id())

	select {
	case r := <-results:
		if r.code != 200 || strings.TrimSpace(string(r.body)) != "{}" {
			t.Fatalf("timeout response = %d %s, want 200 {}", r.code, r.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hold never timed out")
	}
	found := false
	for _, a := range h.getList("/api/approvals?status=expired") {
		if a.id() == appr.id() {
			found = true
		}
	}
	if !found {
		t.Fatal("a timed-out session approval must be marked expired")
	}
}

// The session's own terminal answering first (its PostToolUse fires because
// the operator used Claude's own fallback dialog, or anything else moved the
// session past the tool call) must retire a still-pending approval so it
// does not linger forever on the board.
func TestPostToolUseExpiresAStalePendingApproval(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.SessionApprovalHold = 5 * time.Second })
	sess, tok := askSession(t, h, "ask-race")
	results := postPermissionRequest(h, sess.id(), tok, "Bash", map[string]any{"command": "echo hi"})
	appr := waitPendingSessionApproval(t, h, sess.id())

	code, body := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PostToolUse", sess.id()), `{}`, tok)
	if code != 200 {
		t.Fatalf("PostToolUse: %d %s", code, body)
	}
	h.waitUntil("the stale approval to expire", func() bool {
		for _, a := range h.getList("/api/approvals?status=expired") {
			if a.id() == appr.id() {
				return true
			}
		}
		return false
	})
	select {
	case r := <-results:
		if strings.TrimSpace(string(r.body)) != "{}" {
			t.Fatalf("held hook should still just answer {} once expired out from under it: %s", r.body)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("PostToolUse expiring the approval should have woken the held hook")
	}
}

func TestSessionApprovalDecisionNeedsHumanInTailscaleMode(t *testing.T) {
	h := newHarness(t, func(c *config.Config) {
		c.Auth = "tailscale"
		c.TailscaleUsers = "nobody@example.com"
		c.SessionApprovalHold = 5 * time.Second
	})
	sess, tok := askSession(t, h, "ask-auth")
	results := postPermissionRequest(h, sess.id(), tok, "Bash", map[string]any{"command": "echo hi"})
	appr := waitPendingSessionApproval(t, h, sess.id())

	code, body := h.request("POST", fmt.Sprintf("/api/approvals/%d/decision", appr.id()),
		obj{"decision": "approved"}, nil)
	if code != 403 {
		t.Fatalf("a local principal deciding a session approval in tailscale mode: got %d, want 403 — %s", code, body)
	}
	// The rejected decision leaves the approval pending; let the hold's own
	// timeout resolve the still-running goroutine rather than leaking it.
	<-results
}
