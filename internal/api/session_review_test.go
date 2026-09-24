package api_test

// Review and merge for sessions: live diff, commit/push/PR, PR description
// generation and inline review comments (docs/agent-events.md section 4).
//
// The mock executor doesn't speak the worktree control script (the real
// interactive worktree lifecycle needs real git/python3/tmux — see
// session_review_real_test.go for that, in the isolated runner), and its
// default reply to an unmatched command ("", rc 0) makes every directory look
// like a non-git one. So what's worth proving here is the shape and safety
// behavior that doesn't depend on a real repository: 404/409 handling, that
// review comments and PR descriptions don't need git at all, and that the
// task side (which captures its diff eagerly, onto disk, well before review)
// works end to end under the mock.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

func TestSessionDiff404ForUnknownSession(t *testing.T) {
	h := newHarness(t)
	if code := h.status("GET", "/api/sessions/999999/diff", nil); code != 404 {
		t.Fatalf("unknown session diff: %d", code)
	}
}

// A session with no isolated worktree (running straight in a project's own
// checkout) still resolves and hits git — the mock executor has no git
// binary at all, so IsGitRepo's probe fails the same way it would for a
// plain, uninitialised directory, and the endpoint must say so clearly
// rather than 500.
func TestSessionDiffOnNonGitWorkdirIs409(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID(), "agent": "claude"})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	if code := h.status("GET", fmt.Sprintf("/api/sessions/%d/diff", sess.id()), nil); code != 409 {
		t.Fatalf("diff on a non-git workdir: %d", code)
	}
}

func TestCommitSessionOnNonGitWorkdirIs409(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID(), "agent": "claude"})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	if code := h.status("POST", fmt.Sprintf("/api/sessions/%d/commit", sess.id()), obj{"message": "x"}); code != 409 {
		t.Fatalf("commit on a non-git workdir: %d", code)
	}
}

func TestSessionCommitRefusesOnEndedSession(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID(), "agent": "claude"})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	if code := h.status("DELETE", fmt.Sprintf("/api/sessions/%d?kill=true", sess.id()), nil); code != 200 {
		t.Fatalf("ending the session: %d", code)
	}
	if code := h.status("POST", fmt.Sprintf("/api/sessions/%d/commit", sess.id()), obj{"message": "x"}); code != 409 {
		t.Fatalf("commit on an ended session: %d", code)
	}
}

// The task side captures its diff onto disk right when the attempt finishes
// (scheduler.captureAndFinalize), so PR description generation there needs no
// live git at all and is fully exercisable under the mock.
func TestTaskPRDescriptionUsesTheCapturedDiffAndStubbedSummary(t *testing.T) {
	h := newHarness(t)
	task := h.run(h.seededProjectID(), "Add a health endpoint", "do it", nil)

	var seenPrompt string
	h.App.Server.SummaryGen = func(ctx context.Context, ex executor.Executor, agent, model, prompt string) (string, error) {
		seenPrompt = prompt
		return `{"title":"Add health endpoint","body":"- adds GET /health"}`, nil
	}
	t.Cleanup(func() { h.App.Server.SummaryGen = nil })

	out := h.post(fmt.Sprintf("/api/tasks/%d/pr-description", task.id()), nil, 200)
	if out.str("title") != "Add health endpoint" {
		t.Fatalf("pr-description: %v", out)
	}
	if !strings.Contains(out.str("body"), "adds GET /health") {
		t.Errorf("body: %v", out)
	}
	if !strings.Contains(seenPrompt, "app.py") { // MockDiff touches app.py
		t.Errorf("the diff never reached the summary call:\n%s", seenPrompt)
	}
}

func TestTaskPRDescriptionDefaultAgentAndModelAreClaudeAndHaiku(t *testing.T) {
	h := newHarness(t)
	task := h.run(h.seededProjectID(), "thing", "do it", nil)
	var gotAgent, gotModel string
	h.App.Server.SummaryGen = func(ctx context.Context, ex executor.Executor, agent, model, prompt string) (string, error) {
		gotAgent, gotModel = agent, model
		return `{"title":"t","body":"b"}`, nil
	}
	t.Cleanup(func() { h.App.Server.SummaryGen = nil })
	h.post(fmt.Sprintf("/api/tasks/%d/pr-description", task.id()), nil, 200)
	if gotAgent != "claude" || gotModel != "haiku" {
		t.Fatalf("default summary agent/model: %q/%q", gotAgent, gotModel)
	}

	// the setting, once configured, must reach the same call
	h.decode("PUT", "/api/settings", obj{"summary_agent": "codex", "summary_model": "o1-mini"}, 200, nil)
	h.post(fmt.Sprintf("/api/tasks/%d/pr-description", task.id()), nil, 200)
	if gotAgent != "codex" || gotModel != "o1-mini" {
		t.Fatalf("configured summary agent/model: %q/%q", gotAgent, gotModel)
	}
}

