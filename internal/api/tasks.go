package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/delegation"
	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/scheduler"
	"github.com/JeremiahM37/lectern/v2/internal/skills"
	"github.com/JeremiahM37/lectern/v2/internal/state"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/terminal"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

type taskIn struct {
	ProjectID int64    `json:"project_id"`
	Title     string   `json:"title"`
	Prompt    string   `json:"prompt"`
	Priority  *int     `json:"priority"`
	Labels    []string `json:"labels"`
	// Agent unset means "use the project's default_agent" — the toggle lives on
	// the project so quick-dispatch and templates inherit it too.
	Agent *string `json:"agent"`
	Model string  `json:"model"`
	// PermissionMode unset means "use the project's default_permission_mode",
	// falling back to acceptEdits — the same inheritance the agent toggle uses,
	// so quick-dispatch and templates pick up a project's gating instead of
	// silently bypassing it.
	PermissionMode *string `json:"permission_mode"`
	BaseBranch     string  `json:"base_branch"`
	// Orchestrate turns the task into an orchestrated build: the prompt is the
	// operator's description, and the attempt runs a lead that plans it, hands
	// the implementation to the delegated-build worker, reviews and integrates
	// the result into its own worktree. Requires Delegated builds to be on.
	Orchestrate bool `json:"orchestrate"`
}

func (s *Server) listTasks(w http.ResponseWriter, r *http.Request) {
	f := store.TaskFilter{Status: r.URL.Query().Get("status")}
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		f.ProjectID, _ = strconv.ParseInt(pid, 10, 64)
	}
	rows, err := s.DB.Tasks(f)
	if err != nil {
		respondErr(w, err)
		return
	}
	out := make([]*taskView, 0, len(rows))
	for _, t := range rows {
		out = append(out, s.view(t))
	}
	writeJSON(w, 200, out)
}

func (s *Server) getTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	writeJSON(w, 200, s.view(task))
}

func (s *Server) createTask(w http.ResponseWriter, r *http.Request) {
	var in taskIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if in.Title == "" {
		httpError(w, 422, "title is required")
		return
	}
	if in.Agent != nil && !s.knownAgent(*in.Agent) {
		httpError(w, 422, "agent must be one of %v", s.knownAgentNames())
		return
	}
	if in.PermissionMode != nil && !oneOf(*in.PermissionMode, permissionModes...) {
		httpError(w, 422, "permission_mode must be one of %v", permissionModes)
		return
	}
	priority := 2
	if in.Priority != nil {
		priority = *in.Priority
	}
	if priority < 0 || priority > 4 {
		httpError(w, 422, "priority must be 0-4")
		return
	}
	project, err := s.DB.Project(in.ProjectID)
	if err != nil {
		httpError(w, 400, "no such project")
		return
	}
	agent := strOr(in.Agent, orDefault(project.DefaultAgent, "claude"))
	labels := orEmpty(in.Labels)
	prompt := in.Prompt
	if in.Orchestrate {
		lead, err := s.orchestrationLead(project, in.Agent, in.Model)
		if err != nil {
			httpError(w, 400, "%s", err)
			return
		}
		agent, in.Model = lead.agent, lead.model
		if !delegation.IsLead(labels) {
			labels = append(labels, delegation.LeadLabel)
		}
		prompt = delegation.LeadPrompt(project.Name, firstNonEmptyStr(strings.TrimSpace(in.Prompt), in.Title), lead.cycles)
	}
	if _, ok := s.taskAgent(agent); !ok {
		httpError(w, 422, "agent %q has no non-interactive task definition; configure agent.task or use it for sessions only", agent)
		return
	}
	mode := strOr(in.PermissionMode, orDefault(project.DefaultPermissionMode, "acceptEdits"))
	spec, _ := s.taskAgent(agent)
	if err := taskPermissionError(spec, mode); err != nil {
		httpError(w, 400, "%s", err)
		return
	}
	task, err := s.DB.InsertTask(&store.Task{
		ProjectID: in.ProjectID, Title: in.Title, Prompt: prompt, Status: "backlog",
		Priority: priority, LabelsJSON: store.J(labels), Agent: agent,
		Model: in.Model, PermissionMode: mode, BaseBranch: in.BaseBranch,
	})
	if err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "task", task)
	writeJSON(w, 201, s.view(task))
}

