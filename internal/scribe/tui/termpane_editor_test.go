package tui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/nvim"
)

func TestFitEditorNameKeepsTheFileName(t *testing.T) {
	long := "/tmp/claude-1000/-home-admin-projects-lectern/a05bd330/scratchpad/sample.txt"
	for _, max := range []int{4, 10, 20, 40} {
		got := fitEditorName(long, max)
		if w := ansi.StringWidth(got); w > max {
			t.Fatalf("max %d: %q is %d cells", max, got, w)
		}
		if max >= 11 && !strings.HasSuffix(got, "sample.txt") {
			t.Fatalf("max %d: %q lost the file name", max, got)
		}
		if !strings.HasPrefix(got, "…") {
			t.Fatalf("max %d: %q does not mark the cut", max, got)
		}
	}
}

func TestFitEditorNameLeavesShortNamesAlone(t *testing.T) {
	for _, name := range []string{"notes.txt", "(no file)", "dir/é.go"} {
		if got := fitEditorName(name, 40); got != name {
			t.Fatalf("%q changed to %q", name, got)
		}
	}
	if got := fitEditorName("abcdef", 1); got != "…" {
		t.Fatalf("max 1 gave %q", got)
	}
}

func TestEditorTitleShowsFileNameWhenLong(t *testing.T) {
	skipWithoutPTY(t)
	t.Setenv("SCRIBE_EDITOR", "cat")
	m, _ := newTestModel(t, 80, 30)
	defer m.closePanes()
	m.runCommand("/editor")
	ed := m.ps().editor
	if ed == nil {
		t.Fatal("editor did not open")
	}
	ed.name = "some/very/deeply/nested/directory/structure/for/the/editor/title/readme-final.md"
	if !strings.Contains(screenText(m), "…e/for/the/editor/title/readme-final.md") {
		t.Fatalf("long title lost the file name:\n%s", screenText(m))
	}
}

func TestSplitFileLine(t *testing.T) {
	cases := []struct {
		in   string
		file string
		line int
	}{
		{"main.go", "main.go", 0},
		{"main.go:42", "main.go", 42},
		{"internal/x/y.go:7:3", "internal/x/y.go", 7},
		{"  notes.md:1  ", "notes.md", 1},
		{"weird:name", "weird:name", 0},
		{"main.go:0", "main.go:0", 0},
		{":12", ":12", 0},
		{"dir:12/file.txt", "dir:12/file.txt", 0},
	}
	for _, c := range cases {
		file, line := splitFileLine(c.in)
		if file != c.file || line != c.line {
			t.Fatalf("splitFileLine(%q) = %q, %d; want %q, %d", c.in, file, line, c.file, c.line)
		}
	}
}

func TestEditorCommandPutsLineBeforeFile(t *testing.T) {
	t.Setenv("SCRIBE_EDITOR", "cat")
	argv, err := editorCommandAt("f.txt", 7)
	if err != nil {
		t.Fatal(err)
	}
	if len(argv) != 3 || argv[1] != "+7" || argv[2] != "f.txt" {
		t.Fatalf("argv %v", argv)
	}
	argv, _ = editorCommandAt("", 0)
	if len(argv) != 1 {
		t.Fatalf("no file and no line should give the bare editor: %v", argv)
	}
}

func TestEditorAccessCommand(t *testing.T) {
	prev := nvim.GetAccess()
	t.Cleanup(func() { nvim.SetAccess(prev) })
	m, _ := newTestModel(t, 80, 30)
	m.runCommand("/editor access when-open")
	if nvim.GetAccess() != nvim.AccessWhenOpen {
		t.Fatalf("access %v after /editor access when-open", nvim.GetAccess())
	}
	m.runCommand("/editor access never")
	if nvim.GetAccess() != nvim.AccessOff {
		t.Fatalf("access %v after /editor access never", nvim.GetAccess())
	}
	m.runCommand("/editor access sometimes")
	if nvim.GetAccess() != nvim.AccessOff {
		t.Fatal("a bad value changed the access")
	}
}

func TestEditorOpensNeovimAtLineAndAgentCanReachIt(t *testing.T) {
	skipWithoutPTY(t)
	if _, err := exec.LookPath("nvim"); err != nil {
		t.Skip("nvim is not installed")
	}
	dir := t.TempDir()
	file := filepath.Join(dir, "lines.txt")
	if err := os.WriteFile(file, []byte("a\nb\nc\nd\ne\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SCRIBE_EDITOR", "nvim -u NONE -i NONE")
	m, _ := newTestModel(t, 120, 30)
	defer m.closePanes()
	m.runCommand("/editor " + file + ":4")
	ed := m.ps().editor
	if ed == nil || ed.sess == nil {
		t.Fatalf("editor did not open as a Neovim session: %+v", ed)
	}
	if cur := nvim.Current(); cur != ed.sess {
		t.Fatal("the open editor is not the current session")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	deadline := time.Now().Add(15 * time.Second)
	var line any
	for time.Now().Before(deadline) {
		v, err := ed.sess.Call(ctx, "nvim_eval", "line('.')")
		if err == nil {
			line = v
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if line != int64(4) {
		t.Fatalf("cursor on line %v, want 4", line)
	}

	m.closeEditor()
	if nvim.Current() != nil {
		t.Fatal("closing the editor left a current session")
	}
	if ed.sess.Closed() == false {
		t.Fatal("the session was not closed")
	}
}

func TestEditorSettingIsUsedBeforeVisualAndEditor(t *testing.T) {
	t.Setenv("SCRIBE_EDITOR", "")
	t.Setenv("VISUAL", "vi")
	t.Setenv("EDITOR", "vi")
	setEditorCommand("sh", []string{"-c", "true"})
	defer setEditorCommand("", nil)
	argv, err := editorCommandAt("f.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if argv[0] != "sh" || argv[len(argv)-1] != "f.txt" {
		t.Fatalf("argv = %v, want the configured command first", argv)
	}
}
