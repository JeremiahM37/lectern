package api_test

// The review's commit and push are pinned to the state the person reviewed.
// Another agent can edit the same worktree between the diff load and the
// click; the server must refuse rather than commit or push a different change.
// Real git through the real local executor, like the other review tests.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommitAndPushRefuseWhenTheReviewedStateMoved(t *testing.T) {
	h, proj, repo := newRealSessionHarness(t)
	gitIn(t, repo, "config", "user.name", "Test")
	gitIn(t, repo, "config", "user.email", "t@example.invalid")
	gitIn(t, repo, "checkout", "-q", "-b", "feat")
	var row obj
	h.decode("POST", "/api/sessions", obj{"project_id": proj.ID, "agent": "review-real-agent", "yolo": false, "name": "work · feat"}, 201, &row)
	base := fmt.Sprintf("/api/sessions/%d", int64(row.num("id")))

	writeReviewFile(t, filepath.Join(repo, "app.py"), "def main():\n    print('one')\n")
	reviewed := h.get(base + "/diff")
	head, hash := reviewed.str("head"), reviewed.str("diff_hash")
	if head == "" || hash == "" {
		t.Fatalf("the diff must carry the state it shows: %v", reviewed)
	}

	// Another agent edits the worktree after the person loaded the diff.
	writeReviewFile(t, filepath.Join(repo, "app.py"), "def main():\n    print('two')\n")
	var refused obj
	h.decode("POST", base+"/git/commit", obj{"message": "one", "stage_all": true,
		"expected_head": head, "expected_diff_hash": hash}, 409, &refused)
	if refused.str("error") != "stale" || !strings.Contains(refused.str("detail"), "working tree changed") {
		t.Fatalf("refusal: %v", refused)
	}
	if got, _ := gitCmd(repo, "log", "-1", "--format=%s"); strings.TrimSpace(got) != "base" {
		t.Fatalf("a refused commit must not run, HEAD is %q", got)
	}

	// Reloading the diff pins what is there now, and the commit goes ahead.
	fresh := h.get(base + "/diff")
	h.post(base+"/git/commit", obj{"message": "two", "stage_all": true,
		"expected_head": fresh.str("head"), "expected_diff_hash": fresh.str("diff_hash")}, 200)

	// The branch has moved on since the first load, so that pin is refused as a
	// head move, before the push runs.
	var moved obj
	h.decode("POST", base+"/git/push", obj{"expected_head": head}, 409, &moved)
	if moved.str("error") != "stale" || !strings.HasPrefix(moved.str("detail"), "HEAD moved") {
		t.Fatalf("push refusal: %v", moved)
	}
}

func TestCommitWithoutAPinKeepsTheOldBehaviour(t *testing.T) {
	h, proj, repo := newRealSessionHarness(t)
	gitIn(t, repo, "config", "user.name", "Test")
	gitIn(t, repo, "config", "user.email", "t@example.invalid")
	gitIn(t, repo, "checkout", "-q", "-b", "feat")
	var row obj
	h.decode("POST", "/api/sessions", obj{"project_id": proj.ID, "agent": "review-real-agent", "yolo": false, "name": "work · feat"}, 201, &row)
	base := fmt.Sprintf("/api/sessions/%d", int64(row.num("id")))

	writeReviewFile(t, filepath.Join(repo, "app.py"), "def main():\n    print('one')\n")
	writeReviewFile(t, filepath.Join(repo, "app.py"), "def main():\n    print('two')\n")
	h.post(base+"/git/commit", obj{"message": "no pin", "stage_all": true}, 200)
}
