package api

import (
	"net/http"

	"github.com/JeremiahM37/lectern/v2/internal/sessions"
)

// reviveSession replaces a session's agent process with a resume of the same
// conversation, in one request.
//
// An agent's TUI can hang while its process stays alive: it keeps reading
// keystrokes and never draws or acts on them, so the board says idle, the
// terminal says connected, and the operator sees a frozen prompt. That
// happened to a Codex session after a long turn (2026-09-22): the fix by hand
// was five steps in tmux. Here it is one: the bound native conversation is
// validated on the target first, then the process is killed and the exact
// conversation is resumed as a successor session, so nothing said is lost.
//
//	POST /api/sessions/{id}/revive  → 201 the successor session
func (s *Server) reviveSession(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if row.EndedAt != nil || row.Status == sessions.StatusDead {
		httpError(w, 409, "this session has ended; resume its conversation from the history picker instead")
		return
	}
	if row.SetupState == "creating" {
		httpError(w, 409, "workspace setup is still running; wait for it before restarting the agent")
		return
	}
	cid, err := s.boundNativeCID(r, row)
	if err != nil {
		httpError(w, 409, "no saved conversation is bound to this session (%s); stop it and start a new one", err)
		return
	}
	if err := s.Sessions.Kill(r.Context(), row.ID); err != nil {
		respondErr(w, err)
		return
	}
	next, err := s.Sessions.ResumeConversation(r.Context(), row.ID, cid, row.Name)
	if err != nil {
		httpError(w, 409, "the agent was stopped but its conversation could not be resumed: %s", err)
		return
	}
	writeJSON(w, 201, s.sessionView(next))
}
