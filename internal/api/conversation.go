package api

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/sessions"
	"github.com/JeremiahM37/lectern/v2/internal/shellq"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func (s *Server) sessionReader(w http.ResponseWriter, r *http.Request) {
	row, ok := s.sessionParam(w, r)
	if !ok {
		return
	}
	if row.Status == sessions.StatusDead {
		writeJSON(w, 200, map[string]any{"session": s.sessionView(row), "text": row.PaneTail, "ended": true})
		return
	}
	target, err := s.DB.Target(row.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	// Join soft-wrapped terminal lines, then let the phone wrap at its own width.
	// Exact target avoids tmux treating a session name as a prefix match.
	cmd := fmt.Sprintf("printf '%%s' %s; tmux capture-pane -p -t %s -S -500 -J", shellq.Quote(sessions.PollDelimiter+row.TmuxSession+"\n"), shellq.Quote("="+row.TmuxSession+":"))
	result, err := ex.Run(r.Context(), cmd, executor.RunOpts{Timeout: 15})
	if err != nil || !result.OK() {
		httpError(w, 502, "could not read the session; its target may be offline")
		return
	}
	text := sessions.ParsePoll(result.Stdout)[row.TmuxSession]
	if len(text) > 256000 {
		text = text[len(text)-256000:]
	}
	writeJSON(w, 200, map[string]any{"session": s.sessionView(row), "text": text, "ended": false})
}

func (s *Server) taskMessages(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	rows, err := s.DB.TaskMessages(task.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) sendTaskMessage(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Text      string `json:"text"`
		RequestID string `json:"request_id"`
		Interrupt bool   `json:"interrupt"`
	}
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "invalid message")
		return
	}
	if strings.TrimSpace(body.Text) == "" || len(body.Text) > 32000 || len(body.RequestID) < 8 || len(body.RequestID) > 100 {
		httpError(w, 422, "provide a message (up to 32000 bytes) and a request_id")
		return
	}
	count, _ := s.DB.Count("attempts", "task_id=? AND status IN ('queued','running')", task.ID)
	if count > 1 {
		httpError(w, 409, "wait for the parallel attempts to finish before sending a follow-up")
		return
	}
	project, err := s.DB.Project(task.ProjectID)
	if err != nil {
		respondErr(w, err)
		return
	}
	target, err := s.DB.Target(project.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	if body.Interrupt && target.Kind == "sandbox" {
		httpError(w, 409, "sandbox tasks accept queued follow-ups; interrupting would discard the container")
		return
	}
	// Retries with the same request id return the original receipt, not a second turn.
	_, err = s.DB.Exec(`INSERT INTO task_messages(task_id,request_id,text,interrupt,created_at) VALUES(?,?,?,?,?) ON CONFLICT(task_id,request_id) DO NOTHING`, task.ID, body.RequestID, body.Text, body.Interrupt, store.Now())
	if err != nil {
		respondErr(w, err)
		return
	}
	rows, err := s.DB.TaskMessages(task.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	for _, m := range rows {
		if m.RequestID == body.RequestID {
			if m.Text != body.Text || m.Interrupt != body.Interrupt {
				httpError(w, 409, "request_id was already used for another message")
				return
			}
			s.Bus.Publish("board", "task_message", m)
			writeJSON(w, 202, m)
			return
		}
	}
	httpError(w, 500, "message receipt unavailable")
}
