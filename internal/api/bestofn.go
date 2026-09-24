package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/scheduler"
	"github.com/JeremiahM37/lectern/v2/internal/skills"
	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/worktree"
)

// pickAttemptTask makes one Best-of-N attempt the task's canonical one: every
// existing single-attempt endpoint (commit, integrate, taskReport, the diff
// default) reads store.DB.LatestAttempt, the highest N — so instead of
// teaching each of them a second "which attempt" argument, the picked
// attempt's N is swapped with whichever attempt currently holds the highest
// N. Every other attempt's worktree is then reclaimed, the same reclaim
// cleanupTask already does, since a Best-of-N comparison is exactly the kind
// of task that leaves N-1 worktrees nobody will ever look at again.
func (s *Server) pickAttemptTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	var in struct {
		N int `json:"n"`
	}
	if err := decodeBody(r, &in); err != nil {
		httpError(w, 422, "%s", err.Error())
		return
	}
	if !oneOf(task.Status, "review", "done", "failed") {
		httpError(w, 409, "pick an attempt only once the task has stopped running")
		return
	}
	attempts, err := s.DB.TaskAttempts(task.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	if len(attempts) < 2 {
		httpError(w, 409, "only one attempt exists — nothing to pick between")
		return
	}
	var winner, highest *store.Attempt
	for _, a := range attempts {
		if a.N == in.N {
			winner = a
		}
		if highest == nil || a.N > highest.N {
			highest = a
		}
	}
	if winner == nil {
		httpError(w, 404, "no attempt #%d on this task", in.N)
		return
	}
	if winner.ID != highest.ID {
		// Swap N's so LatestAttempt (highest N) now resolves to the winner.
		// attempts.n carries no uniqueness constraint, so a plain swap is safe.
		s.DB.Update("attempts", highest.ID, map[string]any{"n": winner.N})
		s.DB.Update("attempts", winner.ID, map[string]any{"n": highest.N})
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
	archived := []int{}
	if target.Kind != "sandbox" && proj.KeepWorktrees == 0 {
		if ex, err := s.Reg.For(target); err == nil {
			for _, a := range attempts {
				if a.ID == winner.ID || a.WorktreePath == "" {
					continue
				}
				if s.DB.SessionWorkdir(target.ID, a.WorktreePath) {
					continue // an interactive session owns it; leave it alone
				}
				if cleanErr := skills.Clean(r.Context(), ex, s.DB, proj, a.WorktreePath); cleanErr != nil {
					continue
				}
				if err := worktree.Remove(r.Context(), ex, proj.RepoPath, a.WorktreePath); err == nil {
					s.DB.Update("attempts", a.ID, map[string]any{"worktree_path": ""})
					archived = append(archived, a.N)
				}
			}
		}
	}
	fresh, err := s.DB.Task(task.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "task", fresh)
	writeJSON(w, 200, map[string]any{"task": s.view(fresh), "archived_attempts": archived})
}

// judgeTask spawns a headless judge over a task's finished attempts: an
// ordinary queued task (created_by "judge"), run through the normal
// scheduler like anything else, whose prompt already contains everything it
// needs to rank — it does not need the attempts' own worktrees. Its verdict
// is picked up by scheduler.applyJudgeVerdict on its own completion.
func (s *Server) judgeTask(w http.ResponseWriter, r *http.Request) {
	task, ok := s.taskParam(w, r)
	if !ok {
		return
	}
	attempts, err := s.DB.TaskAttempts(task.ID)
	if err != nil {
		respondErr(w, err)
		return
	}
	if len(attempts) < 2 {
		httpError(w, 409, "judging needs at least two attempts to compare")
		return
	}
	agent := firstNonEmptyStr(s.DB.Setting("judge_agent"), "claude")
	model := firstNonEmptyStr(s.DB.Setting("judge_model"), "haiku")
	if !s.knownAgent(agent) {
		httpError(w, 400, "judge_agent %q is not a known agent — fix it in Settings", agent)
		return
	}
	spec, exists := s.taskAgent(agent)
	if !exists {
		httpError(w, 400, "judge_agent %q has no non-interactive task definition", agent)
		return
	}
	if err := taskPermissionError(spec, "plan"); err != nil {
		httpError(w, 400, "judge agent %s cannot run read-only: %s", agent, err)
		return
	}
	prompt := s.buildJudgePrompt(task, attempts)
	jtask, err := s.DB.InsertTask(&store.Task{
		ProjectID: task.ProjectID, Title: clip("Judge: "+task.Title, 90),
		Prompt: prompt, Status: "queued", Priority: task.Priority,
		Agent: agent, Model: model, PermissionMode: "plan",
		CreatedBy: "judge", ParentTaskID: &task.ID,
	})
	if err != nil {
		respondErr(w, err)
		return
	}
	if _, err := s.Sched.CreateAttempt(jtask, scheduler.AttemptOpts{}); err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "task", jtask)
	writeJSON(w, 201, s.view(jtask))
}

// buildJudgePrompt hands the judge everything a human comparing the Compare
// view would see — per-attempt agent/model, the auto-verify result and a
// clipped diff — and asks for exactly the one-line answer
// scheduler.applyJudgeVerdict's regex expects.
func (s *Server) buildJudgePrompt(task *store.Task, attempts []*store.Attempt) string {
	var b strings.Builder
	b.WriteString("You are judging which of several independent attempts at the same task is best.\n\n")
	b.WriteString("TASK: " + task.Title + "\n" + task.Prompt + "\n\n")
	for _, a := range attempts {
		agent := firstNonEmptyStr(a.Agent, task.Agent)
		model := firstNonEmptyStr(a.Model, task.Model, "default")
		verify := store.UnjObj(a.VerifyJSON)
		diffStat := store.UnjList(a.DiffStatJSON)
		patch := ""
		if raw, err := os.ReadFile(filepath.Join(s.Cfg.DiffDir(),
			fmt.Sprintf("attempt-%d.patch", a.ID))); err == nil {
			patch = clip(string(raw), 6000)
		}
		fmt.Fprintf(&b, "=== ATTEMPT %d (agent=%s model=%s status=%s) ===\n", a.N, agent, model, a.Status)
		fmt.Fprintf(&b, "check result: %v\n", verify)
		fmt.Fprintf(&b, "files changed: %d\n", len(diffStat))
		b.WriteString("diff excerpt:\n```diff\n" + patch + "\n```\n\n")
	}
	b.WriteString("Rank the attempts and pick the single best one. Your FINAL line must be " +
		"exactly:\nJUDGE: attempt <n>\nfollowed by a short REASON on the next line.")
	return b.String()
}
