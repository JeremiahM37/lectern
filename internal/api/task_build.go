package api

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/state"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// The three calls a lead agent makes around a delegated build, beyond what
// the task API already had: wait for the worker to finish, read its report,
// and bring an accepted branch into the lead's own working tree.
//
//	GET  /api/tasks/{id}/wait?timeout=50    blocks until the task is not queued/running
//	GET  /api/tasks/{id}/report             the worker's completion report
//	POST /api/tasks/{id}/integrate          {"workdir": "..."} merge the attempt's branch there

// waitTask is a long poll. It returns the task view as soon as the task has
// left queued/running, or after `timeout` seconds with `done:false` so a
// client with its own deadline can call again; that is a wait, not polling
// for progress, because nothing is reported in between.
func (s *Server) waitTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	timeout := 50
	if v, err := strconv.Atoi(r.URL.Query().Get("timeout")); err == nil && v > 0 {
		timeout = v
	}
	if timeout > 3300 {
		timeout = 3300
	}
	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for {
		if task.Status != "queued" && task.Status != "running" {
			writeJSON(w, 200, map[string]any{"done": true, "task": s.view(task)})
			return
		}
		if time.Now().After(deadline) {
			writeJSON(w, 200, map[string]any{"done": false, "task": s.view(task)})
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(2 * time.Second):
		}
		fresh, err := s.DB.Task(task.ID)
		if err != nil {
			respondErr(w, err)
			return
		}
		task = fresh
	}
}

// taskReport is the last thing the worker said in its latest attempt. A
// worker that follows its brief ends with a structured completion report, so
// this is what a lead reads instead of the transcript.
func (s *Server) taskReport(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	att, report := s.latestAttemptReport(task.ID)
	if att == nil {
		writeJSON(w, 200, map[string]any{"report": "", "attempt": nil})
		return
	}
	writeJSON(w, 200, map[string]any{
		"report": report, "attempt": att.N, "status": task.Status, "exit_code": att.ExitCode,
		"branch": att.Branch, "worktree_path": att.WorktreePath,
		"diff_stat": store.UnjList(att.DiffStatJSON), "verify": store.UnjObj(att.VerifyJSON),
	})
}

// latestAttemptReport returns a task's newest attempt and the last thing that
// attempt said in its transcript — the worker's completion report, when it
// followed its brief. The attempt is nil when the task has never run. Shared
// by the REST report endpoint and A2A's GetTask artifact so both read the same
// text out of the same events.
func (s *Server) latestAttemptReport(taskID int64) (*store.Attempt, string) {
	att, err := s.DB.LatestAttempt(taskID)
	if err != nil {
		return nil, ""
	}
	rows, err := s.DB.TaskEvents(taskID, 0, &att.N)
	if err != nil {
		return att, ""
	}
	report := ""
	for _, ev := range rows {
		if ev.Type != "text" {
			continue
		}
		if t, _ := store.UnjObj(ev.PayloadJSON)["text"].(string); strings.TrimSpace(t) != "" {
			report = t
		}
	}
	return att, report
}

// integrateTask merges the latest attempt's branch into a working tree the
// caller names, on the project's target. It refuses a dirty tree for the
// files the branch touches only insofar as git does: a conflict leaves the
// merge aborted and is reported, never half-applied.
func (s *Server) integrateTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	var in struct {
		Workdir string `json:"workdir"`
		// Mode is "apply" (default): the branch's changes land in the working
		// tree uncommitted, the way a worker editing that tree would have left
		// them, and committing stays the operator's call. "merge" records a
		// merge commit instead.
		Mode string `json:"mode"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if !strings.HasPrefix(in.Workdir, "/") {
		httpError(w, 400, "workdir must be an absolute path")
		return
	}
	if err := state.Check(task.Status, "done"); err != nil && task.Status != "done" {
		httpError(w, 409, "integrate only from review or done (task is %s)", task.Status)
		return
	}
	att, err := s.DB.LatestAttempt(task.ID)
	if err != nil || att.Branch == "" {
		httpError(w, 409, "the task has no branch to integrate")
		return
	}
	proj, err := s.DB.Project(task.ProjectID)
	if err != nil {
		respondErr(w, err)
		return
	}
	_, ex, err := s.skillExecutor(r, proj, proj.TargetID)
	if err != nil {
		respondErr(w, err)
		return
	}
	q := executor.ShellQuote
	// A task's work is left uncommitted in its worktree so the diff can be
	// reviewed as a diff; a merge needs it on the branch first. Nothing to
	// commit is fine (a follow-up may already have committed).
	msg := strings.ReplaceAll(task.Title, `"`, "'")
	// The worker's tree may have no git identity of its own (a bare target
	// account); the record still needs one, and it says who made it.
	commit, err := ex.Run(r.Context(), `n=$(git config user.name || true); e=$(git config user.email || true); `+
		`git add -A && { git diff --cached --quiet || git -c "user.name=${n:-Lectern worker}" -c "user.email=${e:-lectern-worker@localhost}" commit -q -m "`+msg+`"; }`,
		executor.RunOpts{Cwd: att.WorktreePath, Timeout: 60})
	if err != nil {
		respondErr(w, err)
		return
	}
	if !commit.OK() {
		httpError(w, 409, "commit in the task worktree failed: %s", tail(commit.Stdout+commit.Stderr, 800))
		return
	}
	mode := in.Mode
	if mode == "" {
		mode = "apply"
	}
	var script string
	switch mode {
	case "apply":
		script = "cd " + q(in.Workdir) + " && git rev-parse --is-inside-work-tree >/dev/null && " +
			"{ git merge --squash " + q(att.Branch) + " 2>&1 && git reset -q && git status --short; } || { git merge --abort 2>/dev/null; git reset -q --merge 2>/dev/null; exit 65; }"
	case "merge":
		script = "cd " + q(in.Workdir) + " && git rev-parse --is-inside-work-tree >/dev/null && " +
			`n=$(git config user.name || true); e=$(git config user.email || true); ` +
			`git -c "user.name=${n:-Lectern}" -c "user.email=${e:-lectern@localhost}" merge --no-ff --no-edit ` + q(att.Branch) +
			" 2>&1 || { git merge --abort 2>/dev/null; exit 65; }"
	default:
		httpError(w, 400, "mode must be apply or merge")
		return
	}
	res, err := ex.Run(r.Context(), script, executor.RunOpts{Timeout: 120})
	if err != nil {
		respondErr(w, err)
		return
	}
	out := map[string]any{"merged": res.OK(), "mode": mode, "branch": att.Branch, "output": tail(res.Stdout+res.Stderr, 4000)}
	if !res.OK() {
		out["conflict"] = res.RC == 65
		writeJSON(w, 409, out)
		return
	}
	writeJSON(w, 200, out)
}
