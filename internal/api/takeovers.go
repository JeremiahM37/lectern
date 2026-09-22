package api

import (
	"github.com/JeremiahM37/lectern/internal/store"
	"net/http"
)

// The scheduler owns the interruption, so it cannot race its own finalisation.
func (s *Server) takeoverTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	if tr, err := s.DB.Takeover(task.ID); err != nil {
		respondErr(w, err)
		return
	} else if tr != nil {
		if tr.Status == "failed" {
			_, err := s.DB.Exec("UPDATE task_takeovers SET status='pending',error='' WHERE task_id=?", task.ID)
			if err != nil {
				respondErr(w, err)
				return
			}
			tr.Status = "pending"
			tr.Error = ""
		}
		writeJSON(w, 202, tr)
		return
	}
	att, err := s.DB.LatestAttempt(task.ID)
	if err != nil || att.WorktreePath == "" || att.Status == "queued" {
		httpError(w, 409, "This task has not started yet, or its worktree has been cleaned up")
		return
	}
	proj, err := s.DB.Project(task.ProjectID)
	if err != nil {
		respondErr(w, err)
		return
	}
	target, err := s.DB.Target(proj.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	if target.Kind == "sandbox" || att.SandboxVMID != "" {
		httpError(w, 409, "Takeover needs a persistent target; sandbox runs cannot be taken over")
		return
	}
	// Recheck competing attempts and queued operator messages atomically with the
	// durable request. Database triggers fence new writers once it is accepted.
	tx, err := s.DB.BeginTx(r.Context(), nil)
	if err != nil {
		respondErr(w, err)
		return
	}
	defer tx.Rollback()
	var n int
	err = tx.QueryRow("SELECT count(*) FROM attempts WHERE task_id=? AND status IN ('queued','running') AND id!=?", task.ID, att.ID).Scan(&n)
	if err != nil {
		respondErr(w, err)
		return
	}
	if n > 0 {
		httpError(w, 409, "Wait for parallel attempts to finish before taking over")
		return
	}
	err = tx.QueryRow("SELECT count(*) FROM task_messages WHERE task_id=? AND status='pending'", task.ID).Scan(&n)
	if err != nil {
		respondErr(w, err)
		return
	}
	if n > 0 {
		httpError(w, 409, "A follow-up is queued; wait for it to start before taking over")
		return
	}
	_, err = tx.Exec("INSERT INTO task_takeovers(task_id,attempt_id,created_at) VALUES(?,?,?) ON CONFLICT(task_id) DO NOTHING", task.ID, att.ID, store.Now())
	if err != nil {
		respondErr(w, err)
		return
	}
	if err = tx.Commit(); err != nil {
		respondErr(w, err)
		return
	}
	tr, _ := s.DB.Takeover(task.ID)
	writeJSON(w, 202, tr)
	s.Bus.Publish("board", "task", task)
}
