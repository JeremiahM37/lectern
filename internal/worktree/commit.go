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
	return Commit(ctx, ex, dir, branch, CommitOptions{Message: message, StageAll: true,
		Push: push, PR: pr, PRTitle: prTitle, PRBody: prBody})
}

// CommitOptions is the review workspace's commit: the index as staged or
// everything, optionally amending HEAD, then an optional push (plain or
// force-with-lease) and PR.
type CommitOptions struct {
	Message string
	// StageAll runs `git add -A` first; otherwise only what is staged is
	// committed.
	StageAll bool
	Amend    bool
	// AllowPushedAmend overrides the guard that refuses to amend a commit a
	// remote branch already contains.
	AllowPushedAmend bool
	Push             bool
	// ForceWithLease pushes with --force-with-lease. Lease, when set, is the
	// remote commit the caller last saw; the push fails if origin moved.
	ForceWithLease bool
	Lease          string
	PR             bool
	PRTitle        string
	PRBody         string
}

// PushedRefs lists the remote-tracking branches that already contain HEAD.
// A commit on any of them is published; amending it rewrites history someone
// else may have.
func PushedRefs(ctx context.Context, ex executor.Executor, dir string) ([]string, error) {
	res, err := ex.Run(ctx, "git branch -r --contains HEAD", executor.RunOpts{Cwd: dir, Timeout: 30})
	if err != nil {
		return nil, err
	}
	var refs []string
	if res.OK() {
		for _, line := range strings.Split(res.Stdout, "\n") {
			if ref := strings.TrimSpace(line); ref != "" && !strings.Contains(ref, " -> ") {
				refs = append(refs, ref)
			}
		}
	}
	return refs, nil
}

// ErrAmendPushed is the amend guard's refusal. Refs names the remote branches
// that already contain HEAD.
type ErrAmendPushed struct{ Refs []string }

func (e *ErrAmendPushed) Error() string {
	return "the last commit is already on " + strings.Join(e.Refs, ", ") +
		"; amending it rewrites published history and needs a force push"
}

// Commit is CommitPushPR with the review workspace's options. The commit step
// fails as a StepError (409) exactly like CommitPushPR's.
func Commit(ctx context.Context, ex executor.Executor, dir, branch string, o CommitOptions) ([]map[string]any, error) {
	q := executor.ShellQuote
	steps := []map[string]any{}

	if o.Amend && !o.AllowPushedAmend {
		refs, err := PushedRefs(ctx, ex, dir)
		if err != nil {
			return steps, err
		}
		if len(refs) > 0 {
			return steps, &ErrAmendPushed{Refs: refs}
		}
	}
	cmd := "git commit -m " + q(o.Message)
	if o.Amend {
		cmd += " --amend"
	}
	if o.StageAll {
		cmd = "git add -A && " + cmd
	}
	res, err := ex.Run(ctx, cmd, executor.RunOpts{Cwd: dir, Timeout: 120})
	if err != nil {
		return steps, err
	}
	output := clipEnd(res.Stdout+res.Stderr, 800)
	steps = append(steps, map[string]any{"step": "commit", "rc": res.RC, "output": output})
	if !res.OK() {
		detail := output
		if strings.Contains(res.Stdout+res.Stderr, "nothing to commit") ||
			strings.Contains(res.Stdout+res.Stderr, "no changes added to commit") {
			detail = "nothing to commit"
		}
		steps[0]["output"] = clipEnd(res.Stdout+res.Stderr, 12000)
		return steps, &StepError{409, "commit failed: " + detail}
	}

	push := o.Push || o.ForceWithLease
	pr := o.PR
	if push {
		var step map[string]any
		var err error
		if o.ForceWithLease {
			step, err = ForcePushWithLease(ctx, ex, dir, branch, o.Lease)
		} else {
			step, err = Push(ctx, ex, dir, branch)
		}
		if err != nil {
			return steps, err
		}
		steps = append(steps, step)
		if rc, _ := step["rc"].(int); pr && rc != 0 {
			pr = false // never open a PR for a branch that failed to push
		}
	}

	if pr {
		prTitle := o.PRTitle
		if prTitle == "" {
			prTitle = o.Message
		}
		cmd := fmt.Sprintf("gh pr create --head %s --title %s --body %s",
			q(branch), q(prTitle), q(o.PRBody))
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

// ForcePushWithLease pushes branch with --force-with-lease, pinned to lease
// (the remote commit the caller last saw) when one is given, so a push that
// would discard someone else's newer work fails instead.
func ForcePushWithLease(ctx context.Context, ex executor.Executor, dir, branch, lease string) (map[string]any, error) {
	q := executor.ShellQuote
	flag := "--force-with-lease=" + branch
	if lease != "" {
		flag += ":" + lease
	}
	res, err := ex.Run(ctx, "git push -u "+q(flag)+" origin "+q(branch), executor.RunOpts{Cwd: dir, Timeout: 120})
	if err != nil {
		return nil, err
	}
	return map[string]any{"step": "force-push", "rc": res.RC, "output": clipEnd(res.Stdout+res.Stderr, 800)}, nil
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
