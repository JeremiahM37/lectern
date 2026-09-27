package trackers

import (
	"context"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/executor"
	"github.com/JeremiahM37/lectern/v2/internal/testutil"
)

// TestConflictingFilesRealGit runs the real merge-tree script through a
// real local executor against a bare "origin" that carries a GitHub-style
// refs/pull/N/head, the way a forge does.
func TestConflictingFilesRealGit(t *testing.T) {
	testutil.RequireIsolated(t)
	dir := t.TempDir()
	origin, clone := filepath.Join(dir, "origin.git"), filepath.Join(dir, "clone")
	script := `set -e
g() { git -c user.name=t -c user.email=t@example.invalid -c init.defaultBranch=main "$@"; }
g init -q --bare "$ORIGIN"
g init -q "$CLONE"; cd "$CLONE"
g remote add origin "$ORIGIN"
printf 'one\ntwo\n' > a.txt; printf 'keep\n' > b.txt
g add . && g commit -qm base && g push -q origin HEAD:refs/heads/main
g checkout -qb feature
printf 'one\nFEATURE\n' > a.txt; printf 'keep\nmore\n' > b.txt
g commit -qam feature
g push -q origin HEAD:refs/pull/5/head
g checkout -q main
printf 'one\nMAIN\n' > a.txt
g commit -qam main && g push -q origin main
g reset -q --hard HEAD~1
`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(cmd.Environ(), "ORIGIN="+origin, "CLONE="+clone)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("setup: %v\n%s", err, out)
	}
	c := ConflictingFiles(context.Background(), executor.NewLocal(), clone, "refs/heads/main", "refs/pull/5/head")
	if !c.Checked || !reflect.DeepEqual(c.Files, []string{"a.txt"}) {
		t.Fatalf("conflicts = %+v", c)
	}
	// the clone's own branch was not moved and nothing was checked out
	out, _ := exec.Command("git", "-C", clone, "status", "--porcelain").Output()
	if len(out) != 0 {
		t.Fatalf("working tree touched: %s", out)
	}
	if c := ConflictingFiles(context.Background(), executor.NewLocal(), clone, "refs/heads/main", "refs/pull/99/head"); c.Checked || c.Detail == "" {
		t.Fatalf("missing PR ref = %+v", c)
	}
}
