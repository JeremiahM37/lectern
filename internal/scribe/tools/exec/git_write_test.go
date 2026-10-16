package exec

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// commitFile writes name, commits it with msg and returns the new HEAD.
func commitFile(t *testing.T, dir, name, content, msg string) string {
	t.Helper()
	writeFile(t, filepath.Join(dir, name), content)
	gitIn(t, dir, "add", "--", name)
	gitIn(t, dir, "commit", "-q", "-m", msg)
	return head(t, dir)
}

func head(t *testing.T, dir string) string {
	t.Helper()
	return strings.TrimSpace(gitIn(t, dir, "rev-parse", "HEAD"))
}

// bareRemote creates an empty bare repository to push to.
func bareRemote(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gitIn(t, dir, "init", "--bare", "-q", "-b", "main")
	return dir
}

// gitTool returns the git tool from a registry built by newTestRegistry.
func gitRun(t *testing.T, r *tools.Registry, env *tools.Env, args map[string]any) tools.Result {
	t.Helper()
	return runTool(t, r, "git", env, args)
}

// approvingHost returns a fakeHost that answers with d.
func approvingHost(d tools.Decision) *fakeHost {
	return &fakeHost{decision: d}
}

func TestGitPushAndPullRoundTrip(t *testing.T) {
	remote := bareRemote(t)
	dir := initRepo(t)
	gitIn(t, dir, "remote", "add", "origin", remote)
	commitFile(t, dir, "a.txt", "one\n", "first")
	r, _ := newTestRegistry(t, Options{})
	host := approvingHost(tools.Deny)
	env := newTestEnv(dir, host)

	push := gitRun(t, r, env, map[string]any{"action": "push", "remote": "origin", "name": "main"})
	if push.IsError {
		t.Fatalf("push: %+v", push)
	}
	if got := strings.TrimSpace(gitIn(t, remote, "rev-parse", "main")); got != head(t, dir) {
		t.Fatalf("remote main = %s, want local HEAD %s", got, head(t, dir))
	}
	if host.approvalCount() != 0 {
		t.Fatalf("a plain push must not ask: %d approvals", host.approvalCount())
	}

	// A second clone pushes a change; pulling it into dir must bring it in.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, filepath.Dir(other), "clone", "-q", remote, other)
	gitIn(t, other, "config", "user.name", "Scribe Test")
	gitIn(t, other, "config", "user.email", "scribe@test.invalid")
	commitFile(t, other, "b.txt", "two\n", "second")
	gitIn(t, other, "push", "-q", "origin", "main")

	pull := gitRun(t, r, env, map[string]any{"action": "pull", "remote": "origin", "name": "main"})
	if pull.IsError {
		t.Fatalf("pull: %+v", pull)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "b.txt")); err != nil || string(b) != "two\n" {
		t.Fatalf("pull did not bring b.txt in: %v %q", err, b)
	}
}

func TestGitPushNameNeedsRemote(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "one\n", "first")
	r, _ := newTestRegistry(t, Options{})
	res := gitRun(t, r, newTestEnv(dir, nil), map[string]any{"action": "push", "name": "main"})
	if !res.IsError || !strings.Contains(res.Output, "remote") {
		t.Fatalf("name without remote must be refused: %+v", res)
	}
}

func TestGitTransportFlagsRefused(t *testing.T) {
	dir := initRepo(t)
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, approvingHost(tools.AllowOnce))
	for _, call := range []map[string]any{
		{"action": "push", "args": []string{"--receive-pack=touch /tmp/x"}},
		{"action": "pull", "args": []string{"--upload-pack", "sh"}},
	} {
		if res := gitRun(t, r, env, call); !res.IsError || !strings.Contains(res.Output, "refused") {
			t.Fatalf("%v must be refused: %+v", call, res)
		}
	}
}