type taskPatch struct {
	Title          *string   `json:"title"`
	Prompt         *string   `json:"prompt"`
	Status         *string   `json:"status"`
	Priority       *int      `json:"priority"`
	Labels         *[]string `json:"labels"`
	Model          *string   `json:"model"`
	PermissionMode *string   `json:"permission_mode"`
	BaseBranch     *string   `json:"base_branch"`
}

func (s *Server) patchTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	var p taskPatch
	if err := decodeBody(r, &p); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if p.PermissionMode != nil {
		spec, exists := s.taskAgent(task.Agent)
		if !exists {
			httpError(w, 422, "agent %q has no non-interactive task definition", task.Agent)
			return
		}
		if err := taskPermissionError(spec, *p.PermissionMode); err != nil {
			httpError(w, 400, "%s", err)
			return
		}
	}
	fields := map[string]any{}
	setStr(fields, "title", p.Title)
	setStr(fields, "prompt", p.Prompt)
	setStr(fields, "model", p.Model)
	setStr(fields, "permission_mode", p.PermissionMode)
	setStr(fields, "base_branch", p.BaseBranch)
	if p.Priority != nil {
		fields["priority"] = *p.Priority
	}
	if p.Labels != nil {
		fields["labels_json"] = store.J(orEmpty(*p.Labels))
	}
	if p.Status != nil {
		if err := state.Check(task.Status, *p.Status); err != nil {
			httpError(w, 409, "%s", err.Error())
			return
		}
		if *p.Status == "queued" { // moving a card into queued IS a dispatch
			httpError(w, 409, "use /dispatch to queue a task")
			return
		}
		fields["status"] = *p.Status
	}
	if len(fields) > 0 {
		fields["updated_at"] = store.Now()
		if err := s.DB.Update("tasks", task.ID, fields); err != nil {
			respondErr(w, err)
			return
		}
	}
	fresh, err := s.DB.Task(task.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "task", fresh)
	writeJSON(w, 200, s.view(fresh))
}

type dispatchIn struct {
	PermissionMode string `json:"permission_mode"`
	Model          string `json:"model"`
	// ModelB opens a second parallel attempt on another model, for A/B compare.
	ModelB string `json:"model_b"`
}

func (s *Server) dispatchTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	if task.Status == "done" {
		httpError(w, 409, "send a follow-up message to continue a completed task")
		return
	}
	if err := state.Check(task.Status, "queued"); err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	var body dispatchIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	fields := map[string]any{"status": "queued", "updated_at": store.Now()}
	if body.PermissionMode != "" {
		if !oneOf(body.PermissionMode, permissionModes...) {
			httpError(w, 422, "permission_mode must be one of %v", permissionModes)
			return
		}
		spec, exists := s.taskAgent(task.Agent)
		if !exists {
			httpError(w, 422, "agent %q has no non-interactive task definition", task.Agent)
			return
		}
		if err := taskPermissionError(spec, body.PermissionMode); err != nil {
			httpError(w, 400, "%s", err)
			return
		}
		fields["permission_mode"] = body.PermissionMode
	}
	if body.Model != "" {
		fields["model"] = body.Model
	}
	if err := s.DB.Update("tasks", task.ID, fields); err != nil {
		respondErr(w, err)
		return
	}
	fresh, err := s.DB.Task(task.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	if _, err := s.Sched.CreateAttempt(fresh, scheduler.AttemptOpts{}); err != nil {
		respondErr(w, err)
		return
	}
	if body.ModelB != "" {
		if _, err := s.Sched.CreateAttempt(fresh, scheduler.AttemptOpts{Model: body.ModelB}); err != nil {
			respondErr(w, err)
			return
		}
	}
	fresh, _ = s.DB.Task(task.ID)
	s.Bus.Publish("board", "task", fresh)
	writeJSON(w, 200, s.view(fresh))
}

