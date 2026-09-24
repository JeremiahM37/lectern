package api

import "github.com/JeremiahM37/lectern/v2/internal/store"

// attemptView is the latest attempt as the board renders it.
type attemptView struct {
	ID           int64          `json:"id"`
	N            int            `json:"n"`
	Status       string         `json:"status"`
	Branch       string         `json:"branch"`
	WorktreePath string         `json:"worktree_path"`
	TmuxSession  string         `json:"tmux_session"`
	StartedAt    *float64       `json:"started_at"`
	FinishedAt   *float64       `json:"finished_at"`
	ExitCode     *int           `json:"exit_code"`
	Result       map[string]any `json:"result"`
	DiffStat     []any          `json:"diff_stat"`
	Verify       map[string]any `json:"verify"`
	// Driver names the internal/drivers.Kind running this attempt (e.g.
	// "claude-steer"), so the UI can show the steer input only where it will
	// actually work instead of guessing from agent + permission_mode.
	Driver string `json:"driver"`
}

// attemptSummary is one chip in the task sheet's attempt row — and, once a
// task has more than one attempt, one card in the Best-of-N compare view.
type attemptSummary struct {
	ID       int64  `json:"id"`
	N        int    `json:"n"`
	Status   string `json:"status"`
	Model    string `json:"model"`
	ExitCode *int   `json:"exit_code"`
	CostUSD  any    `json:"cost_usd"`
	// Agent/PermissionMode are the variant's OWN choice when it set one
	// (store.Attempt.Agent/PermissionMode), falling back to the task's so a
	// pre-variants task still reports a sensible value.
	Agent          string         `json:"agent"`
	PermissionMode string         `json:"permission_mode"`
	Driver         string         `json:"driver"`
	StartedAt      *float64       `json:"started_at"`
	FinishedAt     *float64       `json:"finished_at"`
	DiffStat       []any          `json:"diff_stat"`
	Verify         map[string]any `json:"verify"`
	InputTokens    any            `json:"input_tokens"`
	OutputTokens   any            `json:"output_tokens"`
}

// taskView is a task plus everything the UI needs to render its card without a
// second request.
type taskView struct {
	*store.Task
	Takeover    *store.Takeover  `json:"takeover,omitempty"`
	Labels      []string         `json:"labels"`
	ProjectName string           `json:"project_name"`
	TargetName  string           `json:"target_name"`
	TargetHost  string           `json:"target_host"`
	TargetUser  string           `json:"target_user"`
	TargetKind  string           `json:"target_kind"`
	Attempt     *attemptView     `json:"attempt,omitempty"`
	Attempts    []attemptSummary `json:"attempts"`
}

// view assembles a task's full board representation.
func (s *Server) view(task *store.Task) *taskView {
	out := &taskView{Task: task, Labels: []string{}, ProjectName: "?", TargetName: "?"}
	out.Takeover, _ = s.DB.Takeover(task.ID)
	if labels := store.UnjStrings(task.LabelsJSON); labels != nil {
		out.Labels = labels
	}
	if proj, err := s.DB.Project(task.ProjectID); err == nil {
		out.ProjectName = proj.Name
		if tgt, err := s.DB.Target(proj.TargetID); err == nil {
			out.TargetName = tgt.Name
			out.TargetHost = tgt.Host
			out.TargetUser = tgt.User
			out.TargetKind = tgt.Kind
		}
	}
	if att, err := s.DB.LatestAttempt(task.ID); err == nil {
		out.Attempt = &attemptView{
			ID: att.ID, N: att.N, Status: att.Status, Branch: att.Branch,
			WorktreePath: att.WorktreePath, TmuxSession: att.TmuxSession,
			StartedAt: att.StartedAt, FinishedAt: att.FinishedAt, ExitCode: att.ExitCode,
			Result: store.UnjObj(att.ResultJSON),
			// diff_stat_json defaults to '{}' before a diff is captured, which
			// parses as an object — coercing to a list is what keeps a running
			// card renderable instead of blanking the whole column
			DiffStat: store.UnjList(att.DiffStatJSON),
			Verify:   store.UnjObj(att.VerifyJSON),
			Driver:   att.Driver,
		}
	}
	out.Attempts = []attemptSummary{}
	if list, err := s.DB.TaskAttempts(task.ID); err == nil {
		for _, a := range list {
			res := store.UnjObj(a.ResultJSON)
			out.Attempts = append(out.Attempts, attemptSummary{
				ID: a.ID, N: a.N, Status: a.Status, Model: a.Model, ExitCode: a.ExitCode,
				CostUSD:        res["cost_usd"],
				InputTokens:    res["input_tokens"],
				OutputTokens:   res["output_tokens"],
				Agent:          firstNonEmptyStr(a.Agent, task.Agent),
				PermissionMode: firstNonEmptyStr(a.PermissionMode, task.PermissionMode),
				Driver:         a.Driver,
				StartedAt:      a.StartedAt,
				FinishedAt:     a.FinishedAt,
				DiffStat:       store.UnjList(a.DiffStatJSON),
				Verify:         store.UnjObj(a.VerifyJSON),
			})
		}
	}
	return out
}
