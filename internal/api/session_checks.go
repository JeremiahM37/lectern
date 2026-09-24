// Session checks: the project's verify_cmd (or auto-detected .verify.yaml)
// run against a session's worktree, triggered on an agent Stop and shown on
// the card. See internal/checks and docs/agent-events.md section 4.
package api

import (
	"context"
	"net/http"
	"strconv"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/checks"
)

// sessionChecks is GET /api/sessions/{id}/checks — newest first, ?limit=.
func (s *Server) sessionChecks(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	rows, err := s.DB.SessionChecks(row.ID, limit)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

// runSessionCheck is POST /api/sessions/{id}/checks — "run now". Like an
// approval decision, this is a real action an operator takes, not something
// an agent should be able to trigger on its own behalf through the general
// API, so it needs the same human principal approvals require.
func (s *Server) runSessionCheck(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if principal, _ := auth.FromContext(r.Context()); !s.Auth.CanDecide(principal) {
		httpError(w, 403, "running a check requires a signed-in human (tailscale identity or access token)")
		return
	}
	if s.Checks == nil {
		httpError(w, 501, "checks are not configured")
		return
	}
	// Fire-and-forget with a background context, same as OnAgentStop: the
	// command can legitimately run for minutes, and this request should not
	// hold the connection open for it. The UI watches the session.check bus
	// event (or polls GET .../checks) for the result.
	go s.Checks.RunForSession(context.Background(), row.ID, checks.ReasonManual)
	writeJSON(w, 202, map[string]any{"started": true})
}

// projectCheckCommand is GET /api/projects/{id}/check-command — the "Check
// command: auto-detected: verify run .verify.yaml" preview the project
// settings UI shows when verify_cmd is empty.
func (s *Server) projectCheckCommand(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such project")
		return
	}
	proj, err := s.DB.Project(id)
	if err != nil {
		respondErr(w, err)
		return
	}
	if s.Checks == nil {
		writeJSON(w, 200, map[string]any{"command": "", "source": "none"})
		return
	}
	cmd, source := s.Checks.DetectCommand(r.Context(), proj)
	writeJSON(w, 200, map[string]any{"command": cmd, "source": source})
}
