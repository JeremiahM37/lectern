// The CI-aware PR loop's API surface (internal/ciloop, docs/ci-loop.md):
// arming a watch when the commit button opens a PR, the manual "watch this
// PR" endpoint for a PR the agent opened itself, and the card view.
package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/ciloop"
)

// armCIFromSteps arms the CI loop for the PR a commit request opened — or,
// when `gh pr create` reported that one already exists for the branch, for
// that one. It returns nil when there is no PR or the project has not opted
// in; an arming failure is logged, never turned into a failed commit. A
// successful push also makes the owner's active watches look again now,
// instead of after their backoff.
func (s *Server) armCIFromSteps(o ciloop.Owner, steps []map[string]any) *ciloop.View {
	if s.CILoop == nil {
		return nil
	}
	for _, step := range steps {
		if rc, _ := step["rc"].(int); step["step"] == "push" && rc == 0 {
			s.CILoop.Pushed(o)
		}
	}
	url := ""
	for _, step := range steps {
		if step["step"] != "pr" {
			continue
		}
		url, _ = step["url"].(string)
		if url == "" {
			if out, _ := step["output"].(string); strings.Contains(out, "already exists") {
				url = ciloop.FindPRURL(out)
			}
		}
	}
	if url == "" {
		return nil
	}
	row, err := s.CILoop.Arm(o, url, false)
	if err != nil {
		if !errors.Is(err, ciloop.ErrNotEnabled) {
			s.Log.Warn("ci loop: arming failed", "pr", url, "err", err)
		}
		return nil
	}
	return ciloop.ViewOf(row)
}

type ciArmIn struct {
	PRURL string `json:"pr_url"`
}

// armTaskCI is POST /api/tasks/{id}/ci — watch a PR the task's agent opened
// itself. A human asking explicitly overrides the project opt-in.
func (s *Server) armTaskCI(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	proj, err := s.DB.Project(task.ProjectID)
	if err != nil {
		respondErr(w, err)
		return
	}
	branch := ""
	if att, err := s.DB.LatestAttempt(task.ID); err == nil {
		branch = att.Branch
	}
	taskID, projectID := task.ID, proj.ID
	s.armCI(w, r, ciloop.Owner{TaskID: &taskID, ProjectID: &projectID, TargetID: proj.TargetID, Branch: branch})
}

// armSessionCI is POST /api/sessions/{id}/ci, the session counterpart.
func (s *Server) armSessionCI(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	sessionID := row.ID
	s.armCI(w, r, ciloop.Owner{SessionID: &sessionID, ProjectID: row.ProjectID, TargetID: row.TargetID})
}

func (s *Server) armCI(w http.ResponseWriter, r *http.Request, o ciloop.Owner) {
	// Arming makes Lectern send messages to an agent on a schedule, so like
	// running a check it is an operator action, not one an agent may take on
	// its own behalf through the general API.
	if principal, _ := auth.FromContext(r.Context()); !s.Auth.CanDecide(principal) {
		httpError(w, 403, "watching a PR requires a signed-in human (tailscale identity or access token)")
		return
	}
	if s.CILoop == nil {
		httpError(w, 501, "the CI loop is not configured")
		return
	}
	var in ciArmIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if !ciloop.ValidPRURL(in.PRURL) {
		httpError(w, 422, "pr_url must be a pull request URL like https://github.com/owner/repo/pull/12")
		return
	}
	row, err := s.CILoop.Arm(o, in.PRURL, true)
	if err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	writeJSON(w, 200, ciloop.ViewOf(row))
}

// latestCI is the card view of an owner's newest watch, or nil.
func (s *Server) latestCI(column string, id int64) *ciloop.View {
	row, err := s.DB.LatestCIWatch(column, id)
	if err != nil || row == nil {
		return nil
	}
	return ciloop.ViewOf(row)
}
