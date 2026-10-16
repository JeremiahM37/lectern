package tui

import (
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// demoImageOnce guards the one PNG the demo reads; demoImagePath is its file.
var (
	demoImageOnce sync.Once
	demoImagePath string
)

// demoImage writes a small parser diagram to a temp file once and returns its
// path. The demo's read of it shows the image inline in the transcript.
func demoImage() string {
	demoImageOnce.Do(func() {
		dir, err := os.MkdirTemp("", "scribe-demo-")
		if err != nil {
			return
		}
		img := image.NewRGBA(image.Rect(0, 0, 96, 48))
		for y := 0; y < 48; y++ {
			for x := 0; x < 96; x++ {
				c := color.RGBA{R: uint8(20 + x*2), G: uint8(30 + y*3), B: 90, A: 255}
				if (x/16+y/12)%2 == 0 {
					c.B = 160
				}
				img.Set(x, y, c)
			}
		}
		path := filepath.Join(dir, "parser-flow.png")
		fh, err := os.Create(path)
		if err != nil {
			return
		}
		defer fh.Close()
		if err := png.Encode(fh, img); err != nil {
			return
		}
		demoImagePath = path
	})
	return demoImagePath
}

// imageRead emits a read of an image file. The result's Meta carries the
// image path, which is how the transcript knows to draw it (the read tool's
// own shape).
func (f *FakeBackend) imageRead(id, rel, path string) {
	raw, _ := json.Marshal(map[string]any{"path": rel})
	f.emit(agent.Event{Kind: agent.EvToolStart, CallID: id, Tool: "read"})
	f.emit(agent.Event{Kind: agent.EvToolArgs, CallID: id, Text: string(raw)})
	f.emit(agent.Event{Kind: agent.EvToolRun, CallID: id, Tool: "read", Args: raw})
	f.pause()
	f.emit(agent.Event{Kind: agent.EvToolResult, CallID: id, Tool: "read", Duration: 6 * time.Millisecond,
		Result: &tools.Result{
			Output:  "Image " + rel + " (png). The image is attached for viewing, 60 columns wide.",
			Summary: "image",
			Meta:    map[string]any{"image": path, "cols": 60},
		}})
	f.pause()
}
