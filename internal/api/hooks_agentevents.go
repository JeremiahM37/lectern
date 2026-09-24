package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// hookBodyLimit bounds one hook/statusline body. Generous for anything a real
// CLI sends (a statusline payload is a few KB; a PreToolUse tool_input could
// in principle carry a large file edit), small enough that a misbehaving or
// hostile caller cannot use this unauthenticated-by-design route group to
// exhaust memory — the token check below still runs first regardless.
const hookBodyLimit = 1 << 20

// sessionFromHookAuth resolves the {id} path value and checks the
// Authorization: Bearer header against that exact session's hook_token, in
// constant time (agentevents.ValidToken). Per docs/agent-events.md section 2
// this is the ONLY auth for the group — deliberately not the general API
// bearer withAuth gates everything else with, and deliberately per-session
// rather than per-attempt like the older /api/hook/approval token, so one
// session's agent can never reach another session's hook endpoint even if it
// somehow learned the other session's id.
//
// A missing session and a wrong token both answer 401 identically, so this
// endpoint cannot be used to enumerate session ids.
func (s *Server) sessionFromHookAuth(w http.ResponseWriter, r *http.Request) (*store.Session, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 401, "unauthorized")
		return nil, false
	}
	sess, err := s.DB.Session(id)
	if err != nil {
		httpError(w, 401, "unauthorized")
		return nil, false
	}
	supplied := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !agentevents.ValidToken(sess.HookToken, supplied) {
		httpError(w, 401, "unauthorized")
		return nil, false
	}
	return sess, true
}

// hookSessionEvent is POST /api/hook/session/{id}/{event} — one Claude or
// Codex hook invocation. Unknown event names are accepted and ignored
// (docs/agent-events.md: "Unknown events are accepted and ignored"), which
// IngestEvent already does by returning ok=false from MapEventState; there is
// nothing for the handler itself to branch on beyond the two cases below.
func (s *Server) hookSessionEvent(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFromHookAuth(w, r)
	if !ok {
		return
	}
	event := r.PathValue("event")
	body, _ := io.ReadAll(io.LimitReader(r.Body, hookBodyLimit))
	if _, _, err := s.Events.IngestEvent(sess, event, body); err != nil {
		respondErr(w, err)
		return
	}
	// A PostToolUse or Notification hook proves the session moved past
	// whatever it was blocked on through some path other than a held
	// PermissionRequest response (its own terminal dialog, most likely) —
	// any approval still "pending" for this session is therefore stale and
	// must not linger (docs/agent-events.md section 3). This runs
	// regardless of permission mode: a session switched from "ask" mid-run
	// can still have an old row to clean up.
	if event == agentevents.EventPostToolUse || event == agentevents.EventNotification {
		s.Broker.ExpireForSession(sess.ID)
	}
	if event == agentevents.EventPermissionRequest && sess.PermissionMode == "ask" {
		s.holdSessionPermissionRequest(w, r, sess, body)
		return
	}
	// `{}` for every other event. Both agents' hook output schemas treat it
	// as "no opinion", and (for PermissionRequest specifically, in bypass
	// mode or any mode this build does not otherwise gate) fall back to
	// their own terminal approval prompt when they see it.
	writeJSON(w, 200, map[string]any{})
}

// permissionRequestIn is the input Claude/Codex send a PermissionRequest
// hook — the same tool_name/tool_input shape the older task-attempt hook
// (hookCreateApproval in approvals.go) already parses, since both agents
// reuse Claude Code's wire format byte-for-byte (docs/agent-events.md
// section 2's codex probing note).
type permissionRequestIn struct {
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
}

// holdSessionPermissionRequest is the docs/agent-events.md section 3
// extension point: it creates a session-scoped approval (internal/broker),
// pushes the actionable "approval" notification, and holds this ONE HTTP
// request open until a human decides or LECTERN_APPROVAL_HOLD passes.
// Unlike the task hook's hookApprovalDecision, there is no long-poll loop
// here — Claude's `type: "http"` PermissionRequest hook makes exactly one
// request and waits for exactly one response, so the entire hold happens in
// this single call via broker.WaitOnce.
func (s *Server) holdSessionPermissionRequest(w http.ResponseWriter, r *http.Request, sess *store.Session, body []byte) {
	var in permissionRequestIn
	_ = json.Unmarshal(body, &in)
	if in.ToolInput == nil {
		in.ToolInput = map[string]any{}
	}
	id, err := s.Broker.CreateForSession(sess.ID, in.ToolName, in.ToolInput)
	if err != nil {
		// Our own bookkeeping failing must never block the agent's tool
		// call: fall back to the same "no opinion" answer an unreachable
		// lectern would produce.
		writeJSON(w, 200, map[string]any{})
		return
	}
	hold := s.Cfg.SessionApprovalHold
	if hold <= 0 {
		hold = 120 * time.Second
	}
	row := s.Broker.WaitOnce(r.Context(), id, hold)
	writeJSON(w, 200, permissionRequestResponse(row))
}

// permissionRequestResponse renders the contract's exact reply shape.
// Anything other than a clean approve/deny — still pending (the request
// context was cancelled), expired, or a lookup failure — degrades to `{}`,
// which both agents treat as "no opinion" and fall back to their own
// terminal prompt for, exactly as a timeout does.
func permissionRequestResponse(row *store.Approval) map[string]any {
	if row == nil {
		return map[string]any{}
	}
	var decision map[string]any
	switch row.Status {
	case "approved":
		decision = map[string]any{"behavior": "allow"}
	case "denied":
		decision = map[string]any{"behavior": "deny"}
		if row.Note != "" {
			decision["message"] = row.Note
		}
	default:
		return map[string]any{}
	}
	return map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": agentevents.EventPermissionRequest,
			"decision":      decision,
		},
	}
}

// hookSessionStatusline is POST /api/hook/session/{id}/statusline — Claude's
// statusline script's background curl. Its caller never looks at the
// response (see the generated script in agentevents.ClaudeSettingsInstallCommand),
// so there is nothing here worth returning beyond 200/`{}`; a failure is
// still reported via the status code for anyone testing the endpoint by hand.
func (s *Server) hookSessionStatusline(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.sessionFromHookAuth(w, r)
	if !ok {
		return
	}
	body, _ := io.ReadAll(io.LimitReader(r.Body, hookBodyLimit))
	if err := s.Events.IngestStatusline(sess, body); err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{})
}
