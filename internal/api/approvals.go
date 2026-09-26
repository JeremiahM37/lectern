package api

import (
	"net/http"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/policy"
	"github.com/JeremiahM37/lectern/v2/internal/scheduler"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// AgentTaskCap and NoteCap bound what one attempt can file, so a looping agent
// cannot flood the board or the memory store.
const (
	AgentTaskCap = 10
	NoteCap      = 20
)

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	status := r.URL.Query().Get("status")
	if status == "" {
		status = "pending"
	}
	rows, err := s.DB.ApprovalsByStatus(status)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

type decisionIn struct {
	Decision    string `json:"decision"`
	Note        string `json:"note"`
	AlwaysAllow bool   `json:"always_allow"`
	// ForSession is the session-scoped twin of AlwaysAllow: "don't ask again
	// for this tool (or this Bash command's first token) for the rest of
	// THIS session" rather than for the whole project forever. Meaningful
	// only for a session-scoped approval (SessionID != 0); a no-op on a task
	// attempt's approval, same as AlwaysAllow is a no-op on AttemptID==0.
	ForSession bool `json:"for_session"`
}

func (s *Server) decideApproval(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 409, "approval not pending")
		return
	}
	// Approvals gate a real, unsupervised action; deciding one needs a human
	// behind it, not just anything that can reach the API. In mode none the
	// whole control plane is unauthenticated by design, so this is skipped.
	if principal, _ := auth.FromContext(r.Context()); !s.Auth.CanDecide(principal) {
		httpError(w, 403, "approval decisions require a signed-in human (tailscale identity or access token)")
		return
	}
	var body decisionIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if body.Decision != "approved" && body.Decision != "denied" {
		httpError(w, 400, "decision must be approved|denied")
		return
	}
	row := s.Broker.Decide(id, body.Decision, body.Note, "user")
	if row == nil {
		httpError(w, 409, "approval not pending")
		return
	}
	// "always allow" writes a project policy rule, which only makes sense for
	// a task attempt's project — a session-scoped approval (AttemptID==0,
	// see docs/agent-events.md section 3) has no attempt to resolve a
	// project from, so the checkbox is simply a no-op there rather than an
	// error.
	if body.AlwaysAllow && body.Decision == "approved" && row.AttemptID != 0 {
		if proj, err := s.DB.ProjectForAttempt(row.AttemptID); err == nil {
			rule := policy.PatternFor(row.ToolName, row.Input)
			updated := policy.AddRule(policy.Parse(proj.PolicyJSON), rule)
			s.DB.Update("projects", proj.ID, map[string]any{"policy_json": store.J(updated)})
		}
	}
	// "Allow for this session" only makes sense for a session-scoped
	// approval (a task attempt's approval has no persistent session to
	// remember the rule against) — same shape as the AlwaysAllow guard
	// above, just keyed on SessionID instead of AttemptID.
	if body.ForSession && body.Decision == "approved" && row.SessionID != 0 {
		s.Broker.AllowForSession(row.SessionID, row.ToolName, row.Input)
	}
	writeJSON(w, 200, row)
}

// ---- agent-facing (per-attempt token auth, exempt from the API bearer) --------

type hookIn struct {
	Token     string         `json:"token"`
	ToolName  string         `json:"tool_name"`
	ToolInput map[string]any `json:"tool_input"`
}

func (s *Server) hookCreateApproval(w http.ResponseWriter, r *http.Request) {
	var body hookIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	att, err := s.DB.AttemptByToken(body.Token)
	if err != nil || att.Status != "running" {
		httpError(w, 403, "invalid attempt token")
		return
	}
	if body.ToolInput == nil {
		body.ToolInput = map[string]any{}
	}
	// server-side policy first: an operator who said "always allow this" should
	// never be paged for it again
	if proj, err := s.DB.ProjectForTask(att.TaskID); err == nil {
		if policy.Matches(policy.Parse(proj.PolicyJSON), body.ToolName, body.ToolInput) {
			aid, err := s.Broker.Create(att.ID, body.ToolName, body.ToolInput, true)
			if err != nil {
				respondErr(w, err)
				return
			}
			s.Broker.Decide(aid, "approved", "matched always-allow rule", "policy")
			writeJSON(w, 201, map[string]any{"id": aid})
			return
		}
	}
	aid, err := s.Broker.Create(att.ID, body.ToolName, body.ToolInput, false)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": aid})
}

