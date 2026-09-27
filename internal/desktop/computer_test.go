package desktop

import (
	"bytes"
	"context"
	"image/png"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestComputerUseSeesAndDrivesARealDisplay(t *testing.T) {
	needs(t)
	for _, b := range []string{"xdotool"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("needs %s", b)
		}
	}
	if _, err := exec.LookPath("scrot"); err != nil {
		if _, err := exec.LookPath("import"); err != nil {
			t.Skip("needs scrot or ImageMagick to capture the screen")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	d, err := Start(ctx, sh, "computeruse0001", 800, 600)
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), sh, d.Dir)
	shot, err := Screenshot(ctx, sh, d)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(shot))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 800 || b.Dy() != 600 {
		t.Fatalf("screenshot is %dx%d", b.Dx(), b.Dy())
	}
	// A window appears in the list with its rectangle.
	if _, err := exec.LookPath("xmessage"); err == nil {
		_, _ = sh(ctx, "DISPLAY=:"+strconv.Itoa(d.Display)+" setsid nohup xmessage -geometry +40+50 'hello from lectern' >/dev/null 2>&1 </dev/null & echo $! >"+d.Dir+"/xmessage.pid")
		deadline := time.Now().Add(10 * time.Second)
		var wins []Window
		for time.Now().Before(deadline) {
			if wins, err = Windows(ctx, sh, d); err == nil && len(wins) > 0 {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if len(wins) == 0 || wins[0].Width < 10 {
			t.Fatalf("windows: %+v %v", wins, err)
		}
	} else if _, err := Windows(ctx, sh, d); err != nil {
		t.Fatal(err)
	}
	if err := Act(ctx, sh, d, Action{Type: "move", X: 123, Y: 234}); err != nil {
		t.Fatal(err)
	}
	out, _ := sh(ctx, "DISPLAY=:"+strconv.Itoa(d.Display)+" xdotool getmouselocation")
	if !strings.Contains(out, "x:123 y:234") {
		t.Fatalf("pointer is at %q", out)
	}
	for _, a := range []Action{{Type: "click", X: 800, Y: 10}, {Type: "key", Key: "Return; rm -rf /"},
		{Type: "type"}, {Type: "scroll", X: 1, Y: 1, Scroll: "sideways"}, {Type: "teleport"}} {
		if err := Act(ctx, sh, d, a); err == nil {
			t.Errorf("%+v was accepted", a)
		}
	}
	for _, a := range []Action{{Type: "click", X: 10, Y: 10}, {Type: "key", Key: "ctrl+l"}, {Type: "type", Text: "hi 'there'"},
		{Type: "scroll", X: 50, Y: 50, Scroll: "down"}} {
		if err := Act(ctx, sh, d, a); err != nil {
			t.Errorf("%+v: %v", a, err)
		}
	}
}
