package tui

import (
	"strings"
	"testing"
)

// markDirty makes the fake report paths as changed in git, and updates the
// tab's cached status the way the event pump would, so a test that edits a
// file sees it on the panel.
func markDirty(t *tab, paths ...string) {
	if fb, ok := t.be.(*FakeBackend); ok {
		fb.dirtyFiles = append(fb.dirtyFiles, paths...)
	}
	if t.git.Branch != "" {
		t.git.Changed = append(t.git.Changed, paths...)
	}
}

// A file a turn changed drops off the Changes panel once git no longer
// reports it (it was committed), and the changed-files count follows.
func TestChangedNowDropsCommittedFiles(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.phase = phaseChat
	editFile(m, "alpha.go", false)
	editFile(m, "bee.go", true)
	tb := m.cur()

	tb.git = GitStatus{Branch: "main", Changed: []string{"alpha.go", "bee.go"}}
	if c, mo := tb.changedNow(); len(c) != 1 || len(mo) != 1 {
		t.Fatalf("both files still dirty: created=%v modified=%v", c, mo)
	}

	// bee.go is committed; alpha.go is still dirty.
	tb.git = GitStatus{Branch: "main", Changed: []string{"alpha.go"}}
	c, mo := tb.changedNow()
	if len(c) != 0 || len(mo) != 1 || mo[0] != "alpha.go" {
		t.Fatalf("after the commit: created=%v modified=%v", c, mo)
	}
	if m.sideWidth() == 0 {
		t.Fatal("the panel should stay while alpha.go is still changed")
	}

	// Everything committed: the panel and the bar go away.
	tb.git = GitStatus{Branch: "main"}
	if c, mo := tb.changedNow(); len(c)+len(mo) != 0 {
		t.Fatalf("everything committed but still listed: %v %v", c, mo)
	}
	if m.sideWidth() != 0 {
		t.Fatal("the side panel should hide once nothing is changed")
	}
	if strings.Contains(plain(m.changedBar(tb)), "alpha.go") {
		t.Fatal("the bar still names a committed file")
	}
}

// Without git status (no repository, or not read yet) the transcript's list
// stands, so a non-git folder keeps its panel.
func TestChangedNowWithoutGitKeepsTheTranscript(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	m.phase = phaseChat
	editFile(m, "notes.txt", false)
	tb := m.cur()
	tb.git = GitStatus{}
	if c, mo := tb.changedNow(); len(mo) != 1 || mo[0] != "notes.txt" || len(c) != 0 {
		t.Fatalf("no git: created=%v modified=%v", c, mo)
	}
}
