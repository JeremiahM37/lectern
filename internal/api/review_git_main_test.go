package api_test

// Committing from a session that works straight on the default branch — every
// session started without a worktree. Before, Commit was simply refused; now
// the person picks: a new branch named from the session, or main itself after
// saying so (docs/design/simple-ui.md "Commit when a session is on the
// default branch"). Real git through the real local executor.

import (
	"fmt"
	"os"
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

// A machine with no git name and email is told so before the folder moves to
// a new branch, not after the commit has already failed on it.
func TestCommitWithoutGitIdentityLeavesTheFolderAlone(t *testing.T) {
	h, proj, repo := newRealSessionHarness(t)
	// Local config beats any global identity; an empty name is what a fresh
	// account amounts to once git refuses to guess one.
	gitIn(t, repo, "config", "user.useConfigOnly", "true")
	gitIn(t, repo, "config", "user.name", "")
	gitIn(t, repo, "config", "user.email", "")
	var row obj
	h.decode("POST", "/api/sessions", obj{"project_id": proj.ID, "agent": "review-real-agent", "yolo": false, "name": "work · main"}, 201, &row)
	base := fmt.Sprintf("/api/sessions/%d/git", int64(row.num("id")))
	writeReviewFile(t, filepath.Join(repo, "app.py"), "def main():\n    print('one')\n")
	var refused obj
	h.decode("POST", base+"/commit", obj{"message": "one", "stage_all": true, "new_branch": "lectern/fix-login"}, 409, &refused)
	if refused.str("code") != "no_git_identity" {
		t.Fatalf("refusal: %v", refused)
	}
	if got, _ := gitCmd(repo, "rev-parse", "--abbrev-ref", "HEAD"); strings.TrimSpace(got) != "main" {
		t.Fatalf("the folder must stay on main: %q", got)
	}
	if got, _ := gitCmd(repo, "branch", "--list", "lectern/fix-login"); strings.TrimSpace(got) != "" {
		t.Fatalf("no branch may be created: %q", got)
	}
	// The refusal reads as a form's question, not a terminal instruction.
	if d := refused.str("detail"); d != "Git needs your name and email before it can commit." {
		t.Fatalf("detail: %q", d)
	}

	// The form's answers that are not a name and an email are refused, and
	// nothing is saved.
	var bad obj
	h.decode("POST", base+"/commit", obj{"message": "one", "stage_all": true, "new_branch": "lectern/fix-login",
		"identity": obj{"name": "Ada", "email": "not-an-email", "scope": "repo"}}, 422, &bad)
	if bad.str("code") != "invalid_git_identity" {
		t.Fatalf("invalid identity: %v", bad)
	}
	if got, _ := gitCmd(repo, "config", "--local", "user.email"); strings.TrimSpace(got) != "" {
		t.Fatalf("nothing may be saved for an invalid identity: %q", got)
	}

	// Answered for this repository only: saved there, then the commit goes
	// ahead on the new branch.
	out := h.post(base+"/commit", obj{"message": "one", "stage_all": true, "new_branch": "lectern/fix-login",
		"identity": obj{"name": "Ada Lovelace", "email": "ada@example.invalid", "scope": "repo"}}, 200)
	if out.str("branch") != "lectern/fix-login" || out["failed"] != nil {
		t.Fatalf("commit after saving the identity: %v", out)
	}
	if got, _ := gitCmd(repo, "config", "--local", "user.name"); strings.TrimSpace(got) != "Ada Lovelace" {
		t.Fatalf("repo user.name: %q", got)
	}
	if got, _ := gitCmd(repo, "log", "-1", "--format=%an <%ae> %s"); strings.TrimSpace(got) != "Ada Lovelace <ada@example.invalid> one" {
		t.Fatalf("commit author: %q", got)
	}
}

// "Save for all repos" writes git's global config — here a private file, so
// the test never touches the real one.
func TestCommitSavesAGlobalGitIdentity(t *testing.T) {
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, []byte("[user]\n\tuseConfigOnly = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	for _, k := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL", "EMAIL"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	h, proj, repo := newRealSessionHarness(t)
	var row obj
	h.decode("POST", "/api/sessions", obj{"project_id": proj.ID, "agent": "review-real-agent", "yolo": false, "name": "work · main"}, 201, &row)
	base := fmt.Sprintf("/api/sessions/%d/git", int64(row.num("id")))
	writeReviewFile(t, filepath.Join(repo, "app.py"), "def main():\n    print('one')\n")
	var refused obj
	h.decode("POST", base+"/commit", obj{"message": "one", "stage_all": true, "allow_base_branch": true}, 409, &refused)
	if refused.str("code") != "no_git_identity" {
		t.Fatalf("refusal: %v", refused)
	}
	out := h.post(base+"/commit", obj{"message": "one", "stage_all": true, "allow_base_branch": true,
		"identity": obj{"name": "Grace Hopper", "email": "grace@example.invalid"}}, 200)
	if out["failed"] != nil {
		t.Fatalf("commit: %v", out)
	}
	saved, err := os.ReadFile(global)
	if err != nil || !strings.Contains(string(saved), "Grace Hopper") || !strings.Contains(string(saved), "grace@example.invalid") {
		t.Fatalf("global config: %s %v", saved, err)
	}
	if got, _ := gitCmd(repo, "config", "--local", "--get", "user.name"); strings.TrimSpace(got) != "" {
		t.Fatalf("a global answer must not be written to the repository: %q", got)
	}
}
