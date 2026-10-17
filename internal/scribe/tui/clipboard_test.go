package tui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
)

// pngBytes is a real 2x2 PNG, so the sniffing and decoding see a genuine file.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// fakeClipboard is a clipboard whose contents the test sets.
type fakeClipboard struct {
	data  []byte
	mt    string
	err   error
	reads int
}

func (f *fakeClipboard) read() ([]byte, string, error) {
	f.reads++
	return f.data, f.mt, f.err
}

// submitRecorder is a fake backend that records what the TUI submits.
type submitRecorder struct {
	*FakeBackend
	prompt  string
	images  []llm.Part
	submits int
}

func (r *submitRecorder) Submit(prompt string, images []llm.Part) {
	r.prompt, r.images = prompt, images
	r.submits++
}

// newImageModel builds a Model whose backend records submissions.
func newImageModel(t *testing.T) (*Model, *submitRecorder) {
	t.Helper()
	fb := NewFakeBackend()
	fb.Delay = 0
	fb.keys["openai"] = ""
	rec := &submitRecorder{FakeBackend: fb}
	m := NewModel(rec, func() (Backend, error) { return NewFakeBackend(), nil }, Options{Width: 100, Height: 40, Theme: "scribe"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m, rec
}

// withClipboard gives the model's input a fake clipboard holding one PNG.
func withClipboard(m *Model, t *testing.T) *fakeClipboard {
	t.Helper()
	fc := &fakeClipboard{data: pngBytes(t), mt: "image/png"}
	m.cur().in.clip = fc.read
	return fc
}

// boxText is the input box as plain text, top border included.
func boxText(m *Model) string {
	return strings.Join(stripLines(m.boxLines(m.cur(), 60, m.modeColor(m.cur()))), "\n")
}

func ctrlV() tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyCtrlV} }

// finishProbe runs the clipboard read that a key started and feeds its result
// back into the model, as the bubbletea loop would.
func finishProbe(t *testing.T, m *Model, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		t.Fatal("the key started no clipboard read")
	}
	m.Update(cmd())
}

// pressCtrlV is Ctrl+V followed by the clipboard read it starts.
func pressCtrlV(t *testing.T, m *Model) {
	t.Helper()
	_, cmd := m.Update(ctrlV())
	finishProbe(t, m, cmd)
}

func TestCtrlVAttachesClipboardImageAsChip(t *testing.T) {
	m, _ := newImageModel(t)
	fc := withClipboard(m, t)

	pressCtrlV(t, m)

	if fc.reads != 1 {
		t.Fatalf("clipboard read %d times, want 1", fc.reads)
	}
	in := m.cur().in
	if len(in.images) != 1 {
		t.Fatalf("attached %d images, want 1", len(in.images))
	}
	if p := in.images[0].part; p.Type != llm.PartImage || p.MediaType != "image/png" {
		t.Fatalf("part = %+v, want a png image part", p)
	}
	if !strings.Contains(boxText(m), "[image 1]") {
		t.Fatalf("box does not show the chip:\n%s", boxText(m))
	}
	if in.value() != "" {
		t.Fatalf("Ctrl+V put text in the input: %q", in.value())
	}
}

func TestCtrlVWithNoImageShowsNoticeAndAttachesNothing(t *testing.T) {
	m, _ := newImageModel(t)
	m.cur().in.clip = (&fakeClipboard{err: errNoClipboardImage}).read
	// No text either, whatever the machine's real clipboard holds.
	m.cur().in.clipText = func() (string, error) { return "", errNoClipboardText }

	pressCtrlV(t, m)

	if n := len(m.cur().in.images); n != 0 {
		t.Fatalf("attached %d images from an empty clipboard", n)
	}
	// spec: no toast; the notice goes to the error log
	if m.flash != "" {
		t.Fatalf("flash = %q, want none (the notice belongs in the error log)", m.flash)
	}
	if len(m.errLog) != 1 || m.errLog[0].Text != "No image on the clipboard" {
		t.Fatalf("error log = %+v, want the no-image notice", m.errLog)
	}
	if strings.Contains(boxText(m), "[image") {
		t.Fatal("box shows a chip with nothing attached")
	}
}

