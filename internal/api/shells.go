package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

type shellIn struct {
	TargetID  int64  `json:"target_id"`
	Machine   string `json:"machine"`
	ProjectID *int64 `json:"project_id"`
}

// createShell is the small, agent-free launch surface used by the terminal
// client, the terminal dashboard's Blank shell action, and the new-terminal
// project picker. A caller names exactly one location: a machine (target_id or
// machine) for a scratch room, or a project_id to open directly in that
// project's repository. It intentionally has no profile, agent, model, memory,
// setup, or worktree inputs, and a project's target and path always come from
// the stored project rather than the request.
func (s *Server) createShell(w http.ResponseWriter, r *http.Request) {
	var in shellIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, http.StatusUnprocessableEntity, "%s", err)
		return
	}
	machine := strings.TrimSpace(in.Machine)
	if in.TargetID != 0 && machine != "" {
		httpError(w, http.StatusBadRequest, "choose target_id or machine, not both")
		return
	}
	if in.ProjectID != nil && (in.TargetID != 0 || machine != "") {
		httpError(w, http.StatusBadRequest, "choose project_id or a machine, not both")
		return
	}
	if in.ProjectID != nil {
		project, err := s.DB.Project(*in.ProjectID)
		if err != nil {
			httpError(w, http.StatusBadRequest, "no such project")
			return
		}
		if strings.TrimSpace(project.RepoPath) == "" {
			httpError(w, http.StatusBadRequest, "project has no repository path")
			return
		}
		sess, err := s.Sessions.LaunchProjectShell(r.Context(), project.ID)
		if err != nil {
			httpError(w, http.StatusConflict, "%s", err)
			return
		}
		writeJSON(w, http.StatusCreated, s.sessionView(sess))
		return
	}
	var target *store.Target
	var err error
	if in.TargetID != 0 {
		target, err = s.DB.Target(in.TargetID)
	} else if machine != "" {
		target, err = s.DB.TargetByName(machine)
	} else {
		in.TargetID = s.defaultTargetID()
		if in.TargetID != 0 {
			target, err = s.DB.Target(in.TargetID)
		} else {
			err = store.ErrNotFound
		}
	}
	if err != nil || target == nil {
		if errors.Is(err, store.ErrNotFound) {
			httpError(w, http.StatusBadRequest, "no such target")
			return
		}
		respondErr(w, err)
		return
	}
	sess, err := s.Sessions.LaunchShell(r.Context(), target.ID)
	if err != nil {
		httpError(w, http.StatusConflict, "%s", err)
		return
	}
	writeJSON(w, http.StatusCreated, s.sessionView(sess))
}
