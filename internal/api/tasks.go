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
	// createdBy overrides InsertTask's "user" default for the caller that is
	// not a person. It is unexported because it is not part of the REST
	// request body — the PWA and the CLI always file as "user"; A2A's
	// SendMessage sets "a2a" so the board, and A2A's own ListTasks, can tell
	// which tasks arrived over the protocol.
	createdBy string
}

// taskRequestError is a failure from the shared create/dispatch/cancel bodies
// below, carrying the HTTP status the REST handlers report. It exists so
// A2A's JSON-RPC surface can file, queue and cancel a task through exactly the
// code the REST API uses and still map the failure onto a JSON-RPC error
// instead of an HTTP one.
type taskRequestError struct {
	status int
	msg    string
}

func (e *taskRequestError) Error() string { return e.msg }

func requestErr(status int, format string, args ...any) error {
	return &taskRequestError{status: status, msg: fmt.Sprintf(format, args...)}
}

// respondTaskErr reports a shared-path failure, or falls back to respondErr
// for the storage errors that path can also return.
func respondTaskErr(w http.ResponseWriter, err error) {
	var re *taskRequestError
	if errors.As(err, &re) {
		httpError(w, re.status, "%s", re.msg)
		return
	}
	respondErr(w, err)
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
	task, err := s.buildTask(in)
	if err != nil {
		respondTaskErr(w, err)
		return
	}
	writeJSON(w, 201, s.view(task))
}

// buildTask is createTask's body without the HTTP plumbing: validate the
// request, insert the task and announce it on the board. It is shared with
// A2A's SendMessage (internal/api/a2a.go) so a task that arrives over the
// protocol is validated — known agent, permission mode, the project's own
// defaults — by the identical code rather than by a second implementation
// that could drift from it.
func (s *Server) buildTask(in taskIn) (*store.Task, error) {
	if in.Title == "" {
		return nil, requestErr(422, "title is required")
	}
	if in.Agent != nil && !s.knownAgent(*in.Agent) {
		return nil, requestErr(422, "agent must be one of %v", s.knownAgentNames())
	}
	if in.PermissionMode != nil && !oneOf(*in.PermissionMode, permissionModes...) {
		return nil, requestErr(422, "permission_mode must be one of %v", permissionModes)
	}
	priority := 2
	if in.Priority != nil {
		priority = *in.Priority
	}
	if priority < 0 || priority > 4 {
		return nil, requestErr(422, "priority must be 0-4")
	}
	project, err := s.DB.Project(in.ProjectID)
	if err != nil {
		return nil, requestErr(400, "no such project")
	}
	agent := strOr(in.Agent, orDefault(project.DefaultAgent, "claude"))
	labels := orEmpty(in.Labels)
	prompt := in.Prompt
	if in.Orchestrate {
		lead, err := s.orchestrationLead(project, in.Agent, in.Model)
		if err != nil {
			return nil, requestErr(400, "%s", err)
		}
		agent, in.Model = lead.agent, lead.model
		if !delegation.IsLead(labels) {
			labels = append(labels, delegation.LeadLabel)
		}
		prompt = delegation.LeadPrompt(project.Name, firstNonEmptyStr(strings.TrimSpace(in.Prompt), in.Title), lead.cycles)
	}
	if _, ok := s.taskAgent(agent); !ok {
		return nil, requestErr(422, "agent %q has no non-interactive task definition; configure agent.task or use it for sessions only", agent)
	}
	mode := strOr(in.PermissionMode, orDefault(project.DefaultPermissionMode, "acceptEdits"))
	spec, _ := s.taskAgent(agent)
	if err := taskPermissionError(spec, mode); err != nil {
		return nil, requestErr(400, "%s", err)
	}
	task, err := s.DB.InsertTask(&store.Task{
		ProjectID: in.ProjectID, Title: in.Title, Prompt: prompt, Status: "backlog",
		Priority: priority, LabelsJSON: store.J(labels), Agent: agent,
		Model: in.Model, PermissionMode: mode, BaseBranch: in.BaseBranch,
		CreatedBy: in.createdBy,
	})
	if err != nil {
		return nil, err
	}
	s.Bus.Publish("board", "task", task)
	return task, nil
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

// variantIn is one Best-of-N attempt to dispatch: its own agent/model/gating,
// or a saved launch profile naming them instead. Unset fields fall back to
// the task's own agent/permission_mode, exactly like a bare dispatch always
// has — a single-variant dispatch is indistinguishable from the old
// single-attempt path, and two variants (agent/permission_mode left unset,
// only model set) is exactly the old model/model_b shape.
type variantIn struct {
	Agent          string `json:"agent"`
	Model          string `json:"model"`
	LaunchProfile  string `json:"launch_profile"`
	PermissionMode string `json:"permission_mode"`
}

const maxDispatchVariants = 8

type dispatchIn struct {
	PermissionMode string `json:"permission_mode"`
	Model          string `json:"model"`
	// ModelB opens a second parallel attempt on another model, for A/B compare.
	// Kept working as a two-variant shorthand once Variants generalised it.
	ModelB string `json:"model_b"`
	// Variants generalises model/model_b to 1..8 attempts, each free to pick
	// its own agent, model, launch profile and permission mode.
	Variants []variantIn `json:"variants"`
}

// resolveVariant fills a variant's agent/model from its named launch profile
// (an explicit agent/model on the variant always wins), then validates the
// result exactly like createTask/dispatchTask already validate a task's own
// agent and permission mode — a variant is not allowed to reach the scheduler
// with a combination a plain task could never have gotten past.
func (s *Server) resolveVariant(v variantIn, fallbackAgent, fallbackPermission string) (agent, model, permission string, err error) {
	agent, model, permission = v.Agent, v.Model, v.PermissionMode
	if v.LaunchProfile != "" {
		profiles, perr := s.DB.LaunchProfiles()
		if perr != nil {
			return "", "", "", perr
		}
		var found *store.LaunchProfile
		for _, p := range profiles {
			if strings.EqualFold(p.Name, v.LaunchProfile) {
				found = p
				break
			}
		}
		if found == nil {
			return "", "", "", fmt.Errorf("no launch profile named %q", v.LaunchProfile)
		}
		if agent == "" {
			agent = found.Agent
		}
		if model == "" {
			model = found.Model
		}
	}
	agent = strOr(&agent, fallbackAgent)
	permission = strOr(&permission, fallbackPermission)
	if !s.knownAgent(agent) {
		return "", "", "", fmt.Errorf("agent must be one of %v", s.knownAgentNames())
	}
	spec, exists := s.taskAgent(agent)
	if !exists {
		return "", "", "", fmt.Errorf("agent %q has no non-interactive task definition", agent)
	}
	if !oneOf(permission, permissionModes...) {
		return "", "", "", fmt.Errorf("permission_mode must be one of %v", permissionModes)
	}
	if err := taskPermissionError(spec, permission); err != nil {
		return "", "", "", err
	}
	return agent, model, permission, nil
}

func (s *Server) dispatchTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	var body dispatchIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	fresh, err := s.queueTask(task, body)
	if err != nil {
		respondTaskErr(w, err)
		return
	}
	writeJSON(w, 200, s.view(fresh))
}

