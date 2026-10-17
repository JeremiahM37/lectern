package chat

import (
	"fmt"
	"hash/fnv"
	"os"
	"strings"
	"time"

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

// imageProtocol is how this process draws images, read once from the
// environment. Tests replace it.
var imageProtocol = render.DetectProtocol(os.Getenv)

// kittyRearm are the times after an image's transmit at which its placement
// is sent again (spec 01 section 2.11: the paint race). Terminals that have
// not finished the first placement by then draw it on the second.
var kittyRearm = []time.Duration{50 * time.Millisecond, 200 * time.Millisecond}

// kittyEntry is one image held by the terminal for one View. A dead entry has
// been cleared from the transcript; its delete is still waiting to be written.
type kittyEntry struct {
	owner   *View
	key     string
	img     render.KittyImage
	placed  time.Time // when the transmit was queued
	rearms  int       // how many kittyRearm delays have fired
	pending string    // escapes queued for the next flush
	dead    bool
}

// kittyEntries is shared by all Views, because the terminal is shared: image
// ids are unique across tabs. Only the bubbletea loop touches it.
var (
	kittyEntries []*kittyEntry
	kittyLastID  uint32
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
// image's number and size, then its picture. An image that does not decode
// gets a header only.
func (v *View) imageLines(rec *Record, cw int) []string {
	var out []string
	cols := minInt(cw, imageCols)
	for i, data := range rec.imgs {
		rows, info, err := v.imageRows(data, cols, imageRows)
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

// imageRows lays data out for the active protocol: a placeholder grid under
// Kitty, half-block art otherwise. An image that Kitty cannot prepare is drawn
// as half-block art instead.
func (v *View) imageRows(data []byte, cols, maxRows int) ([]string, render.ImageInfo, error) {
	if imageProtocol == render.KittyProtocol {
		if img, err := kittyFor(v, data, cols, maxRows); err == nil {
			return img.Grid(), img.Info, nil
		}
	}
	return render.HalfBlocks(data, cols, maxRows)
}

// kittyFor returns the terminal image for data at cols x maxRows cells. The
// first call queues the transmit and placement; later calls reuse the image.
func kittyFor(v *View, data []byte, cols, maxRows int) (render.KittyImage, error) {
	key := fmt.Sprintf("%p|%016x|%d|%d", v, fnv64(data), cols, maxRows)
	for _, e := range kittyEntries {
		if !e.dead && e.key == key {
			return e.img, nil
		}
	}
	img, err := render.PrepareKitty(nextKittyID(), data, cols, maxRows)
	if err != nil {
		return render.KittyImage{}, err
	}
	kittyEntries = append(kittyEntries, &kittyEntry{
		owner:   v,
		key:     key,
		img:     img,
		placed:  time.Now(),
		pending: img.Transmit() + img.Place(),
	})
	return img, nil
}

// nextKittyID hands out ids 1 to 0xFFFFFF in turn. Ids are cheap, and a run
// of 16 million distinct images would be needed before one is reused.
func nextKittyID() uint32 {
	kittyLastID++
	if kittyLastID > 0xFFFFFF {
		kittyLastID = 1
	}
	return kittyLastID
}

// KittyWanted reports whether v has escapes to write, or a placement re-arm
// still to come. The app keeps its frame tick running while this is true.
func (v *View) KittyWanted() bool {
	for _, e := range kittyEntries {
		if e.owner == v && (e.dead || e.pending != "" || e.rearms < len(kittyRearm)) {
			return true
		}
	}
	return false
}

// KittyPending returns the escapes to write for v now, and marks them sent.
// It holds the transmits and placements queued while the transcript was drawn,
// the placement re-arms that have come due by now, and the deletes for cleared
// images. The caller writes the result to the terminal in one write, outside
// the frame: image data is too large to repeat in every frame.
func (v *View) KittyPending(now time.Time) string {
	var sb strings.Builder
	live := kittyEntries[:0]
	for _, e := range kittyEntries {
		if e.owner != v {
			live = append(live, e)
			continue
		}
		sb.WriteString(e.pending)
		e.pending = ""
		if e.dead {
			sb.WriteString(e.img.Delete())
			continue
		}
		for e.rearms < len(kittyRearm) && !now.Before(e.placed.Add(kittyRearm[e.rearms])) {
			sb.WriteString(e.img.Place())
			e.rearms++
		}
		live = append(live, e)
	}
	kittyEntries = live
	return sb.String()
}

// kittyForget marks every image v holds as dead, so its delete is written on
// the next flush, and drops the cached art that named those ids. It is called
// when the transcript is cleared.
func kittyForget(v *View) {
	for _, e := range kittyEntries {
		if e.owner == v && !e.dead {
			e.dead = true
			e.pending = ""
		}
	}
	v.images = nil
}

// ForgetImages releases the terminal images v holds. Call it on a transcript
// that is being dropped or cleared; its deletes are written by KittyPending.
func (v *View) ForgetImages() { kittyForget(v) }

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

func fnv64(data []byte) uint64 {
	h := fnv.New64a()
	_, _ = h.Write(data)
	return h.Sum64()
}