func TestGitForcePushAndHardResetNeedApproval(t *testing.T) {
	remote := bareRemote(t)
	dir := initRepo(t)
	gitIn(t, dir, "remote", "add", "origin", remote)
	commitFile(t, dir, "a.txt", "1\n", "c1")
	c2 := commitFile(t, dir, "a.txt", "2\n", "c2")
	r, _ := newTestRegistry(t, Options{})
	host := approvingHost(tools.Deny)
	env := newTestEnv(dir, host)
	if res := gitRun(t, r, env, map[string]any{"action": "push", "remote": "origin", "name": "main"}); res.IsError {
		t.Fatalf("setup push: %+v", res)
	}

	// Hard reset is destructive: denied, so HEAD must not move.
	if res := gitRun(t, r, env, map[string]any{"action": "reset", "mode": "hard", "ref": "HEAD~1"}); !res.IsError {
		t.Fatalf("denied hard reset must fail: %+v", res)
	}
	if head(t, dir) != c2 {
		t.Fatal("denied hard reset moved HEAD")
	}
	if host.approvalCount() != 1 || !host.approvals[0].Danger {
		t.Fatalf("hard reset should ask once with Danger: %+v", host.approvals)
	}

	// Allowed: reset, then commit something different and force-push it.
	host.decision = tools.AllowOnce
	if res := gitRun(t, r, env, map[string]any{"action": "reset", "mode": "hard", "ref": "HEAD~1"}); res.IsError {
		t.Fatalf("approved hard reset: %+v", res)
	}
	c3 := commitFile(t, dir, "a.txt", "3\n", "c3")

	host.decision = tools.Deny
	if res := gitRun(t, r, env, map[string]any{"action": "push", "remote": "origin", "name": "main", "force": true}); !res.IsError {
		t.Fatalf("denied force push must fail: %+v", res)
	}
	if got := strings.TrimSpace(gitIn(t, remote, "rev-parse", "main")); got != c2 {
		t.Fatalf("denied force push changed the remote to %s", got)
	}

	host.decision = tools.AllowOnce
	if res := gitRun(t, r, env, map[string]any{"action": "push", "remote": "origin", "name": "main", "force": true}); res.IsError {
		t.Fatalf("approved force push: %+v", res)
	}
	if got := strings.TrimSpace(gitIn(t, remote, "rev-parse", "main")); got != c3 {
		t.Fatalf("remote main = %s after force push, want %s", got, c3)
	}
	if host.approvalCount() != 4 {
		t.Fatalf("expected 4 approvals in total, got %d", host.approvalCount())
	}
}

func TestGitResetModesAndPathForm(t *testing.T) {
	dir := initRepo(t)
	c1 := commitFile(t, dir, "a.txt", "1\n", "c1")
	commitFile(t, dir, "a.txt", "2\n", "c2")
	r, _ := newTestRegistry(t, Options{})
	host := approvingHost(tools.Deny)
	env := newTestEnv(dir, host)

	if res := gitRun(t, r, env, map[string]any{"action": "reset", "mode": "soft", "ref": c1}); res.IsError {
		t.Fatalf("soft reset: %+v", res)
	}
	if head(t, dir) != c1 {
		t.Fatal("soft reset did not move HEAD")
	}
	if staged := gitIn(t, dir, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) == "" {
		t.Fatal("soft reset should keep the change staged")
	}
	if host.approvalCount() != 0 {
		t.Fatalf("soft reset must not ask: %d approvals", host.approvalCount())
	}

	if res := gitRun(t, r, env, map[string]any{"action": "reset", "mode": "bogus"}); !res.IsError {
		t.Fatal("unknown mode must be refused")
	}
	if res := gitRun(t, r, env, map[string]any{"action": "reset", "mode": "soft", "files": []string{"a.txt"}}); !res.IsError {
		t.Fatal("soft reset with paths must be refused")
	}

	// Path form unstages without touching the working tree.
	if res := gitRun(t, r, env, map[string]any{"action": "reset", "files": []string{"a.txt"}}); res.IsError {
		t.Fatalf("reset path form: %+v", res)
	}
	if staged := gitIn(t, dir, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Fatalf("path reset should unstage, still staged: %q", staged)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "2\n" {
		t.Fatalf("path reset changed the working tree: %q", b)
	}
}

func TestGitHardResetWithoutHostIsDenied(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "1\n", "c1")
	c2 := commitFile(t, dir, "a.txt", "2\n", "c2")
	r, _ := newTestRegistry(t, Options{})
	res := gitRun(t, r, newTestEnv(dir, nil), map[string]any{"action": "reset", "mode": "hard", "ref": "HEAD~1"})
	if !res.IsError || !strings.Contains(res.Output, "No one is available") {
		t.Fatalf("hard reset with no host must be denied: %+v", res)
	}
	if head(t, dir) != c2 {
		t.Fatal("denied reset moved HEAD")
	}
}

