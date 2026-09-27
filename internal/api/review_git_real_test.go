package api_test

// Real git through the real local executor for the review workspace
// (docs/review.md): hunk staging, discard, the amend guard, force-with-lease,
// commit-hook failure, conflict resolution, AI commit messages, attribution
// and stored comments. Requires the isolated runner: tools/run-isolated-tests.sh.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

// newReviewSession launches an isolated-worktree session on a real repo and
// returns its id, worktree path and branch.
func newReviewSession(t *testing.T) (*harness, string, int64, string, string) {
	t.Helper()
	h, proj, repo := newRealSessionHarness(t)
	var row obj
	h.decode("POST", "/api/sessions", obj{
		"project_id": proj.ID, "agent": "review-real-agent", "worktree": obj{}, "yolo": false,
	}, 201, &row)
	wt := row.sub("workspace").str("path")
	if wt == "" {
		t.Fatalf("session has no worktree: %v", row)
	}
	return h, repo, int64(row.num("id")), wt, row.sub("workspace").str("branch")
}

func writeReviewFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitCmd(dir string, args ...string) (string, error) {
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	return string(out), err
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func numbered(n int, edit map[int]string) string {
	var b strings.Builder
	for i := 1; i <= n; i++ {
		if v, ok := edit[i]; ok {
			b.WriteString(v + "\n")
			continue
		}
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.String()
}

func gitFile(status obj, path string) obj {
	for _, f := range status.list("files") {
		if f.str("path") == path {
			return f
		}
	}
	return nil
}

func hunkFingerprints(f obj, scope string) []string {
	raw, _ := f[scope+"_hunks"].([]any)
	out := make([]string, len(raw))
	for i, v := range raw {
		out[i], _ = v.(string)
	}
	return out
}

func commitIn(t *testing.T, dir, msg string) {
	t.Helper()
	gitIn(t, dir, "-c", "user.name=Test", "-c", "user.email=t@example.invalid", "commit", "-qam", msg)
}

func TestRealGitStageAndUnstageSingleHunks(t *testing.T) {
	h, _, id, wt, _ := newReviewSession(t)
	gitIn(t, wt, "config", "user.name", "Test")
	gitIn(t, wt, "config", "user.email", "t@example.invalid")
	writeReviewFile(t, filepath.Join(wt, "long.txt"), numbered(30, nil))
	gitIn(t, wt, "add", "long.txt")
	commitIn(t, wt, "add long")
	writeReviewFile(t, filepath.Join(wt, "long.txt"), numbered(30, map[int]string{2: "EARLY", 28: "LATE"}))

	base := fmt.Sprintf("/api/sessions/%d/git", id)
	f := gitFile(h.get(base), "long.txt")
	hunks := hunkFingerprints(f, "unstaged")
	if f == nil || len(hunks) != 2 || f["staged"] != false {
		t.Fatalf("expected an unstaged file with two hunks: %v", f)
	}

	// A stale fingerprint is refused and applies nothing.
	if code := h.status("POST", base+"/hunk", obj{"path": "long.txt", "op": "stage", "index": 1, "fingerprint": "stale"}); code != 409 {
		t.Fatalf("stale hunk: %d", code)
	}
	h.post(base+"/hunk", obj{"path": "long.txt", "op": "stage", "index": 1, "fingerprint": hunks[1]}, 200)
	staged := gitIn(t, wt, "diff", "--cached")
	if !strings.Contains(staged, "+LATE") || strings.Contains(staged, "EARLY") {
		t.Fatalf("staging hunk 2 staged the wrong lines:\n%s", staged)
	}
	if work := gitIn(t, wt, "diff"); !strings.Contains(work, "+EARLY") || strings.Contains(work, "LATE") {
		t.Fatalf("the other hunk must stay unstaged:\n%s", work)
	}

	f = gitFile(h.get(base), "long.txt")
	stagedHunks := hunkFingerprints(f, "staged")
	if len(stagedHunks) != 1 {
		t.Fatalf("expected one staged hunk: %v", f)
	}
	h.post(base+"/hunk", obj{"path": "long.txt", "op": "unstage", "index": 0, "fingerprint": stagedHunks[0]}, 200)
	if cached := gitIn(t, wt, "diff", "--cached"); strings.TrimSpace(cached) != "" {
		t.Fatalf("unstaging left something staged:\n%s", cached)
	}
	if !strings.Contains(readFile(t, filepath.Join(wt, "long.txt")), "LATE") {
		t.Fatal("unstaging must never touch the working file")
	}

	// Whole-file stage and unstage, including a new file.
	writeReviewFile(t, filepath.Join(wt, "fresh.txt"), "brand new\n")
	h.post(base+"/stage", obj{"paths": []string{"long.txt", "fresh.txt"}}, 200)
	if names := gitIn(t, wt, "diff", "--cached", "--name-only"); !strings.Contains(names, "long.txt") || !strings.Contains(names, "fresh.txt") {
		t.Fatalf("stage all: %s", names)
	}
	h.post(base+"/unstage", obj{"paths": []string{"fresh.txt"}}, 200)
	if names := gitIn(t, wt, "diff", "--cached", "--name-only"); strings.Contains(names, "fresh.txt") {
		t.Fatalf("unstage: %s", names)
	}
}

func TestRealGitDiscardFileHunkAndRefusesEscapes(t *testing.T) {
	h, _, id, wt, _ := newReviewSession(t)
	gitIn(t, wt, "config", "user.name", "Test")
	gitIn(t, wt, "config", "user.email", "t@example.invalid")
	writeReviewFile(t, filepath.Join(wt, "long.txt"), numbered(30, nil))
	gitIn(t, wt, "add", "long.txt")
	commitIn(t, wt, "add long")
	writeReviewFile(t, filepath.Join(wt, "long.txt"), numbered(30, map[int]string{2: "KEEP-NOT", 28: "KEEP"}))
	writeReviewFile(t, filepath.Join(wt, "scratch.txt"), "throwaway\n")
	base := fmt.Sprintf("/api/sessions/%d/git", id)

	hunks := hunkFingerprints(gitFile(h.get(base), "long.txt"), "unstaged")
	h.post(base+"/hunk", obj{"path": "long.txt", "op": "discard", "index": 0, "fingerprint": hunks[0]}, 200)
	got := readFile(t, filepath.Join(wt, "long.txt"))
	if strings.Contains(got, "KEEP-NOT") || !strings.Contains(got, "KEEP") || !strings.Contains(got, "line 2\n") {
		t.Fatalf("discarding hunk 1 must restore only that hunk:\n%s", got)
	}

	h.post(base+"/discard", obj{"paths": []string{"scratch.txt", "long.txt"}}, 200)
	if _, err := os.Stat(filepath.Join(wt, "scratch.txt")); !os.IsNotExist(err) {
		t.Fatalf("an untracked file must be removed on discard: %v", err)
	}
	if got := readFile(t, filepath.Join(wt, "long.txt")); got != numbered(30, nil) {
		t.Fatalf("discarding the file must restore it:\n%s", got)
	}

	outside := filepath.Join(filepath.Dir(wt), "outside.txt")
	writeReviewFile(t, outside, "must survive\n")
	for _, p := range []string{"../outside.txt", "/etc/hostname", ""} {
		if code := h.status("POST", base+"/discard", obj{"paths": []string{p}}); code != 409 {
			t.Errorf("discard %q: %d", p, code)
		}
	}
	if readFile(t, outside) != "must survive\n" {
		t.Fatal("a path outside the workspace was touched")
	}
}

func TestRealGitAmendGuardAndForceWithLease(t *testing.T) {
	h, repo, id, wt, branch := newReviewSession(t)
	bare := t.TempDir()
	gitIn(t, bare, "init", "-q", "--bare")
	gitIn(t, repo, "remote", "add", "origin", bare)
	gitIn(t, wt, "config", "user.name", "Test")
	gitIn(t, wt, "config", "user.email", "t@example.invalid")
	base := fmt.Sprintf("/api/sessions/%d/git", id)

	writeReviewFile(t, filepath.Join(wt, "app.py"), "def main():\n    print('v1')\n")
	// Nothing committed on the session branch yet: HEAD is main's commit.
	if code := h.status("POST", base+"/commit", obj{"message": "x", "stage_all": true, "amend": true}); code != 409 {
		t.Fatalf("amending the base branch's commit: %d", code)
	}
	out := h.post(base+"/commit", obj{"message": "first", "stage_all": true}, 200)
	if out.list("steps")[0].num("rc") != 0 {
		t.Fatalf("commit: %v", out)
	}
	// Not pushed yet: amending is allowed without any confirmation.
	writeReviewFile(t, filepath.Join(wt, "app.py"), "def main():\n    print('v2')\n")
	h.post(base+"/commit", obj{"message": "first, amended", "stage_all": true, "amend": true}, 200)
	if n := strings.TrimSpace(gitIn(t, wt, "rev-list", "--count", "main..HEAD")); n != "1" {
		t.Fatalf("amend made a new commit instead: %s commits", n)
	}

	out = h.post(base+"/commit", obj{"message": "second", "stage_all": true, "push": true}, 200)
	if strings.Contains(fmt.Sprint(out), "nothing to commit") {
		writeReviewFile(t, filepath.Join(wt, "note.txt"), "x\n")
		out = h.post(base+"/commit", obj{"message": "second", "stage_all": true, "push": true}, 200)
	}
	if steps := out.list("steps"); len(steps) < 2 || steps[1].num("rc") != 0 {
		t.Fatalf("push: %v", out)
	}
	pushed := strings.TrimSpace(gitIn(t, bare, "rev-parse", branch))

	status := h.get(base)
	if status["head_pushed"] != true || status.str("remote_sha") != pushed {
		t.Fatalf("status must report the pushed head and the remote commit: %v", status)
	}

	// The guard: HEAD is on origin now.
	writeReviewFile(t, filepath.Join(wt, "note.txt"), "y\n")
	code, raw := h.request("POST", base+"/commit", obj{"message": "rewrite", "stage_all": true, "amend": true}, nil)
	var refusal obj
	_ = json.Unmarshal(raw, &refusal)
	if code != 409 || refusal.str("code") != "amend_pushed" {
		t.Fatalf("amending a pushed commit must be refused: %d %s", code, raw)
	}
	if strings.TrimSpace(gitIn(t, wt, "rev-parse", "HEAD")) != pushed {
		t.Fatal("the refused amend changed HEAD")
	}

	// Confirmed amend, then a force push pinned to a stale lease fails...
	h.post(base+"/commit", obj{"message": "rewrite", "stage_all": true, "amend": true, "allow_pushed_amend": true}, 200)
	out = h.post(base+"/commit", obj{"message": "unused", "stage_all": true, "amend": true, "allow_pushed_amend": true,
		"force_with_lease": true, "lease": "0123456789abcdef0123456789abcdef01234567"}, 200)
	steps := out.list("steps")
	if len(steps) < 2 || steps[1].str("step") != "force-push" || steps[1].num("rc") == 0 {
		t.Fatalf("a stale lease must fail the force push: %v", out)
	}
	if strings.TrimSpace(gitIn(t, bare, "rev-parse", branch)) != pushed {
		t.Fatal("a failed lease still moved the remote")
	}
	// ...and one pinned to the commit the remote really has succeeds.
	out = h.post(base+"/commit", obj{"message": "rewrite final", "stage_all": true, "amend": true, "allow_pushed_amend": true,
		"force_with_lease": true, "lease": pushed}, 200)
	steps = out.list("steps")
	if len(steps) < 2 || steps[1].num("rc") != 0 {
		t.Fatalf("force push with the right lease: %v", out)
	}
	if got := strings.TrimSpace(gitIn(t, bare, "log", "-1", "--format=%s", branch)); got != "rewrite final" {
		t.Fatalf("the remote did not get the rewritten commit: %q", got)
	}
	// The standalone push action: rewrite again locally, then a stale lease is
	// refused by git and the current one goes through.
	h.post(base+"/commit", obj{"message": "rewrite once more", "stage_all": true, "amend": true, "allow_pushed_amend": true}, 200)
	out = h.post(base+"/push", obj{"force_with_lease": true, "lease": pushed}, 200)
	if out.list("steps")[0].num("rc") == 0 {
		t.Fatalf("a lease on the old remote commit must fail: %v", out)
	}
	current := strings.TrimSpace(gitIn(t, bare, "rev-parse", branch))
	out = h.post(base+"/push", obj{"force_with_lease": true, "lease": current}, 200)
	if out.list("steps")[0].num("rc") != 0 {
		t.Fatalf("a lease on the current remote commit must succeed: %v", out)
	}
	if got := strings.TrimSpace(gitIn(t, bare, "log", "-1", "--format=%s", branch)); got != "rewrite once more" {
		t.Fatalf("the remote did not get the second rewrite: %q", got)
	}
	if code := h.status("POST", base+"/commit", obj{"message": "x", "force_with_lease": true, "lease": "not a sha"}); code != 422 {
		t.Fatalf("a malformed lease: %d", code)
	}
}

func TestRealGitCommitHookFailureOffersFixWithAgent(t *testing.T) {
	h, repo, id, wt, _ := newReviewSession(t)
	gitIn(t, wt, "config", "user.name", "Test")
	gitIn(t, wt, "config", "user.email", "t@example.invalid")
	hook := filepath.Join(repo, ".git", "hooks", "pre-commit")
	writeReviewFile(t, hook, "#!/bin/sh\necho 'lint: app.py:2 trailing whitespace' >&2\nexit 1\n")
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	writeReviewFile(t, filepath.Join(wt, "app.py"), "def main():\n    print('hi')   \n")
	base := fmt.Sprintf("/api/sessions/%d/git", id)

	out := h.post(base+"/commit", obj{"message": "feat: greet", "stage_all": true}, 200)
	failure := out.sub("hook_failure")
	if out.str("failed") != "commit" || !strings.Contains(failure.str("output"), "trailing whitespace") {
		t.Fatalf("a hook failure must be reported with its output: %v", out)
	}
	if hooks, _ := failure["hooks"].([]any); len(hooks) != 1 || hooks[0] != "pre-commit" {
		t.Fatalf("the failing hook must be named: %v", failure)
	}
	h.post(base+"/fix-hook", obj{"message": "feat: greet", "output": failure.str("output"), "hooks": []string{"pre-commit"}}, 200)
	if code := h.status("POST", base+"/fix-hook", obj{"output": " "}); code != 422 {
		t.Fatalf("fix-hook with no output: %d", code)
	}
	// Without a hook, "nothing to commit" is not offered as a hook failure.
	if err := os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	h.post(base+"/commit", obj{"message": "feat: greet", "stage_all": true}, 200)
	out = h.post(base+"/commit", obj{"message": "again", "stage_all": true}, 200)
	if _, has := out["hook_failure"]; has || out.str("detail") != "commit failed: nothing to commit" {
		t.Fatalf("nothing to commit is not a hook failure: %v", out)
	}
}

func TestRealGitCommitMessageUsesStagedDiff(t *testing.T) {
	h, _, id, wt, _ := newReviewSession(t)
	writeReviewFile(t, filepath.Join(wt, "app.py"), "def main():\n    print('staged change')\n")
	writeReviewFile(t, filepath.Join(wt, "other.txt"), "unstaged\n")
	gitIn(t, wt, "add", "app.py")
	var prompt, usedAgent string
	h.App.Server.SummaryGen = func(ctx context.Context, ex executor.Executor, agent, model, p string) (string, error) {
		prompt, usedAgent = p, agent
		return "```\nPrint the staged change\n```", nil
	}
	t.Cleanup(func() { h.App.Server.SummaryGen = nil })
	out := h.post(fmt.Sprintf("/api/sessions/%d/git/commit-message", id), obj{}, 200)
	if out.str("message") != "Print the staged change" {
		t.Fatalf("message: %v", out)
	}
	if !strings.Contains(prompt, "staged change") || strings.Contains(prompt, "other.txt") {
		t.Fatalf("the prompt must describe only the staged diff:\n%s", prompt)
	}
	if usedAgent == "" {
		t.Fatal("no agent was chosen")
	}
}

func TestRealGitConflictResolution(t *testing.T) {
	h, repo, id, wt, _ := newReviewSession(t)
	for _, dir := range []string{wt, repo} {
		gitIn(t, dir, "config", "user.name", "Test")
		gitIn(t, dir, "config", "user.email", "t@example.invalid")
	}
	writeReviewFile(t, filepath.Join(wt, "app.py"), "def main():\n    print('from the session')\n")
	writeReviewFile(t, filepath.Join(wt, "notes.txt"), "session notes\n")
	gitIn(t, wt, "add", ".")
	commitIn(t, wt, "session work")
	writeReviewFile(t, filepath.Join(repo, "app.py"), "def main():\n    print('from main')\n")
	writeReviewFile(t, filepath.Join(repo, "notes.txt"), "main notes\n")
	gitIn(t, repo, "add", ".")
	commitIn(t, repo, "main work")
	// merge main into the session branch: both files conflict
	if out, err := gitCmd(wt, "merge", "main"); err == nil {
		t.Fatalf("expected a conflict: %s", out)
	}

	base := fmt.Sprintf("/api/sessions/%d/git", id)
	// Opening the live diff mid-merge must leave the conflict alone (it used
	// to re-add every path and wipe the unmerged index entries).
	h.get(fmt.Sprintf("/api/sessions/%d/diff", id))
	if st := gitIn(t, wt, "status", "--porcelain"); !strings.Contains(st, "UU app.py") {
		t.Fatalf("the live diff disturbed the conflict:\n%s", st)
	}
	list := h.get(base + "/conflicts")
	if list.str("operation") != "merge" || len(list.list("files")) != 2 {
		t.Fatalf("conflicts: %v", list)
	}
	detail := h.get(base + "/conflicts?path=app.py").sub("file")
	merged := detail.str("merged_text")
	for _, want := range []string{"<<<<<<<", "||||||| base", "print('from the session')", "print('from main')", ">>>>>>>"} {
		if !strings.Contains(merged, want) {
			t.Fatalf("the three-way view lacks %q:\n%s", want, merged)
		}
	}
	if !strings.Contains(detail.str("base_text"), "pass") {
		t.Fatalf("base text: %v", detail)
	}

	// Leftover markers are refused unless explicitly allowed.
	if code := h.status("POST", base+"/resolve", obj{"path": "app.py", "content": merged}); code != 409 {
		t.Fatalf("markers left in: %d", code)
	}
	both := "def main():\n    print('from the session')\n    print('from main')\n"
	h.post(base+"/resolve", obj{"path": "app.py", "content": both}, 200)
	if got := readFile(t, filepath.Join(wt, "app.py")); got != both {
		t.Fatalf("resolved file: %q", got)
	}
	h.post(base+"/resolve", obj{"path": "notes.txt", "take": "theirs"}, 200)
	if got := readFile(t, filepath.Join(wt, "notes.txt")); got != "main notes\n" {
		t.Fatalf("take theirs: %q", got)
	}
	if files := h.get(base + "/conflicts").list("files"); len(files) != 0 {
		t.Fatalf("still conflicted: %v", files)
	}
	if st := gitIn(t, wt, "status", "--porcelain"); strings.Contains(st, "UU") {
		t.Fatalf("resolved files must be staged: %s", st)
	}
	if code := h.status("POST", base+"/resolve", obj{"path": "app.py", "take": "ours"}); code != 409 {
		t.Fatalf("resolving a file no longer in conflict: %d", code)
	}
	out := h.post(base+"/commit", obj{"message": "Merge main"}, 200)
	if out.list("steps")[0].num("rc") != 0 {
		t.Fatalf("finishing the merge: %v", out)
	}
	if parents := strings.Fields(gitIn(t, wt, "log", "-1", "--format=%P")); len(parents) != 2 {
		t.Fatalf("the merge commit must have two parents: %v", parents)
	}
}

func TestRealGitAbortMerge(t *testing.T) {
	h, repo, id, wt, _ := newReviewSession(t)
	for _, dir := range []string{wt, repo} {
		gitIn(t, dir, "config", "user.name", "Test")
		gitIn(t, dir, "config", "user.email", "t@example.invalid")
	}
	writeReviewFile(t, filepath.Join(wt, "app.py"), "a\n")
	commitIn(t, wt, "s")
	writeReviewFile(t, filepath.Join(repo, "app.py"), "b\n")
	commitIn(t, repo, "m")
	_, _ = gitCmd(wt, "merge", "main")
	// A new file the live diff marks intent-to-add must not block the abort.
	writeReviewFile(t, filepath.Join(wt, "untracked.txt"), "keep me\n")
	h.get(fmt.Sprintf("/api/sessions/%d/diff", id))
	base := fmt.Sprintf("/api/sessions/%d/git", id)
	h.post(base+"/abort", obj{}, 200)
	if readFile(t, filepath.Join(wt, "untracked.txt")) != "keep me\n" {
		t.Fatal("abort must never touch an untracked file")
	}
	if h.get(base).str("operation") != "" || readFile(t, filepath.Join(wt, "app.py")) != "a\n" {
		t.Fatal("abort must restore the pre-merge state")
	}
	if code := h.status("POST", base+"/abort", obj{}); code != 409 {
		t.Fatalf("abort with nothing in progress: %d", code)
	}
}

func TestRealGitImageBlob(t *testing.T) {
	h, _, id, wt, _ := newReviewSession(t)
	png := []byte("\x89PNG\r\n\x1a\nfake")
	if err := os.WriteFile(filepath.Join(wt, "logo.png"), png, 0o600); err != nil {
		t.Fatal(err)
	}
	base := fmt.Sprintf("/api/sessions/%d/git/blob", id)
	out := h.get(base + "?path=logo.png&side=new")
	if out.str("mime") != "image/png" || out.str("data") == "" {
		t.Fatalf("new side: %v", out)
	}
	if old := h.get(base + "?path=logo.png&side=old&ref=HEAD"); old["missing"] != true {
		t.Fatalf("a new image has no old side: %v", old)
	}
	if code := h.status("GET", base+"?path=app.py&side=new", nil); code != 422 {
		t.Fatalf("a non-image: %d", code)
	}
	if code := h.status("GET", base+"?path=logo.png&side=old&ref=main%24%28rm%29", nil); code != 422 {
		t.Fatalf("a malformed ref: %d", code)
	}
}

// Attribution from the agent's own edit hook, flipped by a human edit, and
// from an agent co-author trailer in history.
func TestRealAttributionFromHooksAndHistory(t *testing.T) {
	h, _, id, wt, _ := newReviewSession(t)
	gitIn(t, wt, "config", "user.name", "Test")
	gitIn(t, wt, "config", "user.email", "t@example.invalid")
	sess, err := h.App.DB.Session(id)
	if err != nil {
		t.Fatal(err)
	}
	attribution := func() obj {
		return h.get(fmt.Sprintf("/api/sessions/%d/attribution", id)).list("repos")[0].sub("files")
	}
	// Before any hook evidence, uncommitted lines are not claimed for anyone.
	writeReviewFile(t, filepath.Join(wt, "app.py"), "def main():\n    pass\n    agent_line()\n    human_line()\n")
	if files := attribution(); len(files) != 0 {
		t.Fatalf("no evidence yet, nothing should be attributed: %v", files)
	}

	hook, _ := json.Marshal(map[string]any{
		"hook_event_name": "PostToolUse", "tool_name": "Edit", "cwd": wt,
		"tool_input": map[string]any{"file_path": filepath.Join(wt, "app.py"),
			"old_string": "    pass\n", "new_string": "    pass\n    agent_line()\n"},
		"tool_response": map[string]any{"structuredPatch": []any{
			map[string]any{"lines": []any{"     pass", "+    agent_line()"}}}},
	})
	if code, raw := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PostToolUse", id), string(hook), sess.HookToken); code != 200 {
		t.Fatalf("hook: %d %s", code, raw)
	}
	file := attribution().sub("app.py")
	if fmt.Sprint(file["agent"]) != "[[3 3]]" || fmt.Sprint(file["human"]) != "[[4 4]]" {
		t.Fatalf("agent line 3, human line 4: %v", file)
	}

	// A human rewrites the agent's line: it becomes theirs.
	writeReviewFile(t, filepath.Join(wt, "app.py"), "def main():\n    pass\n    agent_line(fixed=True)\n    human_line()\n")
	file = attribution().sub("app.py")
	if file["agent"] != nil || fmt.Sprint(file["human"]) != "[[3 4]]" {
		t.Fatalf("a human edit must flip the line: %v", file)
	}

	// History: a commit with an agent co-author trailer is the agent's.
	writeReviewFile(t, filepath.Join(wt, "gen.py"), "generated = True\n")
	gitIn(t, wt, "add", "gen.py")
	gitIn(t, wt, "commit", "-qm", "Add generated\n\nCo-Authored-By: Claude <noreply@anthropic.com>")
	writeReviewFile(t, filepath.Join(wt, "mine.py"), "handwritten = True\n")
	gitIn(t, wt, "add", "mine.py")
	gitIn(t, wt, "commit", "-qm", "Add mine")
	files := attribution()
	if fmt.Sprint(files.sub("gen.py")["agent"]) != "[[1 1]]" || fmt.Sprint(files.sub("mine.py")["human"]) != "[[1 1]]" {
		t.Fatalf("history attribution: %v", files)
	}
}

func TestRealReviewCommentsRoundTripAndBatchSend(t *testing.T) {
	h, _, id, wt, _ := newReviewSession(t)
	writeReviewFile(t, filepath.Join(wt, "app.py"), "def main():\n    print('hi')\n")
	base := fmt.Sprintf("/api/sessions/%d/review", id)
	a := h.post(base+"/comments", obj{"file": "app.py", "line": 2, "code": "    print('hi')", "text": "use logging"}, 201)
	b := h.post(base+"/comments", obj{"file": "app.py", "line": 1, "code": "def main():", "text": "add a docstring"}, 201)
	if code := h.status("POST", base+"/comments", obj{"file": "app.py", "line": 1, "text": " "}); code != 422 {
		t.Fatalf("an empty comment: %d", code)
	}
	h.post(base, obj{"comment_ids": []int64{a.id(), b.id()}, "summary": "two things"}, 200)
	state := h.get(base + "/state")
	for _, c := range state.list("comments") {
		if c.str("status") != "sent" || c.num("round") != 1 {
			t.Fatalf("sent comments must be in round 1: %v", c)
		}
	}
	// Reopen one and resolve the other; the reopened one goes out in round 2.
	h.patch(fmt.Sprintf("%s/comments/%d", base, a.id()), obj{"status": "draft"}, 200)
	h.patch(fmt.Sprintf("%s/comments/%d", base, b.id()), obj{"status": "resolved"}, 200)
	out := h.post(base, obj{"comment_ids": []int64{a.id()}}, 200)
	if out.num("round") != 2 {
		t.Fatalf("second round: %v", out)
	}
	if code := h.status("POST", base, obj{"comment_ids": []int64{b.id()}}); code != 422 {
		t.Fatalf("a resolved comment cannot be sent without reopening: %d", code)
	}

	h.decode("PUT", base+"/viewed", obj{"path": "app.py", "fingerprint": "abc"}, 200, nil)
	if v := h.get(base + "/state").list("viewed"); len(v) != 1 || v[0].str("fingerprint") != "abc" {
		t.Fatalf("viewed: %v", v)
	}
	h.decode("PUT", base+"/viewed", obj{"path": "app.py", "fingerprint": ""}, 200, nil)
	if v := h.get(base + "/state").list("viewed"); len(v) != 0 {
		t.Fatalf("clearing viewed: %v", v)
	}
	h.decode("DELETE", fmt.Sprintf("%s/comments/%d", base, b.id()), nil, 200, nil)
	if code := h.status("DELETE", fmt.Sprintf("/api/sessions/%d/review/comments/%d", id+1000, a.id()), nil); code != 404 {
		t.Fatalf("another session's comment: %d", code)
	}
}

func postHook(t *testing.T, h *harness, id int64, toolInput, toolResponse map[string]any) {
	t.Helper()
	sess, err := h.App.DB.Session(id)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"hook_event_name": "PostToolUse", "tool_name": "Edit",
		"tool_input": toolInput, "tool_response": toolResponse})
	if code, raw := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/PostToolUse", id), string(body), sess.HookToken); code != 200 {
		t.Fatalf("hook: %d %s", code, raw)
	}
}