// queueTask is dispatchTask's body without the HTTP plumbing: check the task
// can be queued at all, validate the dispatch (including every variant) before
// touching the database, then create the attempt(s). Shared with A2A's
// SendMessage so a queued task is queued by one implementation.
func (s *Server) queueTask(task *store.Task, body dispatchIn) (*store.Task, error) {
	if task.Status == "done" {
		return nil, requestErr(409, "send a follow-up message to continue a completed task")
	}
	if err := state.Check(task.Status, "queued"); err != nil {
		return nil, requestErr(409, "%s", err.Error())
	}
	if len(body.Variants) > 0 && body.ModelB != "" {
		return nil, requestErr(422, "use either variants or model_b, not both")
	}
	if len(body.Variants) > maxDispatchVariants {
		return nil, requestErr(422, "at most %d variants per dispatch", maxDispatchVariants)
	}
	fields := map[string]any{"status": "queued", "updated_at": store.Now()}
	if body.PermissionMode != "" {
		if !oneOf(body.PermissionMode, permissionModes...) {
			return nil, requestErr(422, "permission_mode must be one of %v", permissionModes)
		}
		spec, exists := s.taskAgent(task.Agent)
		if !exists {
			return nil, requestErr(422, "agent %q has no non-interactive task definition", task.Agent)
		}
		if err := taskPermissionError(spec, body.PermissionMode); err != nil {
			return nil, requestErr(400, "%s", err)
		}
		fields["permission_mode"] = body.PermissionMode
	}
	if body.Model != "" {
		fields["model"] = body.Model
	}

	// Validate every variant BEFORE touching the database — a dispatch either
	// queues cleanly or not at all, never half of an N-way spread.
	type resolved struct{ agent, model, permission string }
	var variants []resolved
	for _, v := range body.Variants {
		agent, model, permission, verr := s.resolveVariant(v, task.Agent, orDefault(body.PermissionMode, task.PermissionMode))
		if verr != nil {
			return nil, requestErr(422, "%s", verr.Error())
		}
		variants = append(variants, resolved{agent, model, permission})
	}

	if err := s.DB.Update("tasks", task.ID, fields); err != nil {
		return nil, err
	}
	fresh, err := s.DB.Task(task.ID)
	if err != nil {
		return nil, err
	}
	if len(variants) == 0 {
		if _, err := s.Sched.CreateAttempt(fresh, scheduler.AttemptOpts{}); err != nil {
			return nil, err
		}
		if body.ModelB != "" {
			if _, err := s.Sched.CreateAttempt(fresh, scheduler.AttemptOpts{Model: body.ModelB}); err != nil {
				return nil, err
			}
		}
	} else {
		for _, v := range variants {
			opts := scheduler.AttemptOpts{Model: v.model}
			if v.agent != fresh.Agent {
				opts.Agent = v.agent
			}
			if v.permission != fresh.PermissionMode {
				opts.PermissionMode = v.permission
			}
			if _, err := s.Sched.CreateAttempt(fresh, opts); err != nil {
				return nil, err
			}
		}
	}
	fresh, _ = s.DB.Task(task.ID)
	s.Bus.Publish("board", "task", fresh)
	return fresh, nil
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
	s.followupWithFeedback(w, r, task, body.Feedback)
}

