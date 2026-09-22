package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/internal/store"
)

type shellIn struct {
	TargetID int64  `json:"target_id"`
	Machine  string `json:"machine"`
}

// createShell is the small, agent-free launch surface used by the terminal
// client and the terminal dashboard's Blank shell action. It intentionally has
// no project, profile, agent, model, memory, or worktree inputs.
func (s *Server) createShell(w http.ResponseWriter, r *http.Request) {
	var in shellIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, http.StatusUnprocessableEntity, "%s", err)
		return
	}
	if in.TargetID != 0 && strings.TrimSpace(in.Machine) != "" {
		httpError(w, http.StatusBadRequest, "choose target_id or machine, not both")
		return
	}
	var target *store.Target
	var err error
	if in.TargetID != 0 {
		target, err = s.DB.Target(in.TargetID)
	} else if machine := strings.TrimSpace(in.Machine); machine != "" {
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