type followupIn struct {
	Feedback string `json:"feedback"`
}

func (s *Server) followupTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	if task.Status != "review" {
		httpError(w, 409, "follow-up only from review")
		return
	}
	var body followupIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	last, lastErr := s.DB.LatestAttempt(task.ID)
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
	var opts scheduler.AttemptOpts
	if target.Kind == "sandbox" {
		// the old container is gone — a fresh sandbox, with the feedback carrying
		// all the context the new agent gets
		opts.Prompt = "A previous agent attempted this task and the operator requests " +
			"changes:\n\nORIGINAL TASK:\n" + task.Prompt +
			"\n\nREQUESTED CHANGES:\n" + body.Feedback
	} else {
		opts.Prompt = "The previous attempt finished. The operator reviewed the diff and " +
			"requests changes:\n\n" + body.Feedback + "\n\nApply them in this worktree."
		if lastErr == nil {
			opts.ResumeSession = last.SessionID
			opts.WorktreePath = last.WorktreePath
			opts.Branch = last.Branch
		}
	}
	if _, err := s.Sched.CreateAttempt(task, opts); err != nil {
		respondErr(w, err)
		return
	}
	if err := s.DB.Update("tasks", task.ID, map[string]any{
		"status": "queued", "updated_at": store.Now()}); err != nil {
		respondErr(w, err)
		return
	}
	fresh, _ := s.DB.Task(task.ID)
	s.Bus.Publish("board", "task", fresh)
	writeJSON(w, 200, s.view(fresh))
}

func (s *Server) completeTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	if err := state.Check(task.Status, "done"); err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	s.DB.Update("tasks", task.ID, map[string]any{"status": "done", "updated_at": store.Now()})
	fresh, _ := s.DB.Task(task.ID)
	s.Bus.Publish("board", "task", fresh)
	writeJSON(w, 200, s.view(fresh))
}

func (s *Server) cancelTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	if task.Status != "queued" && task.Status != "running" {
		httpError(w, 409, "cannot cancel from %s", task.Status)
		return
	}
	att, err := s.DB.OneAttemptWhere(
		"task_id=? AND status IN ('queued','running') ORDER BY n DESC", task.ID)
	if err == nil {
		s.Sched.CancelAttempt(r.Context(), att)
	} else {
		s.DB.Update("tasks", task.ID, map[string]any{
			"status": "cancelled", "updated_at": store.Now()})
	}
	fresh, _ := s.DB.Task(task.ID)
	s.Bus.Publish("board", "task", fresh)
	writeJSON(w, 200, s.view(fresh))
}

// deleteTask removes a task and every trace of it: attempts, events, approvals,
// notes, on-disk diffs and worktrees.
func (s *Server) deleteTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	s.removeTask(r.Context(), task)
	writeJSON(w, 200, map[string]any{"deleted": task.ID})
}

