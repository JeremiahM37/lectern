package tui

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// fakeClipText is a clipboard text reader whose contents the test sets.
type fakeClipText struct {
	text  string
	err   error
	reads int
}

func (f *fakeClipText) read() (string, error) {
	f.reads++
	if f.err != nil {
		return "", f.err
	}
	return f.text, nil
}

// withClipText gives the model's input a fake text clipboard.
func withClipText(m *Model, text string, err error) *fakeClipText {
	fc := &fakeClipText{text: text, err: err}
	m.cur().in.clipText = fc.read
	return fc
}

// Ctrl+V on a clipboard with no image but some text pastes the text at the
// cursor, the same way a terminal paste would.
func TestCtrlVPastesClipboardTextWhenThereIsNoImage(t *testing.T) {
	m, _ := newImageModel(t)
	m.cur().in.clip = (&fakeClipboard{err: errNoClipboardImage}).read
	withClipText(m, "explain this stack trace", nil)
	typeText(m, "please ")

	pressCtrlV(t, m)

	if got := m.cur().in.value(); got != "please explain this stack trace" {
		t.Fatalf("input = %q, want the clipboard text at the cursor", got)
	}
	if n := len(m.cur().in.images); n != 0 {
		t.Fatalf("text paste attached %d images", n)
	}
	if m.cur().in.probing {
		t.Fatal("read still marked in flight after the paste")
	}
}

// An image on the clipboard still wins: the text reader is not consulted.
func TestCtrlVPrefersClipboardImageOverText(t *testing.T) {
	m, _ := newImageModel(t)
	withClipboard(m, t)
	ft := withClipText(m, "some words", nil)

	pressCtrlV(t, m)

	if len(m.cur().in.images) != 1 {
		t.Fatal("clipboard image was not attached")
	}
	if ft.reads != 0 {
		t.Fatalf("text was read %d times although an image was there", ft.reads)
	}
	if m.cur().in.value() != "" {
		t.Fatalf("image paste also typed text: %q", m.cur().in.value())
	}
}

// A clipboard with neither image nor text keeps the old behaviour: a log line,
// no flash, nothing typed.
func TestCtrlVWithNoImageOrTextLogsAsBefore(t *testing.T) {
	m, _ := newImageModel(t)
	m.cur().in.clip = (&fakeClipboard{err: errNoClipboardImage}).read
	withClipText(m, "", errNoClipboardText)

	pressCtrlV(t, m)

	if m.cur().in.value() != "" {
		t.Fatalf("empty clipboard typed %q", m.cur().in.value())
	}
	if len(m.errLog) != 1 || m.errLog[0].Text != "No image on the clipboard" {
		t.Fatalf("error log = %+v, want the no-image notice", m.errLog)
	}
}

// With no clipboard helper at all, the missing-tool notice still shows, and no
// text is typed.
func TestCtrlVWithNoClipboardToolStillSaysSo(t *testing.T) {
	m, _ := newImageModel(t)
	m.cur().in.clip = (&fakeClipboard{err: errNoClipboardTool}).read
	withClipText(m, "", errNoClipboardTool)

	pressCtrlV(t, m)

	if m.cur().in.value() != "" {
		t.Fatalf("no tool, yet typed %q", m.cur().in.value())
	}
	if !strings.Contains(m.flash, "No clipboard tool found") {
		t.Fatalf("flash = %q, want the missing-tool notice", m.flash)
	}
}

// A long clipboard text takes the same placeholder as a long bracketed paste,
// and the full text is sent when the prompt is submitted.
func TestCtrlVLongClipboardTextCollapsesAndExpandsOnSubmit(t *testing.T) {
	m, rec := newImageModel(t)
	m.cur().in.clip = (&fakeClipboard{err: errNoClipboardImage}).read
	long := "func a() {}\nfunc b() {}\nfunc c() {}\nfunc d() {}"
	withClipText(m, long, nil)

	pressCtrlV(t, m)

	in := m.cur().in
	if !strings.Contains(in.value(), "<pasted (") {
		t.Fatalf("long text was not collapsed: %q", in.value())
	}
	m.Update(key("enter"))
	if rec.submits != 1 {
		t.Fatalf("submits = %d, want 1", rec.submits)
	}
	if rec.prompt != long {
		t.Fatalf("submitted %q, want the full clipboard text", rec.prompt)
	}
}

// A clipboard that holds a path to an image file attaches the image, the same
// as pasting that path does.
func TestCtrlVClipboardImagePathAttachesImage(t *testing.T) {
	m, _ := newImageModel(t)
	m.cur().in.clip = (&fakeClipboard{err: errNoClipboardImage}).read
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, pngBytes(t), 0o644); err != nil {
		t.Fatal(err)
	}
	withClipText(m, path, nil)

	pressCtrlV(t, m)

	if n := len(m.cur().in.images); n != 1 {
		t.Fatalf("attached %d images from a path on the clipboard, want 1", n)
	}
	if m.cur().in.value() != "" {
		t.Fatalf("image path was typed as text: %q", m.cur().in.value())
	}
}

// Whitespace-only clipboard text is not a paste.
func TestWhitespaceClipboardTextIsNotPasted(t *testing.T) {
	m, _ := newImageModel(t)
	m.cur().in.clip = (&fakeClipboard{err: errNoClipboardImage}).read
	withClipText(m, "  \n\t ", errNoClipboardText)

	pressCtrlV(t, m)

	if m.cur().in.value() != "" {
		t.Fatalf("whitespace was pasted: %q", m.cur().in.value())
	}
}

// The system reader runs the real helper on Linux, so a stand-in wl-paste on
// PATH stands for the Wayland clipboard. The text comes back with CRLF
// normalised, and no trailing newline is added.
func TestLinuxClipboardTextReadsWlPaste(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux clipboard helpers")
	}
	bin := t.TempDir()
	writeScript(t, bin, "wl-paste", `printf 'line one\r\nline two'`)
	t.Setenv("PATH", bin)

	text, err := systemClipboardText()
	if err != nil {
		t.Fatalf("systemClipboardText: %v", err)
	}
	if text != "line one\nline two" {
		t.Fatalf("text = %q", text)
	}
}

// An empty helper output is "no text", not an empty paste.
func TestLinuxClipboardTextEmptyIsNoText(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux clipboard helpers")
	}
	bin := t.TempDir()
	writeScript(t, bin, "wl-paste", "exit 1")
	t.Setenv("PATH", bin)

	if _, err := systemClipboardText(); !errors.Is(err, errNoClipboardText) {
		t.Fatalf("err = %v, want errNoClipboardText", err)
	}
}

// With no helper installed the text read reports the missing tool.
func TestLinuxClipboardTextReportsMissingTool(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux clipboard helpers")
	}
	t.Setenv("PATH", t.TempDir())
	if _, err := systemClipboardText(); !errors.Is(err, errNoClipboardTool) {
		t.Fatalf("err = %v, want errNoClipboardTool", err)
	}
}

// The fallback goes through the probe's own command, so a text read started by
// Ctrl+V reaches the input only when the probe result is delivered.
func TestCtrlVTextReadWaitsForProbeResult(t *testing.T) {
	m, _ := newImageModel(t)
	m.cur().in.clip = (&fakeClipboard{err: errNoClipboardImage}).read
	withClipText(m, "later", nil)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlV})
	if cmd == nil {
		t.Fatal("Ctrl+V started no read")
	}
	if m.cur().in.value() != "" {
		t.Fatal("text appeared before the read finished")
	}
	finishProbe(t, m, cmd)
	if m.cur().in.value() != "later" {
		t.Fatalf("input = %q after the read", m.cur().in.value())
	}
}
