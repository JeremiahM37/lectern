package session

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cpEnv is a project with one session and its checkpoints.
type cpEnv struct {
	t    *testing.T
	root string
	cp   *Checkpoints
}

func newCP(t *testing.T) *cpEnv {
	t.Helper()
	s, root := setup(t)
	m := newSession(t, s, "checkpoints")
	cp, err := NewCheckpoints(s, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &cpEnv{t: t, root: root, cp: cp}
}

// write creates or overwrites a file under the project root, creating dirs.
func (e *cpEnv) write(rel string, data []byte, perm fs.FileMode) string {
	e.t.Helper()
	abs := filepath.Join(e.root, rel)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(abs, data, perm); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chmod(abs, perm); err != nil {
		e.t.Fatal(err)
	}
	return abs
}

// turn runs one agent turn: before is called for each path, then mutate.
func (e *cpEnv) turn(n int, before []string, mutate func()) {
	e.t.Helper()
	e.cp.Begin(n, "turn prompt")
	for _, p := range before {
		e.cp.BeforeWrite(filepath.Join(e.root, p))
	}
	mutate()
	if err := e.cp.End(); err != nil {
		e.t.Fatalf("End: %v", err)
	}
}

func (e *cpEnv) read(rel string) []byte {
	e.t.Helper()
	b, err := os.ReadFile(filepath.Join(e.root, rel))
	if err != nil {
		e.t.Fatalf("read %s: %v", rel, err)
	}
	return b
}

func mode(t *testing.T, path string) fs.FileMode {
	t.Helper()
	st, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return st.Mode().Perm()
}

func TestUndoRedoExactBytesBinaryAndMode(t *testing.T) {
	e := newCP(t)
	binary := []byte{0x00, 0xff, 0xfe, '\r', '\n', 0x80, 0x00, 0x1b, '[', '0', 'm', 0xc3}
	text := []byte("line one\r\nline two (no newline)")
	binPath := e.write("bin/data.bin", binary, 0o640)
	e.write("notes.txt", text, 0o600)

	e.turn(1, []string{"bin/data.bin", "notes.txt"}, func() {
		// The turn rewrites the binary with other bytes and changes the mode.
		if err := os.WriteFile(binPath, []byte{0x01, 0x00, 0x02}, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(binPath, 0o755); err != nil {
			t.Fatal(err)
		}
		e.write("notes.txt", []byte("changed\n"), 0o600)
	})

	turns, err := e.cp.Undo(false)
	if err != nil || len(turns) != 1 || turns[0] != 1 {
		t.Fatalf("Undo: %v %v", turns, err)
	}
	if got := e.read("bin/data.bin"); !bytes.Equal(got, binary) {
		t.Fatalf("binary not restored byte-for-byte: got %x want %x", got, binary)
	}
	if mode(t, binPath) != 0o640 {
		t.Fatalf("mode not restored: %v", mode(t, binPath))
	}
	if got := e.read("notes.txt"); !bytes.Equal(got, text) {
		t.Fatalf("text not restored: %q", got)
	}

	if _, err := e.cp.Redo(false); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if got := e.read("bin/data.bin"); !bytes.Equal(got, []byte{0x01, 0x00, 0x02}) {
		t.Fatalf("redo did not restore the turn's bytes: %x", got)
	}
	if mode(t, binPath) != 0o755 {
		t.Fatalf("redo did not restore mode: %v", mode(t, binPath))
	}
	if got := e.read("notes.txt"); string(got) != "changed\n" {
		t.Fatalf("redo text = %q", got)
	}
}

func TestCreatedFileDeletedOnUndo(t *testing.T) {
	e := newCP(t)
	created := filepath.Join(e.root, "new/dir/fresh.go")
	e.turn(1, []string{"new/dir/fresh.go"}, func() {
		if err := os.MkdirAll(filepath.Dir(created), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(created, []byte("package fresh\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	})
	if _, err := e.cp.Undo(false); err != nil {
		t.Fatalf("Undo: %v", err)
	}
	if _, err := os.Stat(created); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file created by the turn should be gone, stat err = %v", err)
	}
	if _, err := e.cp.Redo(false); err != nil {
		t.Fatalf("Redo: %v", err)
	}
	if got := e.read("new/dir/fresh.go"); string(got) != "package fresh\n" {
		t.Fatalf("redo did not recreate file: %q", got)
	}
}

func TestSecondWriteInTurnKeepsOriginal(t *testing.T) {
	e := newCP(t)
	e.write("a.txt", []byte("v0"), 0o644)
	e.cp.Begin(1, "p")
	p := filepath.Join(e.root, "a.txt")
	e.cp.BeforeWrite(p)
	os.WriteFile(p, []byte("v1"), 0o644)
	e.cp.BeforeWrite(p) // second edit in the same turn: must not re-snapshot v1
	os.WriteFile(p, []byte("v2"), 0o644)
	if err := e.cp.End(); err != nil {
		t.Fatal(err)
	}
	if _, err := e.cp.Undo(false); err != nil {
		t.Fatal(err)
	}
	if got := e.read("a.txt"); string(got) != "v0" {
		t.Fatalf("undo restored %q, want original v0", got)
	}
}

func TestRestoreToRewindsSeveralTurns(t *testing.T) {
	e := newCP(t)
	e.write("a.txt", []byte("v0"), 0o644)
	for i := 1; i <= 3; i++ {
		e.turn(i, []string{"a.txt"}, func() { e.write("a.txt", []byte{'v', byte('0' + i)}, 0o644) })
	}
	turns, err := e.cp.RestoreTo(1, false)
	if err != nil {
		t.Fatalf("RestoreTo: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("RestoreTo(1) reverted %v, want turns 3 and 2", turns)
	}
	if got := e.read("a.txt"); string(got) != "v1" {
		t.Fatalf("after RestoreTo(1) file = %q, want v1", got)
	}
	list := e.cp.List()
	if len(list) != 3 || list[0].Undone || !list[1].Undone || !list[2].Undone {
		t.Fatalf("List after RestoreTo: %+v", list)
	}
	if _, err := e.cp.Redo(false); err != nil {
		t.Fatal(err)
	}
	if got := e.read("a.txt"); string(got) != "v3" {
		t.Fatalf("redo of RestoreTo = %q, want v3", got)
	}
	if _, err := e.cp.RestoreTo(10, false); err != nil {
		t.Fatalf("nothing after 10 should be a no-op: %v", err)
	}
}

func TestUndoConflictRefusesThenForce(t *testing.T) {
	e := newCP(t)
	e.write("a.txt", []byte("before"), 0o644)
	e.turn(1, []string{"a.txt"}, func() { e.write("a.txt", []byte("agent"), 0o644) })

	// The user edits the file after the turn.
	e.write("a.txt", []byte("user edit"), 0o644)

	_, err := e.cp.Undo(false)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("want ErrConflict, got %v", err)
	}
	var ce *ConflictError
	if !errors.As(err, &ce) || len(ce.Files) != 1 || ce.Files[0] != "a.txt" {
		t.Fatalf("conflict should name a.txt: %+v", ce)
	}
	if got := e.read("a.txt"); string(got) != "user edit" {
		t.Fatalf("refused undo still changed the file: %q", got)
	}
	if _, err := e.cp.Undo(true); err != nil {
		t.Fatalf("forced undo: %v", err)
	}
	if got := e.read("a.txt"); string(got) != "before" {
		t.Fatalf("forced undo = %q", got)
	}
}

func TestRedoConflict(t *testing.T) {
	e := newCP(t)
	e.write("a.txt", []byte("before"), 0o644)
	e.turn(1, []string{"a.txt"}, func() { e.write("a.txt", []byte("agent"), 0o644) })
	if _, err := e.cp.Undo(false); err != nil {
		t.Fatal(err)
	}
	e.write("a.txt", []byte("someone else"), 0o644)
	if _, err := e.cp.Redo(false); !errors.Is(err, ErrConflict) {
		t.Fatalf("redo over an external edit: want ErrConflict, got %v", err)
	}
	if got := e.read("a.txt"); string(got) != "someone else" {
		t.Fatalf("refused redo changed the file: %q", got)
	}
}

func TestEmptyTurnLeavesNoCheckpoint(t *testing.T) {
	e := newCP(t)
	e.turn(1, nil, func() {})
	if list := e.cp.List(); len(list) != 0 {
		t.Fatalf("read-only turn should not be a checkpoint: %+v", list)
	}
	if _, err := e.cp.Undo(false); !errors.Is(err, ErrNothingToUndo) {
		t.Fatalf("undo with nothing: %v", err)
	}
	if _, err := e.cp.Redo(false); !errors.Is(err, ErrNothingToRedo) {
		t.Fatalf("redo with nothing: %v", err)
	}
}

func TestDiffOfTurn(t *testing.T) {
	e := newCP(t)
	e.write("a.txt", []byte("one\ntwo\nthree\n"), 0o644)
	e.turn(7, []string{"a.txt"}, func() { e.write("a.txt", []byte("one\n2\nthree\nfour\n"), 0o644) })
	d, err := e.cp.Diff(7)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"--- a/a.txt", "+++ b/a.txt", "-two", "+2", "+four"} {
		if !strings.Contains(d, want) {
			t.Errorf("diff missing %q:\n%s", want, d)
		}
	}
	if _, err := e.cp.Diff(99); err == nil {
		t.Fatal("diff of unknown turn should fail")
	}
}

func TestDiffBinaryAndCreated(t *testing.T) {
	e := newCP(t)
	e.write("img.bin", []byte{0, 1, 2}, 0o644)
	e.turn(1, []string{"img.bin", "made.txt"}, func() {
		e.write("img.bin", []byte{0, 9}, 0o644)
		e.write("made.txt", []byte("hello\n"), 0o644)
	})
	d, err := e.cp.Diff(1)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(d, "Binary files a/img.bin and b/img.bin differ") {
		t.Errorf("binary not reported:\n%s", d)
	}
	if !strings.Contains(d, "--- /dev/null\n+++ b/made.txt") || !strings.Contains(d, "+hello") {
		t.Errorf("created file diff wrong:\n%s", d)
	}
}

func TestStaleSinceReadAndWasRead(t *testing.T) {
	e := newCP(t)
	abs := e.write("a.txt", []byte("hello"), 0o644)
	if e.cp.WasRead(abs) {
		t.Fatal("never read yet")
	}
	if e.cp.StaleSinceRead(abs) {
		t.Fatal("a file never read is not stale")
	}
	e.cp.MarkRead(abs)
	if !e.cp.WasRead(abs) || e.cp.StaleSinceRead(abs) {
		t.Fatal("just read: should be read and fresh")
	}

	// Same size, different content, mtime moved forward: stale.
	if err := os.WriteFile(abs, []byte("HELLO"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(5 * time.Second)
	if err := os.Chtimes(abs, later, later); err != nil {
		t.Fatal(err)
	}
	if !e.cp.StaleSinceRead(abs) {
		t.Fatal("changed on disk after read should be stale")
	}
	e.cp.MarkRead(abs)
	if e.cp.StaleSinceRead(abs) {
		t.Fatal("re-read should clear staleness")
	}

	// Deleted after the read counts as stale.
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	if !e.cp.StaleSinceRead(abs) {
		t.Fatal("deleted after read should be stale")
	}
}

func TestReadStatePersistsAcrossReload(t *testing.T) {
	s, root := setup(t)
	m := newSession(t, s, "reload")
	cp, err := NewCheckpoints(s, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, "r.txt")
	os.WriteFile(abs, []byte("x"), 0o644)
	cp.MarkRead(abs)
	again, err := NewCheckpoints(s, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.WasRead(abs) || again.StaleSinceRead(abs) {
		t.Fatal("read record should survive a reload (resume)")
	}
}

func TestOpenTurnRecoveredAfterCrash(t *testing.T) {
	s, root := setup(t)
	m := newSession(t, s, "crash")
	cp, err := NewCheckpoints(s, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, "c.txt")
	os.WriteFile(abs, []byte("original"), 0o644)
	cp.Begin(4, "about to crash")
	cp.BeforeWrite(abs)
	os.WriteFile(abs, []byte("half done"), 0o644)
	// No End: the process dies here. A new process loads the store.

	reloaded, err := NewCheckpoints(s, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if list := reloaded.List(); len(list) != 1 || list[0].Turn != 4 {
		t.Fatalf("open turn should be recovered: %+v", list)
	}
	if _, err := reloaded.Undo(false); err != nil {
		t.Fatalf("Undo after crash: %v", err)
	}
	if got, _ := os.ReadFile(abs); string(got) != "original" {
		t.Fatalf("after crash undo = %q", got)
	}
}

func TestCorruptCheckpointsFileIsSetAside(t *testing.T) {
	s, _ := setup(t)
	m := newSession(t, s, "corrupt")
	path := filepath.Join(s.Dir(m.ID), "checkpoints.json")
	os.WriteFile(path, []byte("{broken"), 0o600)
	cp, err := NewCheckpoints(s, m.ID)
	if err == nil || cp == nil {
		t.Fatalf("corrupt file should report an error and still return a usable value: %v", err)
	}
	if _, err := os.Stat(path + ".corrupt"); err != nil {
		t.Fatalf("corrupt file not kept aside: %v", err)
	}
	if len(cp.List()) != 0 {
		t.Fatal("corrupt state should start empty")
	}
}

func TestBlobsAreContentAddressed(t *testing.T) {
	e := newCP(t)
	e.write("same.txt", []byte("identical"), 0o644)
	e.write("twin.txt", []byte("identical"), 0o644)
	e.turn(1, []string{"same.txt", "twin.txt"}, func() {
		e.write("same.txt", []byte("x"), 0o644)
		e.write("twin.txt", []byte("y"), 0o644)
	})
	blobs, err := os.ReadDir(filepath.Join(e.cp.dir, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	// original "identical", "x", "y" → three blobs, not four.
	if len(blobs) != 3 {
		t.Fatalf("blob count = %d, want 3", len(blobs))
	}
}

// TestGitIndexUntouched is the regression test for the git-tag design
// defect: checkpoints must never change the user's staged changes.
func TestGitIndexUntouched(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	e := newCP(t)
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = e.root
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return string(out)
	}
	git("init", "-q")
	// Projects ignore .scribe/ (the store lives there); without this the
	// session files would show up in the status comparison below.
	e.write(".gitignore", []byte(".scribe/\n"), 0o644)
	e.write("tracked.txt", []byte("committed\n"), 0o644)
	e.write("staged.txt", []byte("staged v1\n"), 0o644)
	git("add", ".")
	git("commit", "-q", "-m", "base")

	// The user stages one change and leaves another unstaged.
	e.write("staged.txt", []byte("staged v2\n"), 0o644)
	git("add", "staged.txt")
	e.write("staged.txt", []byte("staged v3, unstaged\n"), 0o644)
	e.write("tracked.txt", []byte("user edit\n"), 0o644)

	cachedBefore := git("diff", "--cached")
	statusBefore := git("status", "--porcelain")
	if !strings.Contains(cachedBefore, "staged v2") {
		t.Fatalf("setup: expected a staged change:\n%s", cachedBefore)
	}

	// The agent edits tracked.txt in a turn, then the turn is undone.
	e.turn(1, []string{"tracked.txt"}, func() { e.write("tracked.txt", []byte("agent\n"), 0o644) })
	if _, err := e.cp.Undo(true); err != nil {
		t.Fatal(err)
	}

	if got := git("diff", "--cached"); got != cachedBefore {
		t.Fatalf("git index changed by checkpoint undo.\nbefore:\n%s\nafter:\n%s", cachedBefore, got)
	}
	if got := git("status", "--porcelain"); got != statusBefore {
		t.Fatalf("git status changed.\nbefore:\n%s\nafter:\n%s", statusBefore, got)
	}
	if got := string(e.read("tracked.txt")); got != "user edit\n" {
		t.Fatalf("undo did not restore the user's pre-turn edit: %q", got)
	}
	if tags := git("tag", "--list", "soulforge*", "scribe*"); strings.TrimSpace(tags) != "" {
		t.Fatalf("checkpoints must not create tags: %s", tags)
	}
}
