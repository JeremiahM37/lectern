package broker

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/store"
	"github.com/JeremiahM37/lectern/v2/internal/witness"
)

// fakeTree stands in for a worktree: the test moves it between the request and
// the decision, which is exactly the window the witness exists to cover.
type fakeTree struct {
	state witness.Witness
	err   error
	calls int
}

func (f *fakeTree) read(*store.Approval) (*witness.Witness, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	w := f.state
	return &w, nil
}

func newTree() *fakeTree {
	return &fakeTree{state: witness.Witness{Branch: "main", Head: "1a2b3c4d", DiffHash: "clean"}}
}

func witnessed(t *testing.T, tree *fakeTree) (*Broker, int64) {
	t.Helper()
	br, _, attemptID, _ := newBroker(t)
	br.Witness = tree.read
	id, err := br.Create(attemptID, "Bash", map[string]any{"command": "git push origin main"}, false)
	if err != nil {
		t.Fatal(err)
	}
	return br, id
}

func TestApproveAfterHeadMovedIsStale(t *testing.T) {
	tree := newTree()
	br, id := witnessed(t, tree)
	tree.state.Head = "5d6e7f8a"

	row := br.Decide(id, "approved", "", "user")
	if row == nil || row.Status != "stale" {
		t.Fatalf("approving drifted state must be stale, got %+v", row)
	}
	if row.DecidedBy != "witness" || !strings.HasPrefix(row.Note, "state changed since this was requested: HEAD moved 1a2b3c4 → 5d6e7f8") {
		t.Fatalf("decided_by/note: %q / %q", row.DecidedBy, row.Note)
	}
	// The blocked hook is released with the stale status, not left waiting.
	got := br.WaitOnce(context.Background(), id, time.Second)
	if got == nil || got.Status != "stale" {
		t.Fatalf("waiting hook should receive stale, got %+v", got)
	}
}

func TestApproveWithUnchangedWitnessIsApproved(t *testing.T) {
	tree := newTree()
	br, id := witnessed(t, tree)
	row := br.Decide(id, "approved", "", "user")
	if row == nil || row.Status != "approved" || row.DecidedBy != "user" {
		t.Fatalf("unchanged state should approve, got %+v", row)
	}
	if tree.calls < 2 {
		t.Fatalf("the state should be re-read at the decision, read %d times", tree.calls)
	}
}

func TestDenyIsNeverBlockedByDrift(t *testing.T) {
	tree := newTree()
	br, id := witnessed(t, tree)
	tree.state.DiffHash = "edited"
	row := br.Decide(id, "denied", "not now", "user")
	if row == nil || row.Status != "denied" || row.Note != "not now" {
		t.Fatalf("a deny must stand whatever the state, got %+v", row)
	}
	if tree.calls != 1 {
		t.Fatalf("a deny should not re-read the worktree, read %d times", tree.calls)
	}
}

func TestWorkingTreeEditMakesApprovalStale(t *testing.T) {
	tree := newTree()
	br, id := witnessed(t, tree)
	tree.state.DiffHash = "edited"
	row := br.Decide(id, "approved", "", "user")
	if row == nil || row.Status != "stale" || !strings.HasSuffix(row.Note, "working tree changed; re-run the command to ask again") {
		t.Fatalf("working tree edit: %+v", row)
	}
}

func TestRecheckFailureFailsClosed(t *testing.T) {
	tree := newTree()
	br, id := witnessed(t, tree)
	tree.err = errors.New("ssh timeout")
	row := br.Decide(id, "approved", "", "user")
	if row == nil || row.Status != "stale" {
		t.Fatalf("an approval that cannot be re-checked must not go through, got %+v", row)
	}
}

func TestNonEffectfulCallIsNotSnapshotted(t *testing.T) {
	tree := newTree()
	br, _, attemptID, _ := newBroker(t)
	br.Witness = tree.read
	id, err := br.Create(attemptID, "Bash", map[string]any{"command": "git status"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if tree.calls != 0 {
		t.Fatalf("a read-only command must not be snapshotted, read %d times", tree.calls)
	}
	row, _ := br.DB.Approval(id)
	if row.WitnessJSON != "" {
		t.Fatalf("no witness expected, got %q", row.WitnessJSON)
	}
	if got := br.Decide(id, "approved", "", "user"); got == nil || got.Status != "approved" {
		t.Fatalf("approval without a witness should stand: %+v", got)
	}
}

func TestSnapshotFailureMeansNoWitnessNotAnError(t *testing.T) {
	tree := &fakeTree{err: errors.New("not a git repository")}
	br, _, attemptID, _ := newBroker(t)
	br.Witness = tree.read
	id, err := br.Create(attemptID, "Bash", map[string]any{"command": "git push"}, false)
	if err != nil || id == 0 {
		t.Fatalf("a failed snapshot must not fail the request: id=%d err=%v", id, err)
	}
	if got := br.Decide(id, "approved", "", "user"); got == nil || got.Status != "approved" {
		t.Fatalf("no witness means no re-check: %+v", got)
	}
}

func TestFeatureOffWithoutWitnessFunc(t *testing.T) {
	br, _, attemptID, _ := newBroker(t)
	id, err := br.Create(attemptID, "Bash", map[string]any{"command": "git push"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if got := br.Decide(id, "approved", "", "user"); got == nil || got.Status != "approved" {
		t.Fatalf("with the feature off approvals behave as before: %+v", got)
	}
}

func TestQuietPolicyApprovalIsNotSnapshotted(t *testing.T) {
	tree := newTree()
	br, _, attemptID, _ := newBroker(t)
	br.Witness = tree.read
	if _, err := br.Create(attemptID, "Bash", map[string]any{"command": "git push"}, true); err != nil {
		t.Fatal(err)
	}
	if tree.calls != 0 {
		t.Fatalf("a policy auto-approval has no human-visible state, read %d times", tree.calls)
	}
}
