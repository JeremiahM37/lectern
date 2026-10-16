package tui

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// screenText is the rendered screen with the colours removed.
func screenText(m *Model) string { return ansi.Strip(m.View()) }

// pollScreen waits until the rendered screen contains want.
func pollScreen(t *testing.T, m *Model, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(screenText(m), want) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("screen never showed %q:\n%s", want, screenText(m))
}

func skipWithoutPTY(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("pty tests run on unix")
	}
}

func TestFloatingTerminalOpenAndHide(t *testing.T) {
	skipWithoutPTY(t)
	t.Setenv("SHELL", "/bin/sh")
	m, fb := newTestModel(t, 120, 40)
	defer m.closePanes()
	fb.workdir = t.TempDir()

	m.runCommand("/terminal")
	if m.ps().shown != 0 {
		t.Fatalf("terminal not shown after /terminal (shown=%d)", m.ps().shown)
	}
	if !strings.Contains(screenText(m), "Terminal 1") {
		t.Fatalf("floating box not drawn:\n%s", screenText(m))
	}

	m.Update(key("esc"))
	if m.ps().shown != -1 {
		t.Fatal("Esc did not hide the terminal")
	}
	if strings.Contains(screenText(m), "Terminal 1") {
		t.Fatal("hidden terminal still drawn")
	}
	if exited, _ := m.ps().floats[0].pane.Exited(); exited {
		t.Fatal("hiding the terminal ended its shell")
	}

	m.runCommand("/terminal")
	if m.ps().shown != 0 || len(m.ps().floats) != 1 {
		t.Fatalf("/terminal did not show the same terminal again (shown=%d, n=%d)", m.ps().shown, len(m.ps().floats))
	}
}

func TestFloatingTerminalKeysReachShell(t *testing.T) {
	skipWithoutPTY(t)
	t.Setenv("SHELL", "/bin/sh")
	m, fb := newTestModel(t, 120, 40)
	defer m.closePanes()
	fb.workdir = t.TempDir()

	m.runCommand("/terminal")
	for _, r := range "echo tui$((1+2))" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m.Update(key("enter"))
	pollScreen(t, m, "tui3")
}

func TestFloatingTerminalLimitAndList(t *testing.T) {
	skipWithoutPTY(t)
	t.Setenv("SHELL", "/bin/sh")
	m, fb := newTestModel(t, 120, 40)
	defer m.closePanes()
	fb.workdir = t.TempDir()

	for i := 0; i < maxFloats; i++ {
		m.runCommand("/terminal new")
	}
	if n := len(m.ps().floats); n != maxFloats {
		t.Fatalf("opened %d terminals, want %d", n, maxFloats)
	}
	m.runCommand("/terminal new")
	if n := len(m.ps().floats); n != maxFloats {
		t.Fatalf("sixth terminal was started (%d)", n)
	}
	m.runCommand("/terminals")
	if m.topModal() == nil {
		t.Fatal("/terminals opened no list")
	}
}

func TestEditorMissingShowsInstallHints(t *testing.T) {
	skipWithoutPTY(t)
	for _, v := range []string{"SCRIBE_EDITOR", "VISUAL", "EDITOR"} {
		t.Setenv(v, "")
	}
	t.Setenv("PATH", t.TempDir()) // no editor on PATH
	m, fb := newTestModel(t, 120, 40)
	defer m.closePanes()
	fb.workdir = t.TempDir()

	m.runCommand("/editor")
	if m.ps().editor == nil || m.ps().editor.pane != nil {
		t.Fatal("expected the editor error state")
	}
	if !strings.Contains(screenText(m), "Neovim Not Found") {
		t.Fatalf("error state not drawn:\n%s", screenText(m))
	}
	m.Update(key("esc"))
	if m.ps().editor != nil {
		t.Fatal("Esc did not close the error pane")
	}
}

func TestEditorRunsOnFileAndExitReturnsFocus(t *testing.T) {
	skipWithoutPTY(t)
	t.Setenv("SCRIBE_EDITOR", "cat")
	dir := t.TempDir()
	file := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(file, []byte("editor-body-line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m, fb := newTestModel(t, 120, 40)
	defer m.closePanes()
	fb.workdir = dir

	m.runCommand("/editor notes.txt")
	ed := m.ps().editor
	if ed == nil || ed.pane == nil {
		t.Fatal("editor did not start")
	}
	if m.ps().focus != focusEditor {
		t.Fatal("new editor does not have the focus")
	}
	pollScreen(t, m, "editor-body-line")
	if ed.name != "notes.txt" {
		t.Fatalf("title shows %q, want the workdir-relative name", ed.name)
	}
	if !strings.Contains(screenText(m), "notes.txt") {
		t.Fatalf("title does not name the file:\n%s", screenText(m))
	}
	if m.bodyWidth() >= m.width {
		t.Fatal("chat did not give up columns to the editor")
	}

	m.Update(key("ctrl+e")) // back to the chat
	if m.ps().focus != focusChat {
		t.Fatal("ctrl+e did not move the focus to the chat")
	}
	m.Update(key("ctrl+e")) // and back to the editor
	if m.ps().focus != focusEditor {
		t.Fatal("ctrl+e did not move the focus back to the editor")
	}

	// cat exits on end of input; the pane closes and the chat takes the focus.
	m.Update(key("ctrl+d"))
	deadline := time.Now().Add(5 * time.Second)
	for m.ps().editor != nil && time.Now().Before(deadline) {
		m.onPaneMsg(paneMsg{p: ed.pane})
		time.Sleep(20 * time.Millisecond)
	}
	if m.ps().editor != nil {
		t.Fatal("editor pane stayed open after the program exited")
	}
	if m.ps().focus != focusChat {
		t.Fatal("focus did not return to the chat")
	}
}

func TestEditorSplitCycles(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	want := []int{70, 40, 50, 60}
	for i, w := range want {
		m.runCommand("/editor split")
		if got := editorSplits[m.ps().split]; got != w {
			t.Fatalf("step %d: split %d, want %d", i, got, w)
		}
	}
}

func TestTerminalsCloseRemovesTheTerminalAndFreesTheCap(t *testing.T) {
	skipWithoutPTY(t)
	t.Setenv("SHELL", "/bin/sh")
	m, fb := newTestModel(t, 120, 40)
	defer m.closePanes()
	fb.workdir = t.TempDir()

	for range maxFloats {
		m.runCommand("/terminal new")
	}
	m.runCommand("/terminal new")
	if len(m.ps().floats) != maxFloats {
		t.Fatalf("cap not enforced: %d terminals", len(m.ps().floats))
	}
	// Exited shells used to stay listed, so the cap could never be freed.
	m.runCommand("/terminals close 1")
	if len(m.ps().floats) != maxFloats-1 {
		t.Fatalf("close 1 left %d terminals", len(m.ps().floats))
	}
	m.runCommand("/terminal new")
	if len(m.ps().floats) != maxFloats {
		t.Fatalf("a closed slot was not reused: %d terminals", len(m.ps().floats))
	}
	// Closing the shown terminal hides it and leaves none shown.
	m.runCommand("/terminals close")
	if m.ps().shown != -1 {
		t.Fatalf("closing the shown terminal left shown=%d", m.ps().shown)
	}
	m.runCommand("/terminals close 99")
	if len(m.ps().floats) != maxFloats-1 {
		t.Fatalf("an out-of-range close removed a terminal")
	}
}
