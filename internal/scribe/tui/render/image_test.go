package render

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func encodePNG(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestHalfBlocksKeepsAspectAndPairsPixels(t *testing.T) {
	// A 4x4 square at 4 columns: 4 pixels tall, so 2 rows of half blocks.
	rows, info, err := HalfBlocks(encodePNG(t, 4, 4, color.NRGBA{R: 255, A: 255}), 4, 8)
	if err != nil {
		t.Fatal(err)
	}
	if info.Width != 4 || info.Height != 4 {
		t.Errorf("info = %+v", info)
	}
	if len(rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(rows))
	}
	for i, r := range rows {
		if w := ansi.StringWidth(r); w != 4 {
			t.Errorf("row %d is %d cells, want 4", i, w)
		}
		if strings.Count(r, "▀") != 4 {
			t.Errorf("row %d has %d half blocks, want 4", i, strings.Count(r, "▀"))
		}
	}
	// The top pixel is red in the foreground, so the first cell carries it.
	if !strings.Contains(rows[0], "\x1b[38;2;255;0;0m") {
		t.Errorf("top pixel colour missing: %q", rows[0])
	}
}

func TestHalfBlocksCapsRows(t *testing.T) {
	// A tall image is limited to maxRows rows, and narrowed to keep its shape.
	rows, _, err := HalfBlocks(encodePNG(t, 2, 40, color.NRGBA{G: 255, A: 255}), 40, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 5 {
		t.Errorf("rows = %d, want 5", len(rows))
	}
	if w := ansi.StringWidth(rows[0]); w >= 40 {
		t.Errorf("tall image kept full width (%d cells)", w)
	}
}

func TestHalfBlocksRejectsNonImages(t *testing.T) {
	if _, _, err := HalfBlocks([]byte("plain text"), 10, 4); err == nil {
		t.Errorf("non-image bytes decoded")
	}
}

func TestHalfBlocksTransparentIsDark(t *testing.T) {
	rows, _, err := HalfBlocks(encodePNG(t, 2, 2, color.NRGBA{}), 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rows[0], "\x1b[38;2;0;0;0m") {
		t.Errorf("transparent pixel is not black: %q", rows[0])
	}
}
