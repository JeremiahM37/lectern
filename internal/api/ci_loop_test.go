package api_test

// The CI loop's API seams (docs/ci-loop.md): the commit button arms a watch
// only on an opted-in project, the card view carries it, and the scheduler
// drives it to a verdict. The mock executor answers `gh pr view`/`gh pr
// checks` with an open PR whose one check passed; the fail→fix→pass, cap,
// closed and no-auth paths run against a fake gh in internal/ciloop.

import (
	"fmt"
	"testing"
)

func TestCommitPRArmsCILoopOnlyWhenProjectOptsIn(t *testing.T) {
	h := newHarness(t)

	plain := h.run(h.seededProjectID(), "No CI loop", "do it", nil)
	got := h.post(fmt.Sprintf("/api/tasks/%d/commit", plain.id()),
		obj{"message": "feat: x", "push": true, "pr": true}, 200)
	if got["ci"] != nil {
		t.Fatalf("a project that did not opt in must not be watched: %v", got["ci"])
	}

	p := h.project("ci-on", obj{"ci_loop": true, "ci_max_attempts": 2})
	if p.num("ci_loop") != 1 || p.num("ci_max_attempts") != 2 {
		t.Fatalf("project settings: %v", p)
	}
	task := h.run(p.id(), "Watch my CI", "do it", nil)
	got = h.post(fmt.Sprintf("/api/tasks/%d/commit", task.id()),
		obj{"message": "feat: y", "push": true, "pr": true}, 200)
	ci := got.sub("ci")
	if ci.str("pr_url") != "https://github.com/mock/repo/pull/7" || ci.num("max_attempts") != 2 ||
		ci.str("state") != "pending" {
		t.Fatalf("commit response ci: %v", ci)
	}
	h.waitUntil("the watched PR to pass", func() bool {
		return h.get(fmt.Sprintf("/api/tasks/%d", task.id())).sub("ci").str("state") == "passed"
	})
	view := h.get(fmt.Sprintf("/api/tasks/%d", task.id())).sub("ci")
	if view.str("label") != "CI passed" || view["active"] != false {
		t.Fatalf("card view after pass: %v", view)
	}
}

func TestProjectCIMaxAttemptsIsBounded(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	for _, bad := range []int{0, 11} {
		if code := h.status("PATCH", fmt.Sprintf("/api/projects/%d", pid), obj{"ci_max_attempts": bad}); code != 422 {
			t.Fatalf("ci_max_attempts=%d: %d", bad, code)
		}
	}
	got := h.patch(fmt.Sprintf("/api/projects/%d", pid), obj{"ci_loop": true, "ci_max_attempts": 5}, 200)
	if got.num("ci_loop") != 1 || got.num("ci_max_attempts") != 5 {
		t.Fatalf("patched project: %v", got)
	}
}

func TestManualCIWatchValidatesURL(t *testing.T) {
	h := newHarness(t)
	task := h.run(h.seededProjectID(), "Agent opened its own PR", "do it", nil)
	path := fmt.Sprintf("/api/tasks/%d/ci", task.id())
	if code := h.status("POST", path, obj{"pr_url": "https://github.com/a/b/issues/3"}); code != 422 {
		t.Fatalf("an issue URL must be refused: %d", code)
	}
	got := h.post(path, obj{"pr_url": "https://github.com/a/b/pull/3"}, 200)
	if got.str("state") != "pending" || got.num("max_attempts") != 3 {
		t.Fatalf("manual watch: %v", got)
	}
}