func TestRealGitStagesChosenLinesAndNewFileHunks(t *testing.T) {
	h, _, id, wt, _ := newReviewSession(t)
	gitIn(t, wt, "config", "user.name", "Test")
	gitIn(t, wt, "config", "user.email", "t@example.invalid")
	writeReviewFile(t, filepath.Join(wt, "f.txt"), "a\nb\nc\nd\n")
	gitIn(t, wt, "add", "f.txt")
	commitIn(t, wt, "f")
	writeReviewFile(t, filepath.Join(wt, "f.txt"), "a\nB1\nB2\nc\nd\n")
	writeReviewFile(t, filepath.Join(wt, "new.txt"), "n1\nn2\nn3\n")
	base := fmt.Sprintf("/api/sessions/%d/git", id)
	fp := func(path, scope string) string {
		return hunkFingerprints(gitFile(h.get(base), path), scope)[0]
	}

	// Hunk body: " a", "-b", "+B1", "+B2", " c", " d". Stage only "-b" and "+B1".
	h.post(base+"/hunk", obj{"path": "f.txt", "op": "stage", "index": 0, "fingerprint": fp("f.txt", "unstaged"), "lines": []int{1, 2}}, 200)
	if got := gitIn(t, wt, "show", ":f.txt"); got != "a\nB1\nc\nd\n" {
		t.Fatalf("staging two lines: index is %q", got)
	}
	if code := h.status("POST", base+"/hunk", obj{"path": "f.txt", "op": "stage", "index": 0,
		"fingerprint": fp("f.txt", "unstaged"), "lines": []int{0}}); code != 409 {
		t.Fatalf("choosing a context line: %d", code)
	}
	// Unstage just "+B1" again: the staged removal of "b" stays.
	h.post(base+"/hunk", obj{"path": "f.txt", "op": "unstage", "index": 0, "fingerprint": fp("f.txt", "staged"), "lines": []int{2}}, 200)
	if got := gitIn(t, wt, "show", ":f.txt"); got != "a\nc\nd\n" {
		t.Fatalf("unstaging one line: index is %q", got)
	}
	if got := readFile(t, filepath.Join(wt, "f.txt")); got != "a\nB1\nB2\nc\nd\n" {
		t.Fatalf("staging must never touch the working file: %q", got)
	}

	// A new, untracked file: stage its first and last line only.
	h.post(base+"/hunk", obj{"path": "new.txt", "op": "stage", "index": 0, "fingerprint": fp("new.txt", "unstaged"), "lines": []int{0, 2}}, 200)
	if got := gitIn(t, wt, "show", ":new.txt"); got != "n1\nn3\n" {
		t.Fatalf("partly staging a new file: index is %q", got)
	}
	h.post(base+"/hunk", obj{"path": "new.txt", "op": "unstage", "index": 0, "fingerprint": fp("new.txt", "staged"), "lines": []int{1}}, 200)
	if got := gitIn(t, wt, "show", ":new.txt"); got != "n1\n" {
		t.Fatalf("partly unstaging a new file: index is %q", got)
	}
	// Discarding one line of an untracked file edits the file...
	writeReviewFile(t, filepath.Join(wt, "scratch.txt"), "keep\ndrop\n")
	h.post(base+"/hunk", obj{"path": "scratch.txt", "op": "discard", "index": 0, "fingerprint": fp("scratch.txt", "unstaged"), "lines": []int{1}}, 200)
	if got := readFile(t, filepath.Join(wt, "scratch.txt")); got != "keep\n" {
		t.Fatalf("discarding one line of a new file: %q", got)
	}
	// ...and discarding its whole hunk removes it.
	h.post(base+"/hunk", obj{"path": "scratch.txt", "op": "discard", "index": 0, "fingerprint": fp("scratch.txt", "unstaged")}, 200)
	if _, err := os.Stat(filepath.Join(wt, "scratch.txt")); !os.IsNotExist(err) {
		t.Fatalf("a fully discarded new file must be gone: %v", err)
	}
}

