package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
	"github.com/charmbracelet/x/ansi"
)

// useProtocol switches the image protocol for one test and restores it after,
// and forgets the terminal images the test created.
func useProtocol(t *testing.T, p render.Protocol) {
	t.Helper()
	old := imageProtocol
	imageProtocol = p
	t.Cleanup(func() {
		imageProtocol = old
		kittyEntries = nil
	})
}

func countOf(s, sub string) int { return strings.Count(s, sub) }

func TestKittyCellsAreTextAndSentOnce(t *testing.T) {
	useProtocol(t, render.KittyProtocol)
	v, c := newView(t, Options{})
	v.AddUser("what is this", 1, at(c), false)
	v.AttachImages([]llm.Part{{Type: llm.PartImage, Data: pngBytes(t, 4, 4)}})

	frame := v.View()
	if !strings.Contains(frame, "\U0010EEEE") {
		t.Fatalf("no placeholder cells in the frame:\n%s", ansiStripped(frame))
	}
	// The frame is text only: graphics escapes are written out of band.
	if strings.Contains(frame, "\x1b_G") {
		t.Errorf("graphics escape leaked into the frame")
	}
	if !strings.Contains(plain(v), "image 1 4×4") {
		t.Errorf("header missing under Kitty:\n%s", plain(v))
	}

	base := kittyEntries[0].placed
	first := v.KittyPending(base)
	if countOf(first, "a=t,f=100,") != 1 || countOf(first, "a=p,U=1,") != 1 {
		t.Errorf("first flush = %q, want one transmit and one placement", first)
	}
	if again := v.KittyPending(base); again != "" {
		t.Errorf("second flush resent %q", again)
	}
	// A redraw does not transmit again: the id is already in the terminal.
	_ = v.View()
	if again := v.KittyPending(base); again != "" {
		t.Errorf("redraw resent %q", again)
	}
}

func TestKittyPlacementRearmsAt50And200Milliseconds(t *testing.T) {
	useProtocol(t, render.KittyProtocol)
	v, c := newView(t, Options{})
	v.AddUser("look", 1, at(c), false)
	v.AttachImages([]llm.Part{{Type: llm.PartImage, Data: pngBytes(t, 4, 4)}})
	_ = v.View()
	base := kittyEntries[0].placed
	_ = v.KittyPending(base) // the transmit and first placement

	steps := []struct {
		after  time.Duration
		places int
	}{
		{10 * time.Millisecond, 0},
		{60 * time.Millisecond, 1}, // 50 ms re-arm
		{100 * time.Millisecond, 0},
		{250 * time.Millisecond, 1}, // 200 ms re-arm
		{time.Second, 0},
	}
	for _, s := range steps {
		out := v.KittyPending(base.Add(s.after))
		if n := countOf(out, "a=p,U=1,"); n != s.places {
			t.Errorf("at %v: %d placements, want %d (%q)", s.after, n, s.places, out)
		}
		if countOf(out, "a=t,") != 0 {
			t.Errorf("at %v: re-arm retransmitted the image", s.after)
		}
	}
	if v.KittyWanted() {
		t.Errorf("still wants a flush after both re-arms")
	}
}

func TestKittyClearFreesImages(t *testing.T) {
	useProtocol(t, render.KittyProtocol)
	v, c := newView(t, Options{})
	v.AddUser("look", 1, at(c), false)
	v.AttachImages([]llm.Part{{Type: llm.PartImage, Data: pngBytes(t, 4, 4)}})
	_ = v.View()
	base := kittyEntries[0].placed
	_ = v.KittyPending(base)
	oldID := kittyEntries[0].img.ID

	v.ForgetImages()
	if !v.KittyWanted() {
		t.Fatalf("cleared image queued no delete")
	}
	out := v.KittyPending(base)
	if want := "a=d,d=I,i=" + fmt.Sprint(oldID) + ","; !strings.Contains(out, want) {
		t.Errorf("flush after clear = %q, want %q", out, want)
	}
	if v.KittyWanted() {
		t.Errorf("delete was not written once")
	}

	// Showing the same picture again needs a fresh transmit under a new id.
	v.AddUser("again", 1, at(c), false)
	v.AttachImages([]llm.Part{{Type: llm.PartImage, Data: pngBytes(t, 4, 4)}})
	_ = v.View()
	out = v.KittyPending(base)
	if !strings.Contains(out, "a=t,") || strings.Contains(out, "i="+fmt.Sprint(oldID)+",") {
		t.Errorf("image after clear = %q, want a transmit under a new id", out)
	}
}

func TestHalfBlockTerminalNeverGetsKittyEscapes(t *testing.T) {
	useProtocol(t, render.HalfBlockProtocol)
	v, c := newView(t, Options{})
	v.AddUser("look", 1, at(c), false)
	v.AttachImages([]llm.Part{{Type: llm.PartImage, Data: pngBytes(t, 4, 4)}})
	if !strings.Contains(plain(v), "▀") {
		t.Errorf("half-block art missing:\n%s", plain(v))
	}
	if strings.Contains(v.View(), "\U0010EEEE") {
		t.Errorf("placeholder cells in a half-block terminal")
	}
	if v.KittyWanted() || v.KittyPending(time.Now()) != "" {
		t.Errorf("a half-block terminal queued kitty escapes")
	}
}

func TestKittyFallsBackForUndecodableImage(t *testing.T) {
	useProtocol(t, render.KittyProtocol)
	v, c := newView(t, Options{})
	v.AddUser("look", 1, at(c), false)
	v.AttachImages([]llm.Part{{Type: llm.PartImage, Data: []byte("not a picture")}})
	if !strings.Contains(plain(v), "could not show this image") {
		t.Errorf("fallback line missing:\n%s", plain(v))
	}
	if v.KittyWanted() {
		t.Errorf("an undecodable image was queued for the terminal")
	}
}

func TestReadResultArtUsesKittyGrid(t *testing.T) {
	useProtocol(t, render.KittyProtocol)
	dir := t.TempDir()
	path := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(path, pngBytes(t, 4, 4), 0o600); err != nil {
		t.Fatal(err)
	}
	v, _ := newView(t, Options{})
	lines := v.imageArt(path, "shot.png", 10)
	if !strings.Contains(strings.Join(lines, "\n"), "\U0010EEEE") {
		t.Errorf("read art has no placeholder cells: %q", lines)
	}
	if !v.KittyWanted() {
		t.Errorf("read art queued no transmit")
	}
}

func ansiStripped(s string) string { return ansi.Strip(s) }