func TestAltVAlsoProbesClipboard(t *testing.T) {
	m, _ := newImageModel(t)
	withClipboard(m, t)

	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("v"), Alt: true})
	finishProbe(t, m, cmd)

	if len(m.cur().in.images) != 1 {
		t.Fatal("Alt+v did not attach the clipboard image")
	}
	if m.cur().in.value() != "" {
		t.Fatalf("Alt+v typed text: %q", m.cur().in.value())
	}
}

func TestBackspaceOnEmptyInputRemovesLastChip(t *testing.T) {
	m, _ := newImageModel(t)
	withClipboard(m, t)
	pressCtrlV(t, m)
	pressCtrlV(t, m)
	in := m.cur().in
	if len(in.images) != 2 || !strings.Contains(boxText(m), "[image 2]") {
		t.Fatalf("setup: want two chips, got %d", len(in.images))
	}

	m.Update(tea.KeyMsg{Type: tea.KeyBackspace})

	if len(in.images) != 1 || in.images[0].n != 1 {
		t.Fatalf("after one Backspace: %d chips, want chip 1 only", len(in.images))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if len(in.images) != 0 {
		t.Fatalf("after two Backspaces: %d chips, want none", len(in.images))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyBackspace}) // nothing left: no panic, no effect
	if in.imageN != 0 {
		t.Fatalf("label counter = %d after all chips removed, want 0", in.imageN)
	}
}

func TestBackspaceWithTextDeletesTextNotChip(t *testing.T) {
	m, _ := newImageModel(t)
	withClipboard(m, t)
	pressCtrlV(t, m)
	typeText(m, "hi")

	m.Update(tea.KeyMsg{Type: tea.KeyBackspace})

	if got := m.cur().in.value(); got != "h" {
		t.Fatalf("input = %q, want %q", got, "h")
	}
	if len(m.cur().in.images) != 1 {
		t.Fatal("Backspace in a non-empty input removed the chip")
	}
}

func TestSubmitSendsAttachedImagesAsParts(t *testing.T) {
	m, rec := newImageModel(t)
	withClipboard(m, t)
	pressCtrlV(t, m)
	typeText(m, "what is this")

	m.Update(key("enter"))

	if rec.submits != 1 || rec.prompt != "what is this" {
		t.Fatalf("submitted %d times with %q", rec.submits, rec.prompt)
	}
	if len(rec.images) != 1 {
		t.Fatalf("backend got %d image parts, want 1", len(rec.images))
	}
	if p := rec.images[0]; p.Type != llm.PartImage || p.MediaType != "image/png" || !bytes.Equal(p.Data, pngBytes(t)) {
		t.Fatalf("image part does not match the clipboard bytes: type=%s media=%s", p.Type, p.MediaType)
	}
	if len(m.cur().in.images) != 0 {
		t.Fatal("chips survived the submit")
	}
	if strings.Contains(boxText(m), "[image") {
		t.Fatal("box still shows a chip after submit")
	}
}

func TestImageAloneIsAMessage(t *testing.T) {
	m, rec := newImageModel(t)
	withClipboard(m, t)
	pressCtrlV(t, m)

	m.Update(key("enter"))

	if rec.submits != 1 {
		t.Fatalf("image alone did not submit (submits=%d)", rec.submits)
	}
	if rec.prompt != imageOnlyPrompt {
		t.Fatalf("prompt = %q, want the image-only prompt", rec.prompt)
	}
	if len(rec.images) != 1 {
		t.Fatalf("backend got %d images, want 1", len(rec.images))
	}
}

func TestEnterWithNothingAttachedDoesNothing(t *testing.T) {
	m, rec := newImageModel(t)

	m.Update(key("enter"))

	if rec.submits != 0 {
		t.Fatalf("empty Enter submitted a turn")
	}
}

func TestPastedImagePathAttachesFileWithoutInsertingText(t *testing.T) {
	m, rec := newImageModel(t)
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, pngBytes(t), 0o644); err != nil {
		t.Fatal(err)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(path), Paste: true})

	in := m.cur().in
	if len(in.images) != 1 {
		t.Fatalf("pasted path attached %d images, want 1 (flash %q)", len(in.images), m.flash)
	}
	if in.value() != "" {
		t.Fatalf("path text went into the input: %q", in.value())
	}
	m.Update(key("enter"))
	if rec.submits != 1 || len(rec.images) != 1 {
		t.Fatalf("file image did not travel with the submit")
	}
}