// removeTask tears down one task completely: anything still running is stopped,
// its worktrees are reclaimed, and every row and file that referenced it goes.
//
// Shared with the bulk clear rather than reimplemented there — a second copy of
// this would be the one that forgets the worktrees and quietly fills the disk.
func (s *Server) removeTask(ctx context.Context, task *store.Task) {
	if tr, _ := s.DB.Takeover(task.ID); tr != nil && tr.Status != "ready" {
		return
	}
	proj, _ := s.DB.Project(task.ProjectID)
	var target *store.Target
	if proj != nil {
		target, _ = s.DB.Target(proj.TargetID)
	}
	attempts, _ := s.DB.TaskAttempts(task.ID)

	// 1) stop anything live (kills tmux / destroys a sandbox / expires approvals)
	for _, a := range attempts {
		if a.Status == "queued" || a.Status == "running" {
			s.Sched.CancelAttempt(ctx, a)
		}
	}
	// 2) reclaim worktrees on non-sandbox targets (sandboxes self-destroy)
	if target != nil && target.Kind != "sandbox" && proj != nil && proj.KeepWorktrees == 0 {
		if ex, err := s.Reg.For(target); err == nil {
			for _, a := range attempts {
				if a.WorktreePath != "" && !s.DB.SessionWorkdir(target.ID, a.WorktreePath) {
					// Do not remove the worktree when target-local skill ownership
					// could not be cleaned; its evidence must remain recoverable.
					if cleanErr := skills.Clean(ctx, ex, s.DB, proj, a.WorktreePath); cleanErr != nil {
						continue
					}
					_ = worktree.Remove(ctx, ex, proj.RepoPath, a.WorktreePath)
				}
			}
		}
	}
	// 3) delete on-disk diffs and every row referencing this task's attempts
	for _, a := range attempts {
		os.Remove(filepath.Join(s.Cfg.DiffDir(), fmt.Sprintf("attempt-%d.patch", a.ID)))
		s.DB.Exec(`DELETE FROM events WHERE attempt_id=?`, a.ID)
		s.DB.Exec(`DELETE FROM approvals WHERE attempt_id=?`, a.ID)
		s.DB.Exec(`DELETE FROM memories WHERE created_by_attempt=?`, a.ID)
	}
	s.DB.Exec(`DELETE FROM task_takeovers WHERE task_id=?`, task.ID)
	s.DB.Exec(`DELETE FROM attempts WHERE task_id=?`, task.ID)
	// child tasks (agent-filed / reviewer-gate) are ORPHANED, not cascaded —
	// an agent-filed follow-up may be real work the operator wants to keep
	s.DB.Exec(`UPDATE tasks SET parent_task_id=NULL WHERE parent_task_id=?`, task.ID)
	s.DB.Exec(`DELETE FROM tasks WHERE id=?`, task.ID)
	s.Bus.Publish("board", "task_deleted", map[string]any{"id": task.ID})
}

// clearIn asks for a sweep of finished cards off the board.
type clearIn struct {
	// Statuses defaults to the finished ones. Naming a live status is refused
	// unless Force is set, because clearing a running task kills its agent.
	Statuses  []string `json:"statuses"`
	ProjectID *int64   `json:"project_id"`
	Force     bool     `json:"force"`
}

// finishedStatuses are the ones a card can be in when there is nothing left to
// do with it — what "clear the board" means without having to say so.
var finishedStatuses = []string{"done", "failed", "cancelled"}

// clearTasks sweeps finished cards off the board.
//
// A board that has run for a month is mostly history, and deleting eighty cards
// one at a time is not a thing anyone does — so they pile up and the board stops
// being glanceable, which is the only thing it is for.
func (s *Server) clearTasks(w http.ResponseWriter, r *http.Request) {
	var in clearIn
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	statuses := in.Statuses
	if len(statuses) == 0 {
		statuses = finishedStatuses
	}
	for _, st := range statuses {
		if !oneOf(st, "queued", "running", "review", "done", "failed", "cancelled") {
			httpError(w, 422, "unknown status %q", st)
			return
		}
		// review is waiting on YOU, and queued/running have an agent attached;
		// none of them are what "clear finished work" means
		if !oneOf(st, finishedStatuses...) && !in.Force {
			httpError(w, 409, "%q is not finished work — clearing it would discard "+
				"a task that is still live. Pass force:true if that is what you mean", st)
			return
		}
	}

	where := "status IN (" + strings.TrimSuffix(strings.Repeat("?,", len(statuses)), ",") + ")"
	args := make([]any, 0, len(statuses)+1)
	for _, st := range statuses {
		args = append(args, st)
	}
	if in.ProjectID != nil {
		where += " AND project_id=?"
		args = append(args, *in.ProjectID)
	}
	tasks, err := s.DB.TasksWhere(where, args...)
	if err != nil {
		respondErr(w, err)
		return
	}
	cleared := 0
	for _, task := range tasks {
		if tr, _ := s.DB.Takeover(task.ID); tr != nil && tr.Status != "ready" {
			continue
		}
		s.removeTask(r.Context(), task)
		cleared++
	}
	s.Log.Info("cleared finished tasks", "count", cleared, "statuses", statuses)
	writeJSON(w, 200, map[string]any{"cleared": cleared, "statuses": statuses})
}

