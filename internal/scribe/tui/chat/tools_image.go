package chat

// An image the read tool attaches is drawn inline under its row, as half-block
// art, so the transcript shows what the model is looking at. The read result
// says which file it is in Meta["image"]. The art is off in terminals narrower
// than readImageMinWidth columns, where it would not fit beside the text.

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
)

const (
	// readImageMinWidth is the narrowest terminal that shows read images.
	readImageMinWidth = 60
	// readImageCols and readImageRows bound the art: a sensible share of the
	// transcript, however large the screenshot.
	readImageCols = 60
	readImageRows = 14
)

// readImageLines are the inline lines for a read result that attached an
// image: the image's path and size, then its art. cw is the width the call's
// rows are laid out in. It returns nil when there is nothing to show.
func (v *View) readImageLines(c *Call, cw int) []string {
	if c.Tool != "read" || c.Result == nil || c.running() || v.w < readImageMinWidth || cw < readImageMinWidth {
		return nil
	}
	rel, _ := c.Result.Meta["image"].(string)
	if rel == "" {
		return nil
	}
	path := rel
	if !filepath.IsAbs(path) && v.opt.Workdir != "" {
		path = filepath.Join(v.opt.Workdir, rel)
	}
	cols := minInt(cw-4, readImageCols)
	if cols < 1 {
		return nil
	}
	// The header shows the path the model was given, as the row above does.
	label := argStr(argsOf(c.Args), "path")
	if label == "" {
		label = v.relPath(path)
	}
	fi, err := os.Stat(path)
	if err != nil {
		return []string{"  " + fg(v.th.TextMuted, icons.Icon("image")+" "+label+" (not found)")}
	}
	key := fmt.Sprintf("%s|%s|%d|%d|%d", path, label, cols, fi.Size(), fi.ModTime().UnixNano())
	art, ok := v.images[key]
	if !ok {
		art = v.imageArt(path, label, cols)
		if v.images == nil {
			v.images = map[string][]string{}
		}
		v.images[key] = art
	}
	return art
}

// imageArt renders the file at path as indented half-block lines under a
// header with its path and pixel size. A file that does not decode gets the
// header and a note.
func (v *View) imageArt(path, label string, cols int) []string {
	head := "  " + fg(v.th.TextDim, icons.Icon("image")) + " " + fg(v.th.TextSecondary, label)
	data, err := os.ReadFile(path)
	if err != nil {
		return []string{head, "  " + fg(v.th.TextMuted, "(could not read this image)")}
	}
	rows, info, err := v.imageRows(data, cols, readImageRows)
	if err == nil && info.Width > 0 {
		head += fg(v.th.TextDim, fmt.Sprintf(" %d×%d", info.Width, info.Height))
	}
	out := []string{head}
	if err != nil || len(rows) == 0 {
		return append(out, "  "+fg(v.th.TextMuted, "(could not show this image)"))
	}
	for _, r := range rows {
		out = append(out, "  "+r)
	}
	return out
}
