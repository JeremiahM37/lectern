package chat

import (
	"fmt"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/render"
)

// Images in a user message are drawn at most this many cells wide and rows
// high, so a screenshot takes a fixed, modest share of the transcript. Spec
// 01 section 2.2 draws pasted-image thumbnails at 60 columns.
const (
	imageCols = 60
	imageRows = 8
)

// AttachImages shows the image parts of a prompt under the most recent user
// message. Only the image bytes are kept, and only for the session: they are
// not part of Export, so a restored transcript shows the image count alone.
func (v *View) AttachImages(parts []llm.Part) {
	data := imageBytes(parts)
	if len(data) == 0 {
		return
	}
	for i := len(v.msgs) - 1; i >= 0; i-- {
		if rec := v.msgs[i]; rec.Role == roleUser {
			rec.imgs = append(rec.imgs, data...)
			rec.cache = nil
			return
		}
	}
}

// imageLines draws the images of a user message at cw cells: a header with the
// image's number and size, then its half-block picture. An image that does not
// decode gets a header only.
func (v *View) imageLines(rec *Record, cw int) []string {
	var out []string
	cols := minInt(cw, imageCols)
	for i, data := range rec.imgs {
		rows, info, err := render.HalfBlocks(data, cols, imageRows)
		head := fg(v.th.TextDim, icons.Icon("image")) + " " + fg(v.th.TextSecondary, fmt.Sprintf("image %d", i+1))
		if err == nil && info.Width > 0 {
			head += fg(v.th.TextDim, fmt.Sprintf(" %d×%d", info.Width, info.Height))
		}
		out = append(out, head)
		if err != nil {
			out = append(out, fg(v.th.TextMuted, "(could not show this image)"))
			continue
		}
		out = append(out, rows...)
	}
	return out
}

// imageBytes returns the bytes of the image parts among parts.
func imageBytes(parts []llm.Part) [][]byte {
	var out [][]byte
	for _, p := range parts {
		if p.Type == llm.PartImage && len(p.Data) > 0 {
			out = append(out, p.Data)
		}
	}
	return out
}