// A review is sent to a task as request-changes feedback through the exact
// same path a plain follow-up uses (followupWithFeedback) — the only new
// thing is how the feedback text is assembled.
func TestReviewTaskFormatsCommentsAsFollowupFeedback(t *testing.T) {
	h := newHarness(t)
	task := h.run(h.seededProjectID(), "Review me", "do it", nil)

	resp := h.post(fmt.Sprintf("/api/tasks/%d/review", task.id()), obj{
		"comments": []obj{
			{"file": "app.py", "line": 3, "side": "new", "text": "guard against None", "code": "def health():"},
		},
		"summary": "close, one thing",
	}, 200)
	if resp.str("status") != "queued" {
		t.Fatalf("review: %v", resp)
	}
	second := h.waitStatus(task.id(), "review").sub("attempt")
	if second.num("n") != 2 {
		t.Fatalf("expected a second attempt from the review: %v", second)
	}
	prompt := h.readPromptFile(second.str("worktree_path"))
	for _, want := range []string{"app.py:3", "guard against None", "close, one thing", "def health()"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("attempt prompt missing %q:\n%s", want, prompt)
		}
	}
}

func TestReviewTaskRejectsEmptyReview(t *testing.T) {
	h := newHarness(t)
	task := h.run(h.seededProjectID(), "thing", "do it", nil)
	if code := h.status("POST", fmt.Sprintf("/api/tasks/%d/review", task.id()), obj{}); code != 422 {
		t.Fatalf("empty review: %d", code)
	}
}

func TestReviewTaskOnlyFromReview(t *testing.T) {
	h := newHarness(t)
	task := h.task(h.seededProjectID(), "thing", "do it", nil) // still queued, never dispatched
	code := h.status("POST", fmt.Sprintf("/api/tasks/%d/review", task.id()), obj{"summary": "x"})
	if code != 409 {
		t.Fatalf("review on a queued task: %d", code)
	}
}

// A session review is delivered straight to the pane via SendText, so the
// mock's fake agent sees it exactly as it would see a typed message.
func TestReviewSessionSendsFormattedPromptToThePane(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID(), "agent": "claude"})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")

	got := h.post(fmt.Sprintf("/api/sessions/%d/review", sess.id()), obj{
		"comments": []obj{{"file": "main.go", "line": 42, "side": "new", "text": "unused import"}},
	}, 200)
	if got.num("comments") != 1 {
		t.Fatalf("review response: %v", got)
	}
	// The mock pane only renders the first line of a pasted message (like a
	// real terminal showing what's visible), so prove the FULL formatted
	// prompt was staged for the pane by reading it straight out of the
	// staged file — the same thing `tmux paste-buffer` would have pasted.
	sent := h.sendTextStagedFor(sess.id())
	for _, want := range []string{"Code review feedback (1 comment):", "1. main.go:42 (new side)", "unused import"} {
		if !strings.Contains(sent, want) {
			t.Errorf("staged review text missing %q:\n%s", want, sent)
		}
	}
	h.waitUntil("the review's first line to appear on the pane", func() bool {
		return strings.Contains(h.sessionByID(sess.id()).str("pane_tail"), "Code review feedback")
	})
}

// sendTextStagedFor finds the most recent SendText staging file for a session
// — SendTextCommand writes the full message to /tmp/lectern-send-<id>-<nanos>
// before pasting it into the pane — and returns its content.
func (h *harness) sendTextStagedFor(sessionID int64) string {
	h.t.Helper()
	prefix := fmt.Sprintf("/tmp/lectern-send-%d-", sessionID)
	var latestKey string
	for k := range h.mock().Files() {
		if strings.HasPrefix(k, prefix) && k > latestKey {
			latestKey = k
		}
	}
	if latestKey == "" {
		h.t.Fatalf("no staged SendText file found for session %d", sessionID)
	}
	return string(h.mock().Files()[latestKey])
}

func TestReviewSessionRejectsEmptyReview(t *testing.T) {
	h := newHarness(t)
	sess := h.session(obj{"project_id": h.seededProjectID(), "agent": "claude"})
	h.waitSessionStatus(sess.id(), "waiting", "idle", "running")
	if code := h.status("POST", fmt.Sprintf("/api/sessions/%d/review", sess.id()), obj{}); code != 422 {
		t.Fatalf("empty review: %d", code)
	}
}

// readPromptFile reads the prompt an attempt staged for its agent, straight
// out of the mock's in-memory filesystem — the same file `cat .lectern/
// prompt.md` reads on a real target (internal/scheduler/stage.go).
func (h *harness) readPromptFile(worktreePath string) string {
	h.t.Helper()
	raw, err := h.mock().ReadFile(context.Background(), worktreePath+"/.lectern/prompt.md", 0)
	if err != nil {
		h.t.Fatal(err)
	}
	return string(raw)
}
