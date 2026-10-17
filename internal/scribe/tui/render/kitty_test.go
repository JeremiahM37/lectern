package render

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"math/rand"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestDetectProtocolMatrix(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
		want Protocol
	}{
		{"plain terminal", map[string]string{"TERM": "xterm-256color"}, HalfBlockProtocol},
		{"empty environment", nil, HalfBlockProtocol},
		{"kitty by TERM", map[string]string{"TERM": "xterm-kitty"}, KittyProtocol},
		{"kitty by window id", map[string]string{"TERM": "xterm-256color", "KITTY_WINDOW_ID": "3"}, KittyProtocol},
		{"ghostty", map[string]string{"TERM_PROGRAM": "ghostty"}, KittyProtocol},
		{"WezTerm", map[string]string{"TERM_PROGRAM": "WezTerm"}, KittyProtocol},
		{"iTerm does not speak kitty", map[string]string{"TERM_PROGRAM": "iTerm.app"}, HalfBlockProtocol},
		{"tmux inside kitty", map[string]string{"TMUX": "/tmp/tmux-1000/default,1,0", "TERM": "tmux-256color", "KITTY_WINDOW_ID": "1"}, HalfBlockProtocol},
		{"tmux inside ghostty", map[string]string{"TMUX": "/tmp/x,1,0", "TERM_PROGRAM": "ghostty"}, HalfBlockProtocol},
		{"screen TERM", map[string]string{"TERM": "screen-256color", "KITTY_WINDOW_ID": "1"}, HalfBlockProtocol},
		{"tmux TERM without TMUX", map[string]string{"TERM": "tmux-256color", "TERM_PROGRAM": "ghostty"}, HalfBlockProtocol},
	}
	for _, c := range cases {
		if c.env == nil {
			c.env = map[string]string{}
		}
		c.env["SCRIBE_KITTY"] = "1" // the matrix is for the opt-in path
		if got := DetectProtocol(env(c.env)); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
	// Without the opt-in, even Kitty gets the half-block picture.
	if got := DetectProtocol(env(map[string]string{"TERM": "xterm-kitty"})); got != HalfBlockProtocol {
		t.Errorf("kitty without SCRIBE_KITTY: got %v, want half-block", got)
	}
}

func TestDiacriticTableMatchesProtocol(t *testing.T) {
	if n := len(kittyDiacritics); n != 297 {
		t.Fatalf("table has %d entries, want 297", n)
	}
	if kittyDiacritics[0] != 0x0305 || kittyDiacritics[1] != 0x030D {
		t.Errorf("table starts %X %X, want 305 30D", kittyDiacritics[0], kittyDiacritics[1])
	}
	if last := kittyDiacritics[len(kittyDiacritics)-1]; last != 0x1D244 {
		t.Errorf("table ends %X, want 1D244", last)
	}
	seen := map[rune]bool{}
	for _, r := range kittyDiacritics {
		if seen[r] {
			t.Errorf("duplicate diacritic %X", r)
		}
		seen[r] = true
	}
}

// pngOf encodes img as PNG, failing the test on error.
func pngOf(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func noise(t *testing.T, w, h int) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(1))
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = uint8(rng.Intn(256))
	}
	return pngOf(t, img)
}

// apcBodies returns the body of each graphics escape in s: "keys;payload".
func apcBodies(s string) []string {
	var out []string
	for _, part := range strings.Split(s, "\x1b_G")[1:] {
		out = append(out, strings.TrimSuffix(part, "\x1b\\"))
	}
	return out
}

