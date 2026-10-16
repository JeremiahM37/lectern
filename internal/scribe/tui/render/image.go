package render

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif" // decoders for the formats a user can paste
	_ "image/jpeg"
	_ "image/png"
	"strings"
)

// ImageInfo is what HalfBlocks learned about an image.
type ImageInfo struct {
	Width, Height int // pixels, before scaling
}

// HalfBlocks draws an image with the upper-half block character: each cell
// is one column wide and holds two vertical pixels, the top in the foreground
// colour and the bottom in the background. It works in any terminal with
// 24-bit colour and needs no graphics protocol.
//
// The picture is scaled to at most cols cells wide and maxRows rows high,
// keeping its aspect ratio (a terminal cell is about twice as tall as it is
// wide, which the two-pixel cell already accounts for). Each returned string is
// one row. An error means the bytes are not an image in a format this build
// reads.
func HalfBlocks(data []byte, cols, maxRows int) ([]string, ImageInfo, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, ImageInfo{}, fmt.Errorf("decode image: %w", err)
	}
	b := src.Bounds()
	info := ImageInfo{Width: b.Dx(), Height: b.Dy()}
	if info.Width <= 0 || info.Height <= 0 || cols < 1 || maxRows < 1 {
		return nil, info, nil
	}
	// Pixel grid: cols wide, and two pixels per row, capped at maxRows rows.
	w := cols
	h := int(float64(w) * float64(info.Height) / float64(info.Width))
	if h < 2 {
		h = 2
	}
	if h > maxRows*2 {
		h = maxRows * 2
		w = int(float64(h) * float64(info.Width) / float64(info.Height))
		if w < 1 {
			w = 1
		}
	}
	if h%2 == 1 {
		h++
	}
	px := scale(src, w, h)

	rows := make([]string, 0, h/2)
	for y := 0; y < h; y += 2 {
		var sb strings.Builder
		for x := 0; x < w; x++ {
			top := px.NRGBAAt(x, y)
			bot := px.NRGBAAt(x, y+1)
			fmt.Fprintf(&sb, "\x1b[38;2;%d;%d;%dm\x1b[48;2;%d;%d;%dm▀",
				top.R, top.G, top.B, bot.R, bot.G, bot.B)
		}
		sb.WriteString("\x1b[0m")
		rows = append(rows, sb.String())
	}
	return rows, info, nil
}

// scale resamples src to w x h with nearest-neighbour sampling. Transparent
// pixels are composited over black first, so they draw as a dark cell rather
// than as the terminal's default colour.
func scale(src image.Image, w, h int) *image.NRGBA {
	b := src.Bounds()
	base := image.NewNRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(base, base.Bounds(), &image.Uniform{C: color.Black}, image.Point{}, draw.Src)
	draw.Draw(base, base.Bounds(), src, b.Min, draw.Over)

	out := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		sy := y * b.Dy() / h
		for x := 0; x < w; x++ {
			sx := x * b.Dx() / w
			out.SetNRGBA(x, y, base.NRGBAAt(sx, sy))
		}
	}
	return out
}