func TestGitRestoreDiscardsOnlyWithApproval(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "one\n", "base")
	writeFile(t, filepath.Join(dir, "a.txt"), "two\n")
	r, _ := newTestRegistry(t, Options{})
	host := approvingHost(tools.Deny)
	env := newTestEnv(dir, host)

	if res := gitRun(t, r, env, map[string]any{"action": "restore", "files": []string{"a.txt"}}); !res.IsError {
		t.Fatalf("denied restore of a modified file must fail: %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "two\n" {
		t.Fatalf("denied restore changed the file: %q", b)
	}
	if host.approvalCount() != 1 || !host.approvals[0].Danger {
		t.Fatalf("restore of a modified file should ask with Danger: %+v", host.approvals)
	}

	host.decision = tools.AllowOnce
	if res := gitRun(t, r, env, map[string]any{"action": "restore", "files": []string{"a.txt"}}); res.IsError {
		t.Fatalf("approved restore: %+v", res)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "one\n" {
		t.Fatalf("restore did not revert the file: %q", b)
	}

	// Unstaging an index change loses no work, so it does not ask.
	writeFile(t, filepath.Join(dir, "a.txt"), "three\n")
	gitIn(t, dir, "add", "a.txt")
	before := host.approvalCount()
	if res := gitRun(t, r, env, map[string]any{"action": "restore", "staged": true, "files": []string{"a.txt"}}); res.IsError {
		t.Fatalf("restore --staged: %+v", res)
	}
	if host.approvalCount() != before {
		t.Fatal("restore --staged must not ask")
	}
	if staged := gitIn(t, dir, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Fatalf("restore --staged should empty the index: %q", staged)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "a.txt")); string(b) != "three\n" {
		t.Fatalf("restore --staged must keep the working tree: %q", b)
	}

	if res := gitRun(t, r, env, map[string]any{"action": "restore"}); !res.IsError {
		t.Fatal("restore without files must be refused")
	}
}

func TestGitUnstage(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "a\n", "base")
	writeFile(t, filepath.Join(dir, "b.txt"), "b\n")
	gitIn(t, dir, "add", "b.txt")
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	if res := gitRun(t, r, env, map[string]any{"action": "unstage", "files": []string{"b.txt"}}); res.IsError {
		t.Fatalf("unstage: %+v", res)
	}
	if staged := gitIn(t, dir, "diff", "--cached", "--name-only"); strings.TrimSpace(staged) != "" {
		t.Fatalf("b.txt still staged: %q", staged)
	}
	if _, err := os.Stat(filepath.Join(dir, "b.txt")); err != nil {
		t.Fatalf("unstage must keep the file: %v", err)
	}
	if res := gitRun(t, r, env, map[string]any{"action": "unstage"}); !res.IsError {
		t.Fatal("unstage with neither files nor all must be refused")
	}
}