type commitIn struct {
	Message string `json:"message"`
	Push    bool   `json:"push"`
	PR      bool   `json:"pr"`
}

func (s *Server) commitTask(w http.ResponseWriter, r *http.Request) {
	task, proj, target, att, ok := s.workCtx(w, r)
	if !ok {
		return
	}
	if task.Status != "review" && task.Status != "done" {
		httpError(w, 409, "cannot commit from %s", task.Status)
		return
	}
	var body commitIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	_ = proj
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	ctx := r.Context()
	msg := strings.ReplaceAll(orDefault(body.Message, task.Title), `"`, "'")
	steps := []map[string]any{}

	res, err := ex.Run(ctx, fmt.Sprintf(`git add -A && git commit -m "%s"`, msg),
		executor.RunOpts{Cwd: att.WorktreePath, Timeout: 60})
	if err != nil {
		respondErr(w, err)
		return
	}
	output := clipEnd(res.Stdout+res.Stderr, 800)
	steps = append(steps, map[string]any{"step": "commit", "rc": res.RC, "output": output})
	if !res.OK() {
		detail := output
		if strings.Contains(res.Stdout+res.Stderr, "nothing to commit") {
			detail = "nothing to commit"
		}
		httpError(w, 409, "commit failed: %s", detail)
		return
	}
	if body.Push {
		res, err := ex.Run(ctx, "git push -u origin "+att.Branch,
			executor.RunOpts{Cwd: att.WorktreePath, Timeout: 120})
		if err != nil {
			respondErr(w, err)
			return
		}
		steps = append(steps, map[string]any{"step": "push", "rc": res.RC,
			"output": clipEnd(res.Stdout+res.Stderr, 800)})
		if body.PR && res.RC != 0 {
			body.PR = false // never open a PR for a branch that failed to push
		}
	}
	if body.PR {
		title := strings.ReplaceAll(task.Title, `"`, "'")
		res, err := ex.Run(ctx, fmt.Sprintf(
			`gh pr create --head %s --title "%s" --body "Created by lectern task #%d."`,
			att.Branch, title, task.ID),
			executor.RunOpts{Cwd: att.WorktreePath, Timeout: 120})
		if err != nil {
			respondErr(w, err)
			return
		}
		url := ""
		if res.OK() {
			if lines := strings.Fields(strings.TrimSpace(res.Stdout)); len(lines) > 0 {
				parts := strings.Split(strings.TrimSpace(res.Stdout), "\n")
				url = strings.TrimSpace(parts[len(parts)-1])
			}
		}
		steps = append(steps, map[string]any{"step": "pr", "rc": res.RC,
			"output": clipEnd(res.Stdout+res.Stderr, 800), "url": url})
	}
	s.Bus.Publish(fmt.Sprintf("task:%d", task.ID), "git", map[string]any{"steps": steps})
	writeJSON(w, 200, map[string]any{"steps": steps})
}

func (s *Server) cleanupTask(w http.ResponseWriter, r *http.Request) {
	task, proj, target, _, ok := s.workCtx(w, r)
	if !ok {
		return
	}
	if !oneOf(task.Status, "done", "failed", "cancelled") {
		httpError(w, 409, "cleanup only after done/failed/cancelled")
		return
	}
	ex, err := s.Reg.For(target)
	if err != nil {
		respondErr(w, err)
		return
	}
	removed := []int{}
	list, _ := s.DB.AttemptsWhere("task_id=? AND worktree_path!=''", task.ID)
	for _, a := range list {
		if s.DB.SessionWorkdir(target.ID, a.WorktreePath) {
			httpError(w, 409, "Worktree belongs to an interactive session")
			return
		}
		if cleanErr := skills.Clean(r.Context(), ex, s.DB, proj, a.WorktreePath); cleanErr != nil {
			respondErr(w, cleanErr)
			return
		}
		if err := worktree.Remove(r.Context(), ex, proj.RepoPath, a.WorktreePath); err != nil {
			respondErr(w, err)
			return
		}
		s.DB.Update("attempts", a.ID, map[string]any{"worktree_path": ""})
		removed = append(removed, a.N)
	}
	writeJSON(w, 200, map[string]any{"removed_attempts": removed})
}

