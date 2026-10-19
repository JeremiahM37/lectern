package computer

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// TestLiveDesktop drives a real virtual display: it starts Xvfb, takes a
// screenshot, moves the pointer and clicks, types, and stops the display. It
// needs Xvfb, xdotool and scrot on this machine, so it runs only when
// LECTERN_LIVE_DESKTOP=1 is set.
func TestLiveDesktop(t *testing.T) {
	if os.Getenv("LECTERN_LIVE_DESKTOP") != "1" {
		t.Skip("set LECTERN_LIVE_DESKTOP=1 to run against a real display")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r := New()
	defer r.Close(ctx)
	h := &fakeHost{decision: tools.AllowSession}

	shot := r.Run(ctx, h, json.RawMessage(`{"action":"screenshot"}`))
	if shot.IsError {
		t.Fatalf("screenshot: %s", shot.Output)
	}
	if !bytes.HasPrefix(shot.Image, []byte("\x89PNG")) || len(shot.Image) < 200 {
		t.Fatalf("screenshot is not a PNG of the display (%d bytes)", len(shot.Image))
	}
	for _, in := range []string{
		`{"action":"mouse_move","coordinate":[300,200]}`,
		`{"action":"left_click","coordinate":[300,200]}`,
		`{"action":"type","text":"hello"}`,
		`{"action":"key","text":"Return"}`,
		`{"action":"scroll","coordinate":[300,200],"scroll_direction":"down","scroll_amount":2}`,
		`{"action":"wait","duration":0.2}`,
	} {
		if res := r.Run(ctx, h, json.RawMessage(in)); res.IsError {
			t.Fatalf("%s: %s", in, res.Output)
		}
	}
	// The pointer must have gone where the move said. xdotool reports it.
	d, err := r.display(ctx)
	if err != nil {
		t.Fatal(err)
	}
	out, err := r.run(ctx, "DISPLAY=:"+strconv.Itoa(d.Display)+" xdotool getmouselocation --shell")
	if err != nil {
		t.Fatalf("getmouselocation: %v", err)
	}
	if !bytes.Contains([]byte(out), []byte("X=300")) || !bytes.Contains([]byte(out), []byte("Y=200")) {
		t.Fatalf("pointer is at %q, want X=300 Y=200", out)
	}
}
