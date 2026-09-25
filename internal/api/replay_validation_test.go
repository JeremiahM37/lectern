package api_test

// Request validation and the empty-result contract for the replay import
// endpoints, against the mock target — the mock's `gh pr list`/`git log
// --merges` commands have no scripted response (see internal/executor/mock.go),
// so they fall through to its generic "unhandled command" case (rc 0, empty
// stdout), which makes fetchReplayPRs fall back to the git-log path and then
// find zero merge records. That is a real, useful contract to pin down on
// its own: the pipeline must degrade to "0 candidates", not error out, when
// a target simply has no PR/merge history yet. The pipeline's actual
// candidate-building logic (accept/skip/prompt/check_command) is covered
// against real git history in replay_real_test.go and, without any
// target at all, in internal/replay's own pure tests.

import (
	"fmt"
	"testing"
)

func TestReplayPreviewValidatesProjectAndReportsEmptyResult(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()

	if code := h.status("POST", "/api/evals/replay/preview", obj{"project_id": 999999}); code != 400 {
		t.Fatalf("unknown project should 400, got %d", code)
	}

	resp := h.post("/api/evals/replay/preview", obj{"project_id": pid, "n": 5}, 200)
	if resp.str("source") != "git-log" {
		t.Fatalf("mock target has no gh/PR data — expected the git-log fallback, got %v", resp["source"])
	}
	if len(resp.list("candidates")) != 0 {
		t.Fatalf("mock target has no merge history — expected 0 candidates, got %v", resp["candidates"])
	}
}

func TestReplayCreateSuiteRequiresNameAndRefusesWhenNothingSurvives(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()

	if code := h.status("POST", "/api/evals/replay/suites", obj{"project_id": pid}); code != 422 {
		t.Fatalf("missing name should 422, got %d", code)
	}
	if code := h.status("POST", "/api/evals/replay/suites",
		obj{"project_id": pid, "name": "x"}); code != 409 {
		t.Fatalf("no candidate PRs should 409, got %d", code)
	}

	// no suite should have been left behind by the refused attempt
	suites := h.getList(fmt.Sprintf("/api/evals/suites?project_id=%d", pid))
	if len(suites) != 0 {
		t.Fatalf("a refused create should not persist a suite, got %v", suites)
	}
}

func TestReplayPreviewClampsNToTheAllowedRange(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	// n<=0 and n>100 are both normalized rather than rejected — the handler
	// should still answer 200 either way.
	if code := h.status("POST", "/api/evals/replay/preview", obj{"project_id": pid, "n": 0}); code != 200 {
		t.Fatalf("n=0 should be normalized to the default, got %d", code)
	}
	if code := h.status("POST", "/api/evals/replay/preview", obj{"project_id": pid, "n": 99999}); code != 200 {
		t.Fatalf("an oversized n should be clamped, not refused, got %d", code)
	}
}
