package witness

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
)

// newRepo makes a real git repository with one commit on main, using the local
// executor so Snapshot runs the same commands it runs on a target.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run(t, dir, "init", "-q", "-b", "main")
	run(t, dir, "config", "user.name", "Test")
	run(t, dir, "config", "user.email", "t@example.invalid")
	write(t, filepath.Join(dir, "app.txt"), "one\n")
	run(t, dir, "add", "app.txt")
	run(t, dir, "commit", "-q", "-m", "base")
	return dir
}

func run(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(args, " "), out, err)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snap(t *testing.T, dir string) Witness {
	t.Helper()
	w, err := Snapshot(context.Background(), executor.NewLocal(), dir)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return w
}

func TestSnapshotUnchangedHasNoDrift(t *testing.T) {
	dir := newRepo(t)
	a, b := snap(t, dir), snap(t, dir)
	if a.Branch != "main" || a.Head == "" || a.DiffHash == "" {
		t.Fatalf("snapshot fields: %+v", a)
	}
	if d := a.Drift(b); d != "" {
		t.Fatalf("no change should be no drift, got %q", d)
	}
}

func TestSnapshotNoUpstreamIsEmpty(t *testing.T) {
	dir := newRepo(t)
	if w := snap(t, dir); w.Upstream != "" {
		t.Fatalf("a local branch has no upstream, got %q", w.Upstream)
	}
}

func TestDriftHeadMoved(t *testing.T) {
	dir := newRepo(t)
	before := snap(t, dir)
	write(t, filepath.Join(dir, "app.txt"), "two\n")
	run(t, dir, "commit", "-q", "-am", "more")
	after := snap(t, dir)
	d := before.Drift(after)
	if !strings.HasPrefix(d, "HEAD moved ") || !strings.Contains(d, "→") {
		t.Fatalf("drift: %q", d)
	}
	if len(strings.Split(d, "\n")) != 1 {
		t.Fatalf("drift must be one line: %q", d)
	}
}

func TestDriftWorkingTreeChanged(t *testing.T) {
	dir := newRepo(t)
	before := snap(t, dir)
	write(t, filepath.Join(dir, "app.txt"), "edited\n")
	if d := before.Drift(snap(t, dir)); d != "working tree changed" {
		t.Fatalf("drift: %q", d)
	}
}

func TestDriftUntrackedContentChanged(t *testing.T) {
	// A new untracked file is not in `git diff`. Editing its contents must
	// still register, or another agent could rewrite it unseen.
	dir := newRepo(t)
	write(t, filepath.Join(dir, "new.txt"), "first\n")
	before := snap(t, dir)
	write(t, filepath.Join(dir, "new.txt"), "second\n")
	if d := before.Drift(snap(t, dir)); d != "working tree changed" {
		t.Fatalf("untracked edit should drift, got %q", d)
	}
}

func TestDriftBranchChanged(t *testing.T) {
	dir := newRepo(t)
	before := snap(t, dir)
	run(t, dir, "checkout", "-q", "-b", "other")
	if d := before.Drift(snap(t, dir)); !strings.HasPrefix(d, "branch changed main") {
		t.Fatalf("drift: %q", d)
	}
}

func TestSnapshotNotARepoIsError(t *testing.T) {
	if _, err := Snapshot(context.Background(), executor.NewLocal(), t.TempDir()); err == nil {
		t.Fatal("a directory that is not a git repository must not produce a witness")
	}
}

func TestIsEffectful(t *testing.T) {
	cases := []struct {
		name string
		tool string
		cmd  string
		want bool
	}{
		{"git push", "Bash", "git push origin main", true},
		{"git push with -C", "Bash", "git -C /w push", true},
		{"git merge", "Bash", "git merge feature", true},
		{"git rebase", "Bash", "git rebase main", true},
		{"git commit", "Bash", "git commit -m x", true},
		{"git status is read only", "Bash", "git status", false},
		{"git diff is read only", "Bash", "git diff HEAD", false},
		{"gh pr merge", "Bash", "gh pr merge 12 --squash", true},
		{"gh pr create", "Bash", "gh pr create --title x", true},
		{"gh release", "Bash", "gh release create v1", true},
		{"gh pr view is read only", "Bash", "gh pr view 12", false},
		{"npm publish", "Bash", "npm publish", true},
		{"npm test is read only", "Bash", "npm test", false},
		{"docker push", "Bash", "docker push ghcr.io/x/y", true},
		{"docker ps is read only", "Bash", "docker ps", false},
		{"systemctl restart", "Bash", "systemctl restart lectern", true},
		{"systemctl status is read only", "Bash", "systemctl status lectern", false},
		{"kubectl apply", "Bash", "kubectl apply -f x.yaml", true},
		{"kubectl get is read only", "Bash", "kubectl get pods", false},
		{"terraform apply", "Bash", "terraform apply -auto-approve", true},
		{"deploy script", "Bash", "./scripts/deploy.sh", true},
		{"make deploy", "Bash", "make deploy", true},
		{"after cd", "Bash", "cd /w && git push", true},
		{"after and chain", "Bash", "go test ./... && git push", true},
		{"after semicolon", "Bash", "go test ./...; git push", true},
		{"after pipe", "Bash", "cat x | docker push img", true},
		{"after sudo", "Bash", "sudo systemctl stop nginx", true},
		{"after env assignment", "Bash", "GIT_TERMINAL_PROMPT=0 git push", true},
		{"read only chain", "Bash", "git status && git log -1", false},
		{"quoted pattern in echo is not a push", "Bash", `echo "git push"`, false},
		{"deploy word anywhere", "Bash", "cat docs/deploy.md", true},
		{"not a shell tool", "Edit", "git push", false},
		{"empty command", "Bash", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := IsEffectful(c.tool, map[string]any{"command": c.cmd})
			if got != c.want {
				t.Fatalf("IsEffectful(%q, %q) = %v, want %v", c.tool, c.cmd, got, c.want)
			}
		})
	}
	if IsEffectful("Bash", map[string]any{}) {
		t.Fatal("a call without a command is not effectful")
	}
}
