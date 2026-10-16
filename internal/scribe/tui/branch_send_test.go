package tui

import "testing"

// branchBackend is the fake with three checkpoints and a recorded BranchFrom.
type branchBackend struct {
	*FakeBackend
	branched []int
}

func (b *branchBackend) Checkpoints() []CheckpointInfo {
	return []CheckpointInfo{{Turn: 1}, {Turn: 2}, {Turn: 3}}
}

func (b *branchBackend) BranchFrom(turn int) (int, error) {
	b.branched = append(b.branched, turn)
	return 3 - turn, nil
}

// Sending while an earlier checkpoint is shown asks first; Cancel keeps the
// text and branches nothing, Yes branches from the shown turn.
func TestSendFromEarlierCheckpointConfirmsAndBranches(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	b := &branchBackend{FakeBackend: NewFakeBackend()}
	tb := m.cur()
	tb.be = b
	m.syncRail(tb)
	tb.browse = 0

	m.submit("try another way")
	c, ok := m.topModal().(*Confirm)
	if !ok {
		t.Fatalf("expected a confirmation, got %T", m.topModal())
	}
	c.onNo()
	m.closeModal(c)
	if len(b.branched) != 0 || tb.in.value() != "try another way" {
		t.Fatalf("cancel: branched %v, input %q", b.branched, tb.in.value())
	}

	tb.in.clear()
	tb.browse = 0
	m.submit("try another way")
	c = m.topModal().(*Confirm)
	c.onYes()
	if len(b.branched) != 1 || b.branched[0] != 1 {
		t.Fatalf("yes should branch from turn 1, got %v", b.branched)
	}
	if tb.browse != -1 {
		t.Fatal("after sending, the view should be live again")
	}
}