func TestGitTagCreateListDelete(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "a\n", "base")
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	if res := gitRun(t, r, env, map[string]any{"action": "tag", "sub_action": "create", "name": "v1", "message": "release one"}); res.IsError {
		t.Fatalf("tag create: %+v", res)
	}
	if typ := strings.TrimSpace(gitIn(t, dir, "cat-file", "-t", "v1")); typ != "tag" {
		t.Fatalf("message should make an annotated tag, got type %q", typ)
	}
	list := gitRun(t, r, env, map[string]any{"action": "tag"})
	if list.IsError || !strings.Contains(list.Output, "v1") {
		t.Fatalf("tag list: %+v", list)
	}
	if res := gitRun(t, r, env, map[string]any{"action": "tag", "sub_action": "delete", "name": "v1"}); res.IsError {
		t.Fatalf("tag delete: %+v", res)
	}
	if out := strings.TrimSpace(gitIn(t, dir, "tag", "--list")); out != "" {
		t.Fatalf("tag survived delete: %q", out)
	}
	if res := gitRun(t, r, env, map[string]any{"action": "tag", "sub_action": "create"}); !res.IsError {
		t.Fatal("tag create without name must be refused")
	}
	if res := gitRun(t, r, env, map[string]any{"action": "tag", "sub_action": "move", "name": "v2"}); !res.IsError {
		t.Fatal("unknown tag sub_action must be refused")
	}
}

func TestGitCherryPick(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "a\n", "base")
	gitIn(t, dir, "switch", "-q", "-c", "feature")
	pick := commitFile(t, dir, "f.txt", "f\n", "feature change")
	gitIn(t, dir, "switch", "-q", "main")
	r, _ := newTestRegistry(t, Options{})
	env := newTestEnv(dir, nil)

	if res := gitRun(t, r, env, map[string]any{"action": "cherry_pick", "ref": pick}); res.IsError {
		t.Fatalf("cherry_pick: %+v", res)
	}
	if subject := strings.TrimSpace(gitIn(t, dir, "log", "-1", "--format=%s")); subject != "feature change" {
		t.Fatalf("HEAD after cherry-pick = %q", subject)
	}
	if _, err := os.Stat(filepath.Join(dir, "f.txt")); err != nil {
		t.Fatalf("picked file missing: %v", err)
	}
	if res := gitRun(t, r, env, map[string]any{"action": "cherry_pick", "ref": "--continue"}); !res.IsError {
		t.Fatal("a ref that looks like a flag must be refused")
	}
	if res := gitRun(t, r, env, map[string]any{"action": "cherry_pick"}); !res.IsError {
		t.Fatal("cherry_pick without ref must be refused")
	}
}

func TestGitRebaseNonInteractive(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "a\n", "base")
	gitIn(t, dir, "switch", "-q", "-c", "feature")
	commitFile(t, dir, "f.txt", "f\n", "feature work")
	gitIn(t, dir, "switch", "-q", "main")
	commitFile(t, dir, "m.txt", "m\n", "main work")
	gitIn(t, dir, "switch", "-q", "feature")
	r, _ := newTestRegistry(t, Options{})
	host := approvingHost(tools.Deny)
	env := newTestEnv(dir, host)

	// Interactive forms are refused before anything runs or is asked.
	for _, args := range [][]string{{"-i"}, {"--interactive"}, {"-ri"}} {
		res := gitRun(t, r, env, map[string]any{"action": "rebase", "ref": "main", "args": args})
		if !res.IsError || !strings.Contains(res.Output, "interactive") {
			t.Fatalf("rebase %v must be refused: %+v", args, res)
		}
	}
	if host.approvalCount() != 0 {
		t.Fatalf("refused rebase must not ask: %d approvals", host.approvalCount())
	}

	if res := gitRun(t, r, env, map[string]any{"action": "rebase", "ref": "main"}); !res.IsError {
		t.Fatal("denied rebase must fail")
	}
	before := head(t, dir)

	host.decision = tools.AllowOnce
	if res := gitRun(t, r, env, map[string]any{"action": "rebase", "ref": "main"}); res.IsError {
		t.Fatalf("approved rebase: %+v", res)
	}
	if head(t, dir) == before {
		t.Fatal("rebase did not rewrite the feature branch")
	}
	if err := runGit(dir, "merge-base", "--is-ancestor", "main", "feature"); err != nil {
		t.Fatal("feature should now sit on top of main")
	}
	if host.approvalCount() != 2 || !host.approvals[1].Danger {
		t.Fatalf("rebase should ask once with Danger: %+v", host.approvals)
	}
}