func (s *Server) hookApprovalDecision(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r, "id")
	if err != nil {
		writeJSON(w, 200, map[string]any{"status": "unknown", "note": ""})
		return
	}
	row := s.Broker.Wait(r.Context(), id, s.Cfg.ApprovalPoll)
	if row == nil {
		writeJSON(w, 200, map[string]any{"status": "unknown", "note": ""})
		return
	}
	writeJSON(w, 200, map[string]any{"status": row.Status, "note": row.Note})
}

type hookTaskIn struct {
	Token    string `json:"token"`
	Title    string `json:"title"`
	Prompt   string `json:"prompt"`
	Dispatch bool   `json:"dispatch"`
	Priority *int   `json:"priority"`
}

// hookFileTask lets a running agent file a follow-up card. Capped per attempt so
// a runaway loop cannot bury the board.
func (s *Server) hookFileTask(w http.ResponseWriter, r *http.Request) {
	var body hookTaskIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	att, err := s.DB.AttemptByToken(body.Token)
	if err != nil || att.Status != "running" {
		httpError(w, 403, "invalid attempt token")
		return
	}
	filed, _ := s.DB.Count("tasks", "created_by_attempt=?", att.ID)
	if filed >= AgentTaskCap {
		httpError(w, 429, "attempt already filed %d tasks", AgentTaskCap)
		return
	}
	parent, err := s.DB.Task(att.TaskID)
	if err != nil {
		respondErr(w, err)
		return
	}
	priority := 2
	if body.Priority != nil {
		priority = *body.Priority
	}
	priority = max(0, min(4, priority))
	status := "backlog"
	if body.Dispatch {
		status = "queued"
	}
	prompt := body.Prompt
	if prompt == "" {
		prompt = body.Title
	}
	attID := att.ID
	task, err := s.DB.InsertTask(&store.Task{
		ProjectID: parent.ProjectID, Title: clip(body.Title, 120), Prompt: prompt,
		Status: status, Priority: priority, PermissionMode: parent.PermissionMode,
		Agent: parent.Agent, CreatedBy: "agent", ParentTaskID: &parent.ID,
		CreatedByAttempt: &attID})
	if err != nil {
		respondErr(w, err)
		return
	}
	if body.Dispatch {
		if _, err := s.Sched.CreateAttempt(task, scheduler.AttemptOpts{}); err != nil {
			respondErr(w, err)
			return
		}
	}
	s.Bus.Publish("board", "task", task)
	writeJSON(w, 201, map[string]any{"task_id": task.ID})
}

type hookNoteIn struct {
	Token string `json:"token"`
	Note  string `json:"note"`
}

// hookAddNote records a durable project note, injected into future dispatch
// prompts so the next agent starts where this one finished.
func (s *Server) hookAddNote(w http.ResponseWriter, r *http.Request) {
	var body hookNoteIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	att, err := s.DB.AttemptByToken(body.Token)
	if err != nil || att.Status != "running" {
		httpError(w, 403, "invalid attempt token")
		return
	}
	if trimSpace(body.Note) == "" {
		httpError(w, 400, "empty note")
		return
	}
	written, _ := s.DB.Count("memories", "created_by_attempt=?", att.ID)
	if written >= NoteCap {
		httpError(w, 429, "attempt already wrote %d notes", NoteCap)
		return
	}
	proj, err := s.DB.ProjectForTask(att.TaskID)
	if err != nil {
		respondErr(w, err)
		return
	}
	attID := att.ID
	nid, err := s.DB.InsertNote(proj.ID, clip(body.Note, 1000), &attID)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"note_id": nid})
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func trimSpace(s string) string { return strings.TrimSpace(s) }
