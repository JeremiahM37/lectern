package worktree

import (
	"context"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// StepError is a git/gh step that ran and failed on its own terms (bad
// commit, a real merge conflict) as opposed to the executor itself failing —
// the former is the caller's to fix and maps to 409, the latter is ours and
// maps through respondErr like any other infrastructure error.
type StepError struct {
	Status int
	Msg    string
}

func (e *StepError) Error() string { return e.Msg }

// CommitPushPR runs `git add -A && git commit`, an optional push and an
// optional `gh pr create`, in dir on branch. It is the one place the task and
// session Commit buttons and the CI loop's own fix commits touch git, so a fix
// to one path fixes all of them. A push or PR step that runs but fails is
// recorded in steps and does not abort, except a failed push always cancels a
// requested PR (never open a PR for a branch that didn't reach origin).
func CommitPushPR(ctx context.Context, ex executor.Executor, dir, branch, message string,
	push, pr bool, prTitle, prBody string) ([]map[string]any, error) {
	q := executor.ShellQuote
	steps := []map[string]any{}

	res, err := ex.Run(ctx, "git add -A && git commit -m "+q(message), executor.RunOpts{Cwd: dir, Timeout: 60})
	if err != nil {
		return steps, err
	}
	output := clipEnd(res.Stdout+res.Stderr, 800)
	steps = append(steps, map[string]any{"step": "commit", "rc": res.RC, "output": output})
	if !res.OK() {
		detail := output
		if strings.Contains(res.Stdout+res.Stderr, "nothing to commit") {
			detail = "nothing to commit"
		}
		return steps, &StepError{409, "commit failed: " + detail}
	}

	if push {
		step, err := Push(ctx, ex, dir, branch)
		if err != nil {
			return steps, err
		}
		steps = append(steps, step)
		if rc, _ := step["rc"].(int); pr && rc != 0 {
			pr = false // never open a PR for a branch that failed to push
		}
	}

	if pr {
		if prTitle == "" {
			prTitle = message
		}
		cmd := fmt.Sprintf("gh pr create --head %s --title %s --body %s",
			q(branch), q(prTitle), q(prBody))
		res, err := ex.Run(ctx, cmd, executor.RunOpts{Cwd: dir, Timeout: 120})
		if err != nil {
			return steps, err
		}
		url := ""
		if res.OK() {
			lines := strings.Split(strings.TrimSpace(res.Stdout), "\n")
			url = strings.TrimSpace(lines[len(lines)-1])
		}
		steps = append(steps, map[string]any{"step": "pr", "rc": res.RC,
			"output": clipEnd(res.Stdout+res.Stderr, 800), "url": url})
	}
	return steps, nil
}

// Push runs `git push -u origin <branch>` in dir and reports it as a step.
// A push that runs and fails is a step with a non-zero "rc", not an error.
func Push(ctx context.Context, ex executor.Executor, dir, branch string) (map[string]any, error) {
	res, err := ex.Run(ctx, "git push -u origin "+executor.ShellQuote(branch), executor.RunOpts{Cwd: dir, Timeout: 120})
	if err != nil {
		return nil, err
	}
	return map[string]any{"step": "push", "rc": res.RC, "output": clipEnd(res.Stdout+res.Stderr, 800)}, nil
}

func clipEnd(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