// followupWithFeedback sends a reviewed task back for another attempt with
// feedback, however that feedback was assembled — a plain follow-up note
// (followupTask) or formatted inline review comments (reviewTask).
func (s *Server) followupWithFeedback(w http.ResponseWriter, r *http.Request, task *store.Task, feedback string) {
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
			"\n\nREQUESTED CHANGES:\n" + feedback
	} else {
		opts.Prompt = "The previous attempt finished. The operator reviewed the diff and " +
			"requests changes:\n\n" + feedback + "\n\nApply them in this worktree."
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

type steerIn struct {
	Text string `json:"text"`
}

// steerTask delivers a follow-up message to a running attempt's driver
// without cancelling and redispatching it. Only meaningful for an attempt
// whose recorded driver accepts one (attempt.driver — see
// internal/drivers.Select); anything else returns 409, same status the UI
// already uses for "not valid from this state".
func (s *Server) steerTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	var body steerIn
	if err := decodeBody(r, &body); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		httpError(w, 422, "text is required")
		return
	}
	if task.Status != "running" {
		httpError(w, 409, "can only steer a running task")
		return
	}
	att, err := s.DB.OneAttemptWhere("task_id=? AND status='running' ORDER BY n DESC", task.ID)
	if err != nil {
		httpError(w, 409, "no running attempt")
		return
	}
	if err := s.Sched.Steer(r.Context(), att.ID, body.Text); err != nil {
		httpError(w, 409, "%s", err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
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
	fresh, err := s.cancelTaskRecord(r.Context(), task)
	if err != nil {
		respondTaskErr(w, err)
		return
	}
	writeJSON(w, 200, s.view(fresh))
}

// cancelTaskRecord is cancelTask's body without the HTTP plumbing, shared with
// A2A's CancelTask so both surfaces stop an attempt the same way: through the
// scheduler, so the agent process and its worktree are released, with the
// direct status write only as the fallback for a task that has no live
// attempt row to cancel.
func (s *Server) cancelTaskRecord(ctx context.Context, task *store.Task) (*store.Task, error) {
	if task.Status != "queued" && task.Status != "running" {
		return nil, requestErr(409, "cannot cancel from %s", task.Status)
	}
	att, err := s.DB.OneAttemptWhere(
		"task_id=? AND status IN ('queued','running') ORDER BY n DESC", task.ID)
	if err == nil {
		s.Sched.CancelAttempt(ctx, att)
	} else {
		s.DB.Update("tasks", task.ID, map[string]any{
			"status": "cancelled", "updated_at": store.Now()})
	}
	fresh, _ := s.DB.Task(task.ID)
	s.Bus.Publish("board", "task", fresh)
	return fresh, nil
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
	if r.Method != "GET" && task.CreatedBy == autoOwner {
		httpError(w, 409, "Workshop tasks use isolated workers; manage them from Autonomous workshop")
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
