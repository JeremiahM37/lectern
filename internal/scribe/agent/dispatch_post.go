package agent

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// The post-dispatch passes (agentFeatures.desloppify and verifyEdits). Both
// are plain subagents built by the same factory as the dispatch's own tasks,
// so the app wiring decides which model runs them. A pass that cannot start
// reports why instead of failing the dispatch.

// desloppifyTask is the cleanup instruction for the files code tasks edited.
const desloppifyTask = `Clean up the code these edits left behind. Edited files: %s

Remove: framework-behaviour tests, redundant type assertions, error handling for states that cannot happen, debug logging and dead code.
Keep: TODO markers, business logic and non-obvious comments.

Edit only the files listed. Read around each section before you change it. Then reply in under 200 words: what you removed and what you kept.`

// reviewTask is the instruction for the read-only reviewer.
const reviewTask = `Review the edits that code agents just made. Edited files: %s

Project check result:
%s

Read the changed files. Look for correctness bugs, broken call sites and problems the check would not catch. Do not edit files.

End with one line, exactly "VERDICT: PASS" or "VERDICT: FAIL", then the reasons in under 150 words.`

// lintFixTimeout bounds the deterministic lint fix after code tasks edit files
// (spec 06 section 9.5: 30 seconds).
const lintFixTimeout = 30 * time.Second

// lintFixer is the file-scoped lint fix the project tool offers. It is an
// optional interface on the tool: a registry without it skips the pass.
type lintFixer interface {
	LintFixFiles(ctx context.Context, env *tools.Env, files []string, timeout time.Duration) (string, bool)
}

// lintFix runs the project's lint auto-fix on the files code tasks changed.
// It runs once per dispatch, before the cleanup and the check, so both see the
// fixed files. It is deterministic and returns the report, or "" when the
// registry has no project tool that can scope a fix.
func lintFix(ctx context.Context, env *tools.Env, reg *tools.Registry, changed []string) string {
	if reg == nil || len(changed) == 0 {
		return ""
	}
	t, ok := reg.Get("project")
	if !ok {
		return ""
	}
	fx, ok := t.(lintFixer)
	if !ok {
		return ""
	}
	e := *env
	e.CallID = env.CallID + "/lintfix"
	report, _ := fx.LintFixFiles(ctx, &e, changed, lintFixTimeout)
	return report
}

// sideTask runs one post-dispatch subagent and counts its spend toward the
// dispatch. It does not run once the circuit breaker has opened.
func (r *dispatchRun) sideTask(ctx context.Context, role string, purpose Purpose, label, task string) attempt {
	if r.tripped() {
		return attempt{err: errBreakerOpen}
	}
	idx := r.board.add(role, label)
	res := r.attempt(ctx, idx, role, purpose, task)
	r.mu.Lock()
	r.total.Add(res.usage)
	r.cost += res.cost
	r.mu.Unlock()
	return res
}

// desloppify runs the cleanup pass over the edited files. It returns the
// report for the Cleanup section and the files the pass changed.
func (r *dispatchRun) desloppify(ctx context.Context, changed []string) (string, []string) {
	res := r.sideTask(ctx, "code", PurposeCleanup, "cleanup of edited files",
		fmt.Sprintf(desloppifyTask, strings.Join(changed, ", ")))
	if res.err != nil {
		return "not run: " + res.err.Error(), nil
	}
	report := strings.TrimSpace(res.report)
	if report == "" {
		report = "the cleanup agent reported nothing"
	}
	return report, res.edited
}

// review runs the read-only reviewer over the changed files and the project
// check. It returns the report and a verdict: "pass", "fail" or "unknown".
func (r *dispatchRun) review(ctx context.Context, changed []string, check string) (string, string) {
	task := fmt.Sprintf(reviewTask, strings.Join(changed, ", "), check)
	res := r.sideTask(ctx, "explore", PurposeReview, "review of the changed files", task)
	if res.err != nil {
		return "not run: " + res.err.Error(), "unknown"
	}
	report := strings.TrimSpace(res.report)
	return report, verdictOf(report)
}

// verdictOf reads the last "VERDICT:" line of a reviewer's reply.
func verdictOf(report string) string {
	lines := strings.Split(report, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.ToUpper(strings.Trim(strings.TrimSpace(lines[i]), "*_` "))
		v, ok := strings.CutPrefix(line, "VERDICT:")
		if !ok {
			continue
		}
		switch strings.TrimSpace(strings.Trim(v, "*` ")) {
		case "PASS":
			return "pass"
		case "FAIL":
			return "fail"
		}
		return "unknown"
	}
	return "unknown"
}