// Agent marks are pruned: hashes no longer in the file, files that no longer
// differ from the base, and every mark of a long-ended session.
func TestRealAttributionPrunesAgentMarks(t *testing.T) {
	h, _, id, wt, _ := newReviewSession(t)
	app := filepath.Join(wt, "app.py")
	writeReviewFile(t, app, "def main():\n    pass\n    kept()\n    replaced()\n")
	writeReviewFile(t, filepath.Join(wt, "other.py"), "agent_only = 1\n")
	postHook(t, h, id, map[string]any{"file_path": app}, map[string]any{"structuredPatch": []any{
		map[string]any{"lines": []any{"+    kept()", "+    replaced()"}}}})
	postHook(t, h, id, map[string]any{"file_path": filepath.Join(wt, "other.py")}, map[string]any{"structuredPatch": []any{
		map[string]any{"lines": []any{"+agent_only = 1"}}}})
	count := func() int {
		n, err := h.App.DB.CountAgentLineMarks(id)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 3 {
		t.Fatalf("recorded marks: %d", n)
	}
	// A human rewrites one agent line and reverts the other file entirely.
	writeReviewFile(t, app, "def main():\n    pass\n    kept()\n    mine()\n")
	if err := os.Remove(filepath.Join(wt, "other.py")); err != nil {
		t.Fatal(err)
	}
	file := h.get(fmt.Sprintf("/api/sessions/%d/attribution", id)).list("repos")[0].sub("files").sub("app.py")
	if fmt.Sprint(file["agent"]) != "[[3 3]]" || fmt.Sprint(file["human"]) != "[[4 4]]" {
		t.Fatalf("attribution after the edit: %v", file)
	}
	if n := count(); n != 1 {
		t.Fatalf("only kept() should still be marked, got %d marks", n)
	}

	// An ended session's marks go once it is past the retention period.
	if err := h.App.DB.Update("sessions", id, map[string]any{"ended_at": store.Now() - 15*24*3600}); err != nil {
		t.Fatal(err)
	}
	if n, err := h.App.DB.PruneEndedAgentLineMarks(store.Now() - 14*24*3600); err != nil || n != 1 || count() != 0 {
		t.Fatalf("pruning an ended session: %d %v, %d left", n, err, count())
	}
}

// A session in its own checkout, whose base branch does not exist there,
// still shows its uncommitted work.
func TestRealLiveDiffFallsBackToHeadWithoutTheBaseBranch(t *testing.T) {
	h, proj, repo := newRealSessionHarness(t)
	gitIn(t, repo, "branch", "-m", "main", "trunk")
	var row obj
	h.decode("POST", "/api/sessions", obj{"project_id": proj.ID, "agent": "review-real-agent", "yolo": false}, 201, &row)
	writeReviewFile(t, filepath.Join(repo, "app.py"), "def main():\n    print('uncommitted')\n")
	var diff obj
	h.decode("GET", fmt.Sprintf("/api/sessions/%d/diff", int64(row.num("id"))), nil, 200, &diff)
	files := diff.list("repos")[0].list("files")
	if len(files) != 1 || !strings.Contains(files[0].str("patch"), "uncommitted") {
		t.Fatalf("expected the uncommitted change against HEAD: %v", diff)
	}
}
