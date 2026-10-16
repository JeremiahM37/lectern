package session

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCheckpointRecordsContextFillAndKeepsItAcrossReopen(t *testing.T) {
	s, root := setup(t)
	m := newSession(t, s, "ctx")
	cp, err := NewCheckpoints(s, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, "a.txt")
	if err := os.WriteFile(abs, []byte("one"), 0o644); err != nil {
		t.Fatal(err)
	}
	cp.Begin(1, "edit a")
	cp.BeforeWrite(abs)
	if err := os.WriteFile(abs, []byte("two"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := cp.End(); err != nil {
		t.Fatal(err)
	}
	if err := cp.SetContext(1, 84000, 200000); err != nil {
		t.Fatal(err)
	}
	if got := cp.List(); len(got) != 1 || got[0].CtxUsed != 84000 || got[0].CtxWindow != 200000 {
		t.Fatalf("List = %+v, want the fill recorded on turn 1", got)
	}
	re, err := NewCheckpoints(s, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got := re.List(); len(got) != 1 || got[0].CtxUsed != 84000 || got[0].CtxWindow != 200000 {
		t.Fatalf("after reopen List = %+v, want the fill kept", got)
	}
	// A turn with no checkpoint, or no window, is a no-op rather than an error.
	if err := cp.SetContext(9, 1, 2); err != nil {
		t.Fatalf("turn without a checkpoint: %v", err)
	}
	if err := cp.SetContext(1, 5, 0); err != nil {
		t.Fatal(err)
	}
	if got := cp.List(); got[0].CtxUsed != 84000 {
		t.Fatalf("a zero window must not overwrite the record: %+v", got)
	}
}