func TestPastedTextPathThatIsNotAnImageStaysText(t *testing.T) {
	m, _ := newImageModel(t)
	path := filepath.Join(t.TempDir(), "notes.txt")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(path), Paste: true})

	if len(m.cur().in.images) != 0 {
		t.Fatal("a .txt path was attached as an image")
	}
	if m.cur().in.value() != path {
		t.Fatalf("input = %q, want the pasted path as text", m.cur().in.value())
	}
}

func TestPastedImageTooBigIsRefused(t *testing.T) {
	m, _ := newImageModel(t)
	path := filepath.Join(t.TempDir(), "huge.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(clipImageMax + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(path), Paste: true})

	if len(m.cur().in.images) != 0 {
		t.Fatal("an oversized image was attached")
	}
	if m.flashKind != "err" || !strings.Contains(m.flash, "limit") {
		t.Fatalf("flash = %q (%s), want the size-limit error", m.flash, m.flashKind)
	}
}

func TestImagePathFromPaste(t *testing.T) {
	dir := t.TempDir()
	plain := filepath.Join(dir, "a.png")
	spaced := filepath.Join(dir, "my shot.JPG")
	for _, p := range []string{plain, spaced} {
		if err := os.WriteFile(p, pngBytes(t), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name, in, want string
		ok             bool
	}{
		{"plain", plain, plain, true},
		{"surrounding whitespace", "  " + plain + "\n", plain, true},
		{"single quoted", "'" + plain + "'", plain, true},
		{"double quoted", `"` + plain + `"`, plain, true},
		{"file url", "file://" + strings.ReplaceAll(spaced, " ", "%20"), spaced, true},
		{"escaped space", strings.ReplaceAll(spaced, " ", `\ `), spaced, true},
		{"missing file", filepath.Join(dir, "gone.png"), "", false},
		{"not an image extension", filepath.Join(dir, "x.txt"), "", false},
		{"two lines", plain + "\n" + plain, "", false},
		{"a directory", dir + ".png", "", false},
		{"empty", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := imagePathFromPaste(c.in)
			if ok != c.ok || got != c.want {
				t.Fatalf("imagePathFromPaste(%q) = (%q, %v), want (%q, %v)", c.in, got, ok, c.want, c.ok)
			}
		})
	}
}

func TestSniffImage(t *testing.T) {
	webp := append([]byte("RIFF\x00\x00\x00\x00WEBP"), 0)
	cases := []struct {
		want string
		in   []byte
	}{
		{"image/png", pngBytes(t)},
		{"image/jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0}},
		{"image/gif", []byte("GIF89a....")},
		{"image/webp", webp},
		{"", []byte("hello, world")},
		{"", nil},
	}
	for _, c := range cases {
		if got := sniffImage(c.in); got != c.want {
			t.Errorf("sniffImage(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// writeScript writes an executable shell script named name into dir.
func writeScript(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// The Linux path runs the real helper binaries, so it is exercised with small
// stand-in scripts on PATH.
func TestLinuxClipboardFallsBackFromWlPasteToXclip(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux clipboard helpers")
	}
	png := pngBytes(t)
	pngPath := filepath.Join(t.TempDir(), "clip.png")
	if err := os.WriteFile(pngPath, png, 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeScript(t, bin, "wl-paste", "exit 1") // no image on the Wayland clipboard
	writeScript(t, bin, "xclip", "cat "+pngPath)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	data, err := linuxClipboardImage(ctx)
	if err != nil {
		t.Fatalf("linuxClipboardImage: %v", err)
	}
	if !bytes.Equal(data, png) {
		t.Fatal("clipboard bytes did not come back from xclip")
	}
}

func TestLinuxClipboardReportsMissingTool(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux clipboard helpers")
	}
	t.Setenv("PATH", t.TempDir()) // no helpers at all
	_, err := linuxClipboardImage(context.Background())
	if !errors.Is(err, errNoClipboardTool) {
		t.Fatalf("err = %v, want errNoClipboardTool", err)
	}
}