func (s *Server) attachTerminal(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	att, err := s.DB.OneAttemptWhere("task_id=? AND status='running' ORDER BY n DESC", task.ID)
	if err != nil || att.TmuxSession == "" {
		httpError(w, 409, "no running tmux session to attach")
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
	port, err := s.Terminals.Attach(r.Context(), terminal.Attachment{
		Key:         fmt.Sprintf("attempt:%d", att.ID),
		TmuxSession: att.TmuxSession, SandboxVMID: att.SandboxVMID,
	}, target)
	if err != nil {
		httpError(w, 503, "%s", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"port": port,
		"url": fmt.Sprintf("/term/attempt/%d/", att.ID)})
}

func (s *Server) taskEvents(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	var afterSeq int64
	if v := r.URL.Query().Get("after_seq"); v != "" {
		afterSeq, _ = strconv.ParseInt(v, 10, 64)
	}
	var attemptN *int
	if v := r.URL.Query().Get("attempt_n"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			attemptN = &n
		}
	}
	rows, err := s.DB.TaskEvents(task.ID, afterSeq, attemptN)
	if err != nil {
		respondErr(w, err)
		return
	}
	writeJSON(w, 200, rows)
}

func (s *Server) taskDiff(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	var att *store.Attempt
	var err error
	if v := r.URL.Query().Get("attempt_n"); v != "" {
		n, _ := strconv.Atoi(v)
		att, err = s.DB.OneAttemptWhere("task_id=? AND n=? AND diff_stat_json!='{}'", task.ID, n)
	} else {
		att, err = s.DB.OneAttemptWhere(
			"task_id=? AND diff_stat_json!='{}' ORDER BY n DESC", task.ID)
	}
	if err != nil {
		httpError(w, 404, "no diff captured yet")
		return
	}
	patch := ""
	if raw, err := os.ReadFile(filepath.Join(s.Cfg.DiffDir(),
		fmt.Sprintf("attempt-%d.patch", att.ID))); err == nil {
		patch = string(raw)
	}
	writeJSON(w, 200, map[string]any{
		"attempt_n": att.N,
		"stats":     store.UnjList(att.DiffStatJSON),
		"files":     worktree.SplitPatch(patch),
	})
}

// ---- shared lookups ----------------------------------------------------------

func (s *Server) taskParam(w http.ResponseWriter, r *http.Request) (*store.Task, bool) {
	id, err := pathID(r, "id")
	if err != nil {
		httpError(w, 404, "no such task")
		return nil, false
	}
	task, err := s.DB.Task(id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			httpError(w, 404, "no such task")
		} else {
			respondErr(w, err)
		}
		return nil, false
	}
	if r.Method != "GET" && !strings.HasSuffix(r.URL.Path, "/takeover") {
		if tr, _ := s.DB.Takeover(task.ID); tr != nil && !(r.Method == "DELETE" && tr.Status == "ready") {
			httpError(w, 409, "This task is moving to an interactive session; open its session to continue")
			return nil, false
		}
	}
	return task, true
}

// workCtx resolves the task plus the latest attempt that actually has a worktree.
func (s *Server) workCtx(w http.ResponseWriter, r *http.Request) (*store.Task, *store.Project,
	*store.Target, *store.Attempt, bool) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return nil, nil, nil, nil, false
	}
	att, err := s.DB.OneAttemptWhere("task_id=? AND worktree_path!='' ORDER BY n DESC", task.ID)
	if err != nil {
		httpError(w, 409, "task has no worktree yet")
		return nil, nil, nil, nil, false
	}
	proj, err := s.DB.Project(task.ProjectID)
	if err != nil {
		respondErr(w, err)
		return nil, nil, nil, nil, false
	}
	target, err := s.DB.Target(proj.TargetID)
	if err != nil {
		respondErr(w, err)
		return nil, nil, nil, nil, false
	}
	return task, proj, target, att, true
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}
