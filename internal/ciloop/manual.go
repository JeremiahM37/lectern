package ciloop

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// "Fix checks with agent" (the pull request page, docs/trackers.md) is the
// loop started by a person instead of by a PR Lectern opened: a new session
// is launched with the first fix request as its opening prompt, and the
// watch is armed as if that request had already been sent, so the loop
// takes over from the agent's first push.

// ErrChecksNotFailing is FixReport's answer for a PR with nothing to fix.
var ErrChecksNotFailing = errors.New("no checks are failing on this pull request")

// ErrAlreadyWatched is ArmAsked's answer when the loop is already working on
// the PR for someone else.
var ErrAlreadyWatched = errors.New("the CI loop is already working on this pull request")

// FixReport reads the PR and its checks through the target's gh and builds
// fix request 1 of maxAttempts, the same report the loop itself sends.
func (w *Watcher) FixReport(ctx context.Context, ex executor.Executor, prURL, branch string, maxAttempts int) (report, headSHA string, err error) {
	prURL = strings.TrimRight(strings.TrimSpace(prURL), "/")
	if !ValidPRURL(prURL) {
		return "", "", fmt.Errorf("not a pull request URL: %q", prURL)
	}
	info, err := viewPR(ctx, ex, prURL)
	if err != nil {
		return "", "", err
	}
	if info.State != "OPEN" {
		return "", "", fmt.Errorf("the pull request is %s", strings.ToLower(info.State))
	}
	checks, err := listChecks(ctx, ex, prURL)
	if err != nil {
		return "", "", err
	}
	var failing []check
	for _, c := range checks {
		if c.failed() {
			failing = append(failing, c)
		}
	}
	if len(failing) == 0 {
		return "", info.HeadSHA, ErrChecksNotFailing
	}
	if maxAttempts <= 0 {
		maxAttempts = store.DefaultCIMaxAttempts
	}
	cw := &store.CIWatch{PRURL: prURL, Branch: branch, MaxAttempts: maxAttempts}
	return w.report(ctx, ex, cw, info.HeadSHA, failing, 1, false), info.HeadSHA, nil
}

// ArmAsked starts watching prURL for o as though fix request 1 had already
// been delivered for headSHA: the loop waits for a push, then carries on.
// A PR the loop is already actively watching is left alone.
func (w *Watcher) ArmAsked(o Owner, prURL, headSHA string) (*store.CIWatch, error) {
	prURL = strings.TrimRight(strings.TrimSpace(prURL), "/")
	if cur, err := w.DB.CIWatchByURL(prURL); err == nil && (cur.State == StatePending || cur.State == StateFailing) {
		return cur, ErrAlreadyWatched
	}
	row, err := w.Arm(o, prURL, true)
	if err != nil {
		return nil, err
	}
	now := w.now()
	fresh := w.update(row, map[string]any{
		"state": StateFailing, "attempts": 1, "asked_sha": headSHA, "head_sha": headSHA,
		"interval_s": dur(w.MinInterval, DefaultMinInterval), "last_change_at": now,
		"next_poll_at": now + dur(w.MinInterval, DefaultMinInterval),
	}, true)
	if fresh == nil {
		return row, nil
	}
	return fresh, nil
}

// Watching reports the loop's active watch on prURL, if any.
func (w *Watcher) Watching(prURL string) *View {
	cur, err := w.DB.CIWatchByURL(strings.TrimRight(strings.TrimSpace(prURL), "/"))
	if err != nil {
		return nil
	}
	return ViewOf(cur)
}
