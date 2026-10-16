package session

import (
	"path/filepath"
	"strings"
	"testing"
)

// Steering sent during a turn is folded into that turn's checkpoint prompt,
// and steering outside a turn is ignored.
func TestFoldSteeringIntoRunningCheckpoint(t *testing.T) {
	e := newCP(t)
	e.cp.Fold("ignored, no turn running")
	e.cp.Begin(1, "rename the handler")
	e.cp.Fold("  also update the docs  ")
	e.write("docs.md", []byte("x"), 0o644)
	abs := filepath.Join(e.root, "docs.md")
	e.cp.BeforeWrite(abs)
	e.write("docs.md", []byte("y"), 0o644)
	if err := e.cp.End(); err != nil {
		t.Fatal(err)
	}
	cps := e.cp.List()
	if len(cps) != 1 {
		t.Fatalf("want one checkpoint, got %d", len(cps))
	}
	if want := "rename the handler\nalso update the docs"; cps[0].Prompt != want {
		t.Fatalf("prompt = %q, want %q", cps[0].Prompt, want)
	}
	if strings.Contains(cps[0].Prompt, "ignored") {
		t.Fatal("steering outside a turn leaked into a checkpoint")
	}
}

// Steering is written with the open turn, so a crash keeps it.
func TestFoldSurvivesReload(t *testing.T) {
	s, root := setup(t)
	m := newSession(t, s, "fold-reload")
	cp, err := NewCheckpoints(s, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	cp.Begin(1, "start")
	cp.Fold("more")
	cp.BeforeWrite(filepath.Join(root, "f.txt"))
	again, err := NewCheckpoints(s, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	cps := again.List()
	if len(cps) == 0 || cps[len(cps)-1].Prompt != "start\nmore" {
		t.Fatalf("after reload: %+v", cps)
	}
}
