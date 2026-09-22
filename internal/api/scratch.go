package api

import (
	"net/http"
	"strconv"

	"github.com/JeremiahM37/lectern/v2/internal/scratch"
)

// Scratch workspaces are made freely and were never removed. The sweep trashes
// the ones where nothing happened; these routes show what it sees and let a
// person decide about the ones where something did.

func (s *Server) scratchSweeper() *scratch.Sweeper {
	w := s.Sched.Scratch()
	if w.Days <= 0 {
		// The sweep is off, but a report is still useful: show what a week would do.
		w.Days = 7
	}
	return w
}

// scratchReport is a dry run: it changes nothing.
func (s *Server) scratchReport(w http.ResponseWriter, r *http.Request) {
	if s.Cfg.Mock {
		writeJSON(w, 200, &scratch.Result{DryRun: true, Targets: []scratch.TargetReport{}, Trashed: []string{}})
		return
	}
	target, _ := strconv.ParseInt(r.URL.Query().Get("target_id"), 10, 64)
	res, err := s.scratchSweeper().Sweep(r.Context(), true, target)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

type scratchSweepIn struct {
	DryRun   bool  `json:"dry_run"`
	TargetID int64 `json:"target_id"`
}

func (s *Server) scratchSweep(w http.ResponseWriter, r *http.Request) {
	var in scratchSweepIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if s.Cfg.Mock {
		httpError(w, 409, "the mock target has no scratch workspaces")
		return
	}
	res, err := s.scratchSweeper().Sweep(r.Context(), in.DryRun, in.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, res)
}

type scratchDirIn struct {
	TargetID int64  `json:"target_id"`
	Name     string `json:"name"`
}

func (s *Server) scratchDecision(w http.ResponseWriter, r *http.Request, decide func(*scratch.Sweeper, scratchDirIn) error) {
	var in scratchDirIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err)
		return
	}
	if in.TargetID <= 0 || !scratch.SafeName(in.Name) {
		httpError(w, 422, "target_id and a scratch directory name are required")
		return
	}
	if err := decide(s.scratchSweeper(), in); err != nil {
		httpError(w, 409, "%s", err)
		return
	}
	w.WriteHeader(204)
}

func (s *Server) scratchDiscard(w http.ResponseWriter, r *http.Request) {
	s.scratchDecision(w, r, func(sw *scratch.Sweeper, in scratchDirIn) error {
		return sw.Discard(r.Context(), in.TargetID, in.Name)
	})
}

func (s *Server) scratchKeep(w http.ResponseWriter, r *http.Request) {
	s.scratchDecision(w, r, func(sw *scratch.Sweeper, in scratchDirIn) error {
		return sw.Keep(r.Context(), in.TargetID, in.Name)
	})
}
