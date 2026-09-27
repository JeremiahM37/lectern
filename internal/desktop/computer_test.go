package desktop

import (
	"bytes"
	"context"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
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

// Computer use by element: a real browser on a real display, read through
// AT-SPI, a button pressed and a field typed into by ref.
func TestComputerUseByAccessibilityRef(t *testing.T) {
	needs(t)
	for _, b := range []string{"xdotool", "dbus-daemon", "dbus-send", "xprop", "chromium"} {
		if _, err := exec.LookPath(b); err != nil {
			t.Skipf("needs %s", b)
		}
	}
	if out, _ := exec.Command("/usr/bin/python3", "-c", "import gi;gi.require_version('Atspi','2.0')").CombinedOutput(); len(out) > 0 {
		t.Skip("needs python3-gi with Atspi")
	}
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `<title>Start</title><button onclick="document.title='Pressed '+document.querySelector('input').value">Press me</button><input aria-label="Name">`)
	}))
	defer page.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	d, err := Start(ctx, sh, "a11ytest00001", 1024, 768)
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), sh, d.Dir)
	if _, err := os.Stat(d.Dir + "/a11y"); err != nil {
		t.Fatalf("the desktop has no accessibility bus: %v", err)
	}
	if _, err := OpenBrowser(ctx, sh, d, page.URL); err != nil {
		t.Fatal(err)
	}
	ref := func(tree, want string) int {
		for _, line := range strings.Split(tree, "\n") {
			if strings.Contains(line, want) && strings.Contains(line, "[ref=") {
				n, _ := strconv.Atoi(strings.SplitN(strings.SplitN(line, "[ref=", 2)[1], "]", 2)[0])
				return n
			}
		}
		return 0
	}
	var tree string
	deadline := time.Now().Add(40 * time.Second)
	for time.Now().Before(deadline) {
		tree, _, err = A11ySnapshot(ctx, sh, d)
		if err == nil && ref(tree, `"Press me"`) > 0 && ref(tree, `"Name"`) > 0 {
			break
		}
		time.Sleep(time.Second)
	}
	if ref(tree, `"Press me"`) == 0 {
		t.Fatalf("no button in the accessibility tree (%v):\n%s", err, tree)
	}
	if _, err := A11yAct(ctx, sh, d, ref(tree, `"Name"`), "type", "Ada"); err != nil {
		t.Fatal(err)
	}
	via, err := A11yAct(ctx, sh, d, ref(tree, `"Press me"`), "click", "")
	if err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if tree, _, _ = A11ySnapshot(ctx, sh, d); strings.Contains(tree, "Pressed Ada") {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if !strings.Contains(tree, "Pressed Ada") {
		t.Fatalf("the click (via %s) did not reach the page:\n%s", via, tree)
	}
	t.Logf("clicked via %s; tree:\n%s", via, tree)
	if _, err := A11yAct(ctx, sh, d, 99999, "click", ""); err == nil {
		t.Fatal("an unknown ref was accepted")
	}
}
