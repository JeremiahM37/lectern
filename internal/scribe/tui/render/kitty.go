package render

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/png"
	"strings"
)

// Protocol is the way a terminal can draw an image inline.
type Protocol int

const (
	// HalfBlockProtocol draws the image as coloured half-block characters. It
	// works in any terminal with 24-bit colour, so it is the default.
	HalfBlockProtocol Protocol = iota
	// KittyProtocol draws real pixels with the Kitty graphics protocol, using
	// Unicode placeholder cells so the picture moves with ordinary text.
	KittyProtocol
)

// DetectProtocol chooses a protocol from the environment (getenv is
// os.Getenv in production). A terminal that does not speak the Kitty protocol
// gets the half-block art: a stray graphics escape prints as garbage.
//
// Inside tmux the answer is always half-block. tmux drops graphics escapes
// unless passthrough is configured for the pane, and it does not draw the
// placeholder cells, so the picture would not appear even if the escapes got
// through.
func DetectProtocol(getenv func(string) string) Protocol {
	// Kitty images are opt-in (SCRIBE_KITTY=1) until they have been checked
	// in a real Kitty, Ghostty and WezTerm: the image data is written beside
	// bubbletea's frames, and a failure there would leave blank cells where
	// the half-block picture works everywhere.
	if getenv("SCRIBE_KITTY") != "1" {
		return HalfBlockProtocol
	}
	if getenv("TMUX") != "" {
		return HalfBlockProtocol
	}
	term := getenv("TERM")
	if strings.HasPrefix(term, "screen") || strings.HasPrefix(term, "tmux") {
		return HalfBlockProtocol
	}
	if term == "xterm-kitty" || getenv("KITTY_WINDOW_ID") != "" {
		return KittyProtocol
	}
	switch strings.ToLower(getenv("TERM_PROGRAM")) {
	case "ghostty", "wezterm":
		return KittyProtocol
	}
	return HalfBlockProtocol
}

const (
	// kittyChunk is the most base64 payload bytes sent in one escape. The
	// protocol allows up to 4096; the payload must split on multiples of 4,
	// which 4096 is.
	kittyChunk = 4096
	// kittyMaxID is the largest image id that fits the 24-bit colour the
	// placeholder cells use to name it.
	kittyMaxID = 0xFFFFFF
	// kittyPlaceholder is U+10EEEE, the codepoint that marks a cell as part of
	// an image rather than text.
	kittyPlaceholder = 0x10EEEE
)

// KittyImage is one image laid out for the Kitty protocol: an id the terminal
// stores it under, its size in cells, and its PNG bytes.
type KittyImage struct {
	ID         uint32
	Cols, Rows int
	Info       ImageInfo
	png        []byte
}

// PrepareKitty decodes data and fits it to at most cols columns and maxRows
// rows of cells, keeping the aspect ratio. id names the image to the
// terminal; it must be between 1 and 0xFFFFFF. Kitty only reads PNG, so other
// formats are re-encoded.
func PrepareKitty(id uint32, data []byte, cols, maxRows int) (KittyImage, error) {
	if id < 1 || id > kittyMaxID {
		return KittyImage{}, fmt.Errorf("kitty image id %d out of range", id)
	}
	src, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return KittyImage{}, fmt.Errorf("decode image: %w", err)
	}
	b := src.Bounds()
	info := ImageInfo{Width: b.Dx(), Height: b.Dy()}
	if info.Width <= 0 || info.Height <= 0 || cols < 1 || maxRows < 1 {
		return KittyImage{}, fmt.Errorf("image %dx%d cannot be placed in %dx%d cells", info.Width, info.Height, cols, maxRows)
	}
	pngBytes := data
	if format != "png" {
		var buf bytes.Buffer
		if err := png.Encode(&buf, src); err != nil {
			return KittyImage{}, fmt.Errorf("encode png: %w", err)
		}
		pngBytes = buf.Bytes()
	}
	c, r := fitCells(info.Width, info.Height, cols, maxRows)
	return KittyImage{ID: id, Cols: c, Rows: r, Info: info, png: pngBytes}, nil
}

// fitCells sizes a w x h picture in cells. A cell is about twice as tall as it
// is wide, so a row covers two columns' worth of height. The result never
// exceeds the diacritic table, which is what limits the grid.
func fitCells(w, h, cols, maxRows int) (int, int) {
	if maxRows > len(kittyDiacritics) {
		maxRows = len(kittyDiacritics)
	}
	if cols > len(kittyDiacritics) {
		cols = len(kittyDiacritics)
	}
	rows := int(float64(cols)*float64(h)/float64(w)/2 + 0.5)
	if rows < 1 {
		rows = 1
	}
	if rows > maxRows {
		rows = maxRows
		cols = int(float64(rows)*2*float64(w)/float64(h) + 0.5)
		if cols < 1 {
			cols = 1
		}
	}
	return cols, rows
}

// Transmit returns the escapes that store the image in the terminal under its
// id, without placing it. Large images are split into chunks of at most
// kittyChunk base64 bytes; every chunk but the last sets m=1, and only the
// first carries the full key set. q=2 asks the terminal not to reply, so
// nothing comes back on stdin.
func (k KittyImage) Transmit() string {
	enc := base64.StdEncoding.EncodeToString(k.png)
	var sb strings.Builder
	for start := 0; ; start += kittyChunk {
		end := start + kittyChunk
		if end > len(enc) {
			end = len(enc)
		}
		more := 0
		if end < len(enc) {
			more = 1
		}
		if start == 0 {
			fmt.Fprintf(&sb, "\x1b_Ga=t,f=100,i=%d,q=2,m=%d;", k.ID, more)
		} else {
			fmt.Fprintf(&sb, "\x1b_Gq=2,m=%d;", more)
		}
		sb.WriteString(enc[start:end])
		sb.WriteString("\x1b\\")
		if end >= len(enc) {
			return sb.String()
		}
	}
}

// Place returns the escape that makes the stored image available to
// placeholder cells: a virtual placement (U=1) sized to the grid. It moves no
// cursor and draws nothing by itself. The text cells from Grid do the drawing.
func (k KittyImage) Place() string {
	return fmt.Sprintf("\x1b_Ga=p,U=1,i=%d,c=%d,r=%d,q=2,C=1\x1b\\", k.ID, k.Cols, k.Rows)
}

// Delete returns the escape that frees the image and its placements. Freeing
// the data (d=I, not d=i) is what keeps a long session from filling the
// terminal's image store.
func (k KittyImage) Delete() string {
	return fmt.Sprintf("\x1b_Ga=d,d=I,i=%d,q=2\x1b\\", k.ID)
}

// Grid returns the placeholder rows, one string per row of the image. Each
// cell is U+10EEEE followed by the row and column diacritics. The image id is
// carried in the foreground colour, as 24-bit RGB, so every row starts with
// that colour and resets it at the end. A row is Cols cells wide.
func (k KittyImage) Grid() []string {
	id := k.ID
	color := fmt.Sprintf("\x1b[38;2;%d;%d;%dm", id>>16&0xff, id>>8&0xff, id&0xff)
	rows := make([]string, 0, k.Rows)
	for r := 0; r < k.Rows; r++ {
		var sb strings.Builder
		sb.WriteString(color)
		for c := 0; c < k.Cols; c++ {
			sb.WriteRune(kittyPlaceholder)
			sb.WriteRune(kittyDiacritics[r])
			sb.WriteRune(kittyDiacritics[c])
		}
		sb.WriteString("\x1b[39m")
		rows = append(rows, sb.String())
	}
	return rows
}
