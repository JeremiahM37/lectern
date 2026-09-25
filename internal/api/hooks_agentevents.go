package api

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/agentevents"
	"github.com/JeremiahM37/lectern/v2/internal/awareness"
	"github.com/JeremiahM37/lectern/v2/internal/budget"
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
	// Claim board (docs/claims.md): a session ending releases everything it
	// still holds immediately, rather than waiting for the sweep's dead-
	// session catch-all. Side effect only — SessionEnd never carries
	// additionalContext back to an agent that is already gone.
	if event == agentevents.EventSessionEnd && s.Claims != nil {
		if _, err := s.Claims.ReleaseAllForSession(sess.ID); err != nil {
			s.Log.Warn("claims: could not release on session end", "session", sess.ID, "err", err)
		}
	}
	// Cross-agent awareness (docs/agent-events.md "Cross-agent awareness"):
	// SessionStart/UserPromptSubmit get a peer briefing, PreToolUse gets an
	// overwrite/conflict warning. Both ride the SAME `hookSpecificOutput`
	// shape Claude/Codex already deliver verbatim to the agent's context —
	// see agentevents.EventPermissionRequest's reply above for the sibling
	// use of this shape.
	texts := []string{s.awarenessAdditionalContext(sess, event, body), s.claimsAdditionalContext(sess, event, body)}
	// Budgets (docs/budgets.md "Interactive sessions"): a session is never
	// killed for spend, but the next UserPromptSubmit tells the agent a
	// "stop"-mode budget is exhausted and asks it to stop and summarise —
	// the same hookSpecificOutput/additionalContext channel awareness above
	// already uses, so this rides for free rather than needing its own hook
	// wiring.
	if event == agentevents.EventUserPromptSubmit {
		texts = append(texts, s.budgetAdditionalContext(sess))
	}
	if text := joinNonEmpty(texts, "\n\n"); text != "" {
		writeJSON(w, 200, map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": event, "additionalContext": text,
		}})
		return
	}
	// `{}` for every other event. Both agents' hook output schemas treat it
	// as "no opinion", and (for PermissionRequest specifically, in bypass
	// mode or any mode this build does not otherwise gate) fall back to
	// their own terminal approval prompt when they see it.
	writeJSON(w, 200, map[string]any{})
}

// awarenessHookInput is the subset of PreToolUse/PostToolUse/UserPromptSubmit
// bodies awareness reads — a strict subset of permissionRequestIn/the
// notificationProbe shapes already used above, since every Claude/Codex
// hook reuses the same wire format (docs/agent-events.md section 2's codex
// probing note).
type awarenessHookInput struct {
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
	Prompt    string         `json:"prompt"`
}

// awarenessSettingOn is the same "default ON, off only when explicitly '0'"
// convention internal/alerts.Watcher.enabled uses for its toggles.
func (s *Server) awarenessSettingOn(key string) bool {
	return strings.TrimSpace(s.DB.Setting(key)) != "0"
}

// awarenessAdditionalContext is the one place cross-agent awareness touches
// the hook response. It ALWAYS kicks off EnsureRepoKeyAsync first (a no-op
// once resolved) so a session's repo_key eventually gets populated even if
// every awareness setting is off, and it never shells out itself — see
// internal/awareness's package doc for why that split matters to keeping
// this fast enough for a hook response.
func (s *Server) awarenessAdditionalContext(sess *store.Session, event string, body []byte) string {
	if s.Awareness == nil {
		return ""
	}
	s.Awareness.EnsureRepoKeyAsync(sess)
	var in awarenessHookInput
	if len(body) > 0 {
		_ = json.Unmarshal(body, &in)
	}
	if in.ToolInput == nil {
		in.ToolInput = map[string]any{}
	}
	switch event {
	case agentevents.EventSessionStart:
		if !s.awarenessSettingOn("awareness_briefing") {
			return ""
		}
		text, ok := s.Awareness.Briefing(sess)
		if !ok {
			return ""
		}
		return text
	case agentevents.EventUserPromptSubmit:
		s.Awareness.RecordPrompt(sess, in.Prompt)
		if !s.awarenessSettingOn("awareness_briefing") {
			return ""
		}
		text, ok := s.Awareness.Briefing(sess)
		if !ok {
			return ""
		}
		return text
	case agentevents.EventPreToolUse:
		// Record as intent (docs/agent-events.md point 1a): a peer whose own
		// PreToolUse lands a moment later already sees this in-flight edit,
		// not just ones PostToolUse has confirmed. EditWarning excludes the
		// calling session's own rows, so this can never warn a session about
		// itself.
		if absPath, ok := awareness.FilePathFromToolInput(in.ToolName, in.ToolInput); ok {
			s.Awareness.RecordEdit(sess, absPath)
		}
		if !s.awarenessSettingOn("awareness_edit_warning") {
			return ""
		}
		text, ok := s.Awareness.EditWarning(sess, in.ToolName, in.ToolInput)
		if !ok {
			return ""
		}
		return text
	case agentevents.EventPostToolUse:
		if absPath, ok := awareness.FilePathFromToolInput(in.ToolName, in.ToolInput); ok {
			s.Awareness.RecordEdit(sess, absPath)
		}
		return ""
	default:
		return ""
	}
}

// budgetAdditionalContext is docs/budgets.md's "Interactive sessions"
// clause: never kills the session, but tells the agent plainly once a
// "stop"-mode overall or per-agent budget is exhausted, so it can wrap up on
// its own terms instead of being cut off mid-edit. Checked fresh on every
// UserPromptSubmit rather than cached — the same reasoning budget.Gate's own
// doc comment gives for task dispatch and session launch.
func (s *Server) budgetAdditionalContext(sess *store.Session) string {
	label, exhausted := budget.ExhaustedLabel(s.DB, sess.Agent)
	if !exhausted {
		return ""
	}
	return "Budget notice: " + label + ". Please stop what you are doing, " +
		"summarise your progress and hand off cleanly — do not start new work " +
		"until the operator raises the limit or a new period begins."
}

// joinNonEmpty joins only the non-blank strings in parts with sep.
func joinNonEmpty(parts []string, sep string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, sep)
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