func TestGitRebaseConflictThenAbortNeedsNoApproval(t *testing.T) {
	dir := initRepo(t)
	commitFile(t, dir, "a.txt", "base\n", "base")
	gitIn(t, dir, "switch", "-q", "-c", "feature")
	commitFile(t, dir, "a.txt", "feature side\n", "feature edit")
	gitIn(t, dir, "switch", "-q", "main")
	commitFile(t, dir, "a.txt", "main side\n", "main edit")
	gitIn(t, dir, "switch", "-q", "feature")
	before := head(t, dir)

	r, _ := newTestRegistry(t, Options{})
	host := approvingHost(tools.AllowOnce)
	env := newTestEnv(dir, host)
	conflict := gitRun(t, r, env, map[string]any{"action": "rebase", "ref": "main"})
	if !conflict.IsError {
		t.Fatalf("conflicting rebase should stop with an error: %+v", conflict)
	}
	asked := host.approvalCount()

	abort := gitRun(t, r, env, map[string]any{"action": "rebase", "sub_action": "abort"})
	if abort.IsError {
		t.Fatalf("rebase abort: %+v", abort)
	}
	if host.approvalCount() != asked {
		t.Fatal("rebase abort must not ask")
	}
	if head(t, dir) != before {
		t.Fatal("abort did not restore the feature branch")
	}
	if status := strings.TrimSpace(gitIn(t, dir, "status", "--porcelain")); status != "" {
		t.Fatalf("tree not clean after abort: %q", status)
	}
}

func TestGitDangerClassification(t *testing.T) {
	cases := []struct {
		action, sub string
		argv        []string
		danger      bool
	}{
		{"reset", "", []string{"reset", "--hard", "HEAD~1"}, true},
		{"reset", "", []string{"reset", "--mixed"}, false},
		{"reset", "", []string{"reset", "-q", "--", "a"}, false},
		{"push", "", []string{"push", "--force-with-lease", "origin", "main"}, true},
		{"push", "", []string{"push", "-f", "origin", "main"}, true},
		{"push", "", []string{"push", "--delete", "origin", "topic"}, true},
		{"push", "", []string{"push", "origin", "main"}, false},
		{"pull", "", []string{"pull", "--rebase", "origin", "main"}, true},
		{"pull", "", []string{"pull", "origin", "main"}, false},
		{"rebase", "", []string{"rebase", "main"}, true},
		{"rebase", "continue", []string{"rebase", "--continue"}, true},
		{"rebase", "abort", []string{"rebase", "--abort"}, false},
		{"tag", "create", []string{"tag", "v1"}, false},
		{"cherry_pick", "", []string{"cherry-pick", "abc"}, false},
	}
	for _, c := range cases {
		if got := gitDanger(c.action, c.sub, c.argv) != ""; got != c.danger {
			t.Errorf("gitDanger(%s %s %v) danger=%v, want %v", c.action, c.sub, c.argv, got, c.danger)
		}
	}
}

func TestGitNewActionsReadOnlyClassification(t *testing.T) {
	tool := &gitTool{}
	for _, raw := range []string{
		`{"action":"push","remote":"origin","name":"main"}`,
		`{"action":"pull"}`,
		`{"action":"reset","mode":"hard"}`,
		`{"action":"restore","files":["a"]}`,
		`{"action":"unstage","all":true}`,
		`{"action":"tag","sub_action":"delete","name":"v1"}`,
		`{"action":"cherry_pick","ref":"abc"}`,
		`{"action":"rebase","ref":"main"}`,
	} {
		if tool.ReadOnlyCall(json.RawMessage(raw)) {
			t.Errorf("ReadOnlyCall(%s) = true, want false", raw)
		}
	}
}

// runGit runs git in dir and returns its error, for assertions.
func runGit(dir string, args ...string) error {
	ctx := context.Background()
	out, err := gitCapture(ctx, dir, args...)
	_ = out
	return err
}
