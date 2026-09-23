package api

import (
	"io"
	"net/http"
	"strings"

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
// nothing for the handler itself to branch on.
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
	// `{}` for every event, PermissionRequest included. This IS the
	// extension point docs/agent-events.md section 3 hands to a later
	// worker: hold-for-approval belongs inside IngestEvent (or a call from
	// here into the broker) before this response is written, answering with
	// a real {"hookSpecificOutput":{"decision":{"behavior":...}}} instead.
	// Until then, `{}` is a valid "no opinion" answer under both agents'
	// hook output schemas, and both fall back to their own terminal approval
	// prompt when they see it.
	writeJSON(w, 200, map[string]any{})
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
