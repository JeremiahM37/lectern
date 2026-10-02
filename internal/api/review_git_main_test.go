package api_test

// Committing from a session that works straight on the default branch — every
// session started without a worktree. Before, Commit was simply refused; now
// the person picks: a new branch named from the session, or main itself after
// saying so (docs/design/simple-ui.md "Commit when a session is on the
// default branch"). Real git through the real local executor.

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestRealGitCommitOnMainOffersABranchOrAnExplicitMain(t *testing.T) {
	h, proj, repo := newRealSessionHarness(t)
	gitIn(t, repo, "config", "user.name", "Test")
	gitIn(t, repo, "config", "user.email", "t@example.invalid")
	var row obj
	h.decode("POST", "/api/sessions", obj{"project_id": proj.ID, "agent": "review-real-agent", "yolo": false, "name": "work · main"}, 201, &row)
	base := fmt.Sprintf("/api/sessions/%d/git", int64(row.num("id")))

	status := h.get(base)
	if status["on_base_branch"] != true || status["has_remote"] != false || status["own_checkout"] != true || status.str("dir") != repo {
		t.Fatalf("a session on main in the person's own folder, no remote: %v", status)
	}

	// Saying neither is refused with a code the web app recognises.
	writeReviewFile(t, filepath.Join(repo, "app.py"), "def main():\n    print('one')\n")
	// The message starts from what changed, never the session's name.
	if got := h.get(base).str("suggested_message"); got != "Update app.py" {
		t.Fatalf("suggested message: %q", got)
	}
	var refused obj
	h.decode("POST", base+"/commit", obj{"message": "one", "stage_all": true}, 409, &refused)
	if refused.str("code") != "on_base_branch" || refused.str("branch") != "main" {
		t.Fatalf("refusal: %v", refused)
	}

	// A new branch, named from the session, then the commit lands there.
	out := h.post(base+"/commit", obj{"message": "one", "stage_all": true, "new_branch": "lectern/fix-login"}, 200)
	if out.str("branch") != "lectern/fix-login" {
		t.Fatalf("commit branch: %v", out)
	}
	if got, _ := gitCmd(repo, "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(got) != "lectern/fix-login" {
		t.Fatalf("HEAD after new branch: %q", got)
	}
	if got, _ := gitCmd(repo, "log", "-1", "--format=%s", "main"); strings.TrimSpace(got) != "base" {
		t.Fatalf("main must not move: %q", got)
	}
	if h.get(base)["on_base_branch"] != false {
		t.Fatal("the session is on its own branch now")
	}
	// Its name said which branch it was on; it follows the folder.
	if got := h.sessionByID(int64(row.num("id"))).str("name"); got != "work · lectern/fix-login" {
		t.Fatalf("name follows the branch: %q", got)
	}

	// A taken name gets a suffix rather than failing.
	gitIn(t, repo, "switch", "-q", "main")
	writeReviewFile(t, filepath.Join(repo, "app.py"), "def main():\n    print('two')\n")
	out = h.post(base+"/commit", obj{"message": "two", "stage_all": true, "new_branch": "lectern/fix-login"}, 200)
	if out.str("branch") != "lectern/fix-login-2" {
		t.Fatalf("suffixed branch: %v", out)
	}
	gitIn(t, repo, "switch", "-q", "main")
	if code := h.status("POST", base+"/commit", obj{"message": "x", "stage_all": true, "new_branch": "bad..name"}); code != 409 {
		t.Fatalf("invalid branch name: %d", code)
	}

	// Main itself, explicitly (auth mode none: the operator is the person).
	writeReviewFile(t, filepath.Join(repo, "app.py"), "def main():\n    print('three')\n")
	out = h.post(base+"/commit", obj{"message": "three", "stage_all": true, "allow_base_branch": true}, 200)
	if out.str("branch") != "main" || out["failed"] != nil {
		t.Fatalf("commit to main: %v", out)
	}
	if got, _ := gitCmd(repo, "log", "-1", "--format=%s", "main"); strings.TrimSpace(got) != "three" {
		t.Fatalf("main's last commit: %q", got)
	}

	gitIn(t, repo, "remote", "add", "origin", repo)
	if h.get(base)["has_remote"] != true {
		t.Fatal("has_remote once there is one")
	}
}