func TestTransmitSmallImageIsOneEscape(t *testing.T) {
	img, err := PrepareKitty(5, encodePNG(t, 4, 4, color.NRGBA{R: 255, A: 255}), 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	out := img.Transmit()
	bodies := apcBodies(out)
	if len(bodies) != 1 {
		t.Fatalf("got %d escapes, want 1", len(bodies))
	}
	if !strings.HasPrefix(out, "\x1b_Ga=t,f=100,i=5,q=2,m=0;") || !strings.HasSuffix(out, "\x1b\\") {
		t.Errorf("unexpected escape shape: %q", out[:min(len(out), 60)])
	}
}

func TestTransmitChunksAtFourKilobytes(t *testing.T) {
	data := noise(t, 96, 96)
	img, err := PrepareKitty(9, data, 40, 14)
	if err != nil {
		t.Fatal(err)
	}
	bodies := apcBodies(img.Transmit())
	if len(bodies) < 3 {
		t.Fatalf("got %d escapes; a noisy image should need several chunks", len(bodies))
	}
	var payload strings.Builder
	for i, body := range bodies {
		keys, chunk, ok := strings.Cut(body, ";")
		if !ok {
			t.Fatalf("chunk %d has no payload separator", i)
		}
		last := i == len(bodies)-1
		wantM := "m=1"
		if last {
			wantM = "m=0"
		}
		if i == 0 {
			if keys != "a=t,f=100,i=9,q=2,"+wantM {
				t.Errorf("first chunk keys %q", keys)
			}
		} else if keys != "q=2,"+wantM {
			t.Errorf("chunk %d keys %q, want only q and m", i, keys)
		}
		if !last && len(chunk) != kittyChunk {
			t.Errorf("chunk %d is %d bytes, want %d", i, len(chunk), kittyChunk)
		}
		if len(chunk) > kittyChunk {
			t.Errorf("chunk %d is %d bytes, over the limit", i, len(chunk))
		}
		payload.WriteString(chunk)
	}
	got, err := base64.StdEncoding.DecodeString(payload.String())
	if err != nil {
		t.Fatalf("payload is not base64: %v", err)
	}
	if !bytes.Equal(got, data) {
		t.Errorf("reassembled payload differs from the PNG (%d vs %d bytes)", len(got), len(data))
	}
}

func TestNonPNGIsSentAsPNG(t *testing.T) {
	var buf bytes.Buffer
	src := image.NewPaletted(image.Rect(0, 0, 3, 3), color.Palette{color.Black, color.White})
	if err := gif.Encode(&buf, src, nil); err != nil {
		t.Fatal(err)
	}
	img, err := PrepareKitty(2, buf.Bytes(), 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	bodies := apcBodies(img.Transmit())
	_, payload, _ := strings.Cut(bodies[0], ";")
	raw, _ := base64.StdEncoding.DecodeString(payload)
	if !bytes.HasPrefix(raw, []byte("\x89PNG")) {
		t.Errorf("GIF was not re-encoded as PNG")
	}
}

func TestPlaceAndDeleteEscapes(t *testing.T) {
	img := KittyImage{ID: 5, Cols: 4, Rows: 2}
	if got, want := img.Place(), "\x1b_Ga=p,U=1,i=5,c=4,r=2,q=2,C=1\x1b\\"; got != want {
		t.Errorf("Place = %q, want %q", got, want)
	}
	if got, want := img.Delete(), "\x1b_Ga=d,d=I,i=5,q=2\x1b\\"; got != want {
		t.Errorf("Delete = %q, want %q", got, want)
	}
}

func TestGridEncodesRowColumnAndID(t *testing.T) {
	img, err := PrepareKitty(0x010203, encodePNG(t, 4, 4, color.NRGBA{A: 255}), 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	rows := img.Grid()
	if len(rows) != img.Rows || img.Rows != 2 || img.Cols != 4 {
		t.Fatalf("grid %dx%d with %d rows", img.Cols, img.Rows, len(rows))
	}
	for r, line := range rows {
		if !strings.HasPrefix(line, "\x1b[38;2;1;2;3m") {
			t.Errorf("row %d does not carry the id colour: %q", r, line)
		}
		if w := ansi.StringWidth(line); w != img.Cols {
			t.Errorf("row %d is %d cells, want %d", r, w, img.Cols)
		}
		var want strings.Builder
		want.WriteString("\x1b[38;2;1;2;3m")
		for c := 0; c < img.Cols; c++ {
			want.WriteRune(0x10EEEE)
			want.WriteRune(kittyDiacritics[r])
			want.WriteRune(kittyDiacritics[c])
		}
		want.WriteString("\x1b[39m")
		if line != want.String() {
			t.Errorf("row %d cells are wrong:\n got %q\nwant %q", r, line, want.String())
		}
	}
}

func TestPrepareKittyRejectsBadInput(t *testing.T) {
	png := encodePNG(t, 2, 2, color.NRGBA{A: 255})
	if _, err := PrepareKitty(0, png, 4, 4); err == nil {
		t.Error("id 0 accepted")
	}
	if _, err := PrepareKitty(0x1000000, png, 4, 4); err == nil {
		t.Error("id above 24 bits accepted")
	}
	if _, err := PrepareKitty(1, []byte("not a picture"), 4, 4); err == nil {
		t.Error("non-image bytes accepted")
	}
}

func TestFitCellsKeepsShapeAndCaps(t *testing.T) {
	if c, r := fitCells(100, 50, 20, 8); c != 20 || r != 5 {
		t.Errorf("wide image = %dx%d cells, want 20x5", c, r)
	}
	if c, r := fitCells(2, 40, 40, 5); r != 5 || c < 1 {
		t.Errorf("tall image = %dx%d cells, want 5 rows", c, r)
	}
}
