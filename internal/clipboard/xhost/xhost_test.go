package xhost

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/jezek/xgb"
	"github.com/jezek/xgb/xproto"
)

// nativeRead is a stand-in for a program that reads the X clipboard through
// the X protocol itself, as Codex's arboard does (no xclip, no wl-paste): it
// converts the CLIPBOARD selection to target and follows INCR transfers.
func nativeRead(t *testing.T, display, xauth, target string) ([]byte, bool) {
	t.Helper()
	os.Setenv("XAUTHORITY", xauth)
	x, err := xgb.NewConnDisplay(display)
	if err != nil {
		t.Fatal(err)
	}
	defer x.Close()
	screen := xproto.Setup(x).DefaultScreen(x)
	win, _ := xproto.NewWindowId(x)
	xproto.CreateWindow(x, 0, win, screen.Root, 0, 0, 1, 1, 0, xproto.WindowClassInputOnly, screen.RootVisual,
		xproto.CwEventMask, []uint32{xproto.EventMaskPropertyChange})
	atom := func(n string) xproto.Atom {
		r, err := xproto.InternAtom(x, false, uint16(len(n)), n).Reply()
		if err != nil {
			t.Fatal(err)
		}
		return r.Atom
	}
	clip, prop, incr := atom("CLIPBOARD"), atom("LECTERN_TEST_PROP"), atom("INCR")
	xproto.ConvertSelection(x, win, clip, atom(target), prop, xproto.TimeCurrentTime)
	var out []byte
	inIncr := false
	deadline := time.After(10 * time.Second)
	events := make(chan xgb.Event, 64)
	go func() {
		for {
			ev, err := x.WaitForEvent()
			if ev == nil && err == nil {
				close(events)
				return
			}
			if ev != nil {
				events <- ev
			}
		}
	}()
	for {
		select {
		case <-deadline:
			t.Fatal("timed out reading the clipboard")
		case ev, ok := <-events:
			if !ok {
				return nil, false
			}
			switch e := ev.(type) {
			case xproto.SelectionNotifyEvent:
				if e.Property == 0 {
					return nil, false
				}
				r, err := xproto.GetProperty(x, true, win, prop, xproto.GetPropertyTypeAny, 0, 1<<24).Reply()
				if err != nil {
					t.Fatal(err)
				}
				if r.Type == incr {
					inIncr = true
					continue
				}
				return r.Value, true
			case xproto.PropertyNotifyEvent:
				if !inIncr || e.State != xproto.PropertyNewValue || e.Atom != prop {
					continue
				}
				r, err := xproto.GetProperty(x, true, win, prop, xproto.GetPropertyTypeAny, 0, 1<<24).Reply()
				if err != nil {
					t.Fatal(err)
				}
				if len(r.Value) == 0 {
					return out, true
				}
				out = append(out, r.Value...)
			}
		}
	}
}

func buildLectern(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "lectern")
	cmd := exec.Command("go", "build", "-o", bin, "github.com/JeremiahM37/lectern/v2/cmd/lectern")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, out)
	}
	return bin
}

func TestHeadlessClipboardServesNativeReaders(t *testing.T) {
	if _, err := exec.LookPath("Xvfb"); err != nil {
		t.Skip("Xvfb is not installed")
	}
	bin := buildLectern(t)
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("lcb-%d", os.Getpid()))
	t.Cleanup(func() {
		exec.Command("pkill", "-f", dir).Run()
		os.RemoveAll(dir)
	})
	env, err := Ensure(dir, bin)
	if err != nil {
		t.Fatal(err)
	}
	// ensure is idempotent: the second call finds the same server
	again, err := Ensure(dir, bin)
	if err != nil || again != env {
		t.Fatalf("second ensure: %v %v", again, err)
	}
	// the display refuses a client without the cookie
	os.Setenv("XAUTHORITY", filepath.Join(t.TempDir(), "none"))
	if x, err := xgb.NewConnDisplay(env.Display); err == nil {
		if _, err := xproto.InternAtom(x, false, 4, "TEST").Reply(); err == nil {
			t.Fatal("a client without the cookie was served")
		}
		x.Close()
	}

	if _, ok := nativeRead(t, env.Display, env.Xauthority, "image/png"); ok {
		t.Fatal("empty clipboard answered")
	}
	for _, size := range []int{1000, 150000, 1 << 20, 5 << 20} {
		img := make([]byte, size)
		rand.Read(img)
		copy(img, "\x89PNG\r\n\x1a\n")
		if err := Set(dir, "image/png", img); err != nil {
			t.Fatal(err)
		}
		got, ok := nativeRead(t, env.Display, env.Xauthority, "image/png")
		if !ok || !bytes.Equal(got, img) {
			t.Fatalf("%d bytes: ok=%v len=%d", size, ok, len(got))
		}
		if _, ok := nativeRead(t, env.Display, env.Xauthority, "text/plain"); ok {
			t.Fatal("served text for an image")
		}
	}
	// the TARGETS list names what is there
	tg, ok := nativeRead(t, env.Display, env.Xauthority, "TARGETS")
	if !ok || len(tg)%4 != 0 || len(tg) < 12 {
		t.Fatalf("targets: %v %d", ok, len(tg))
	}
	_ = binary.LittleEndian
	// text, then clear
	Set(dir, "text/plain", []byte("hello"))
	if got, ok := nativeRead(t, env.Display, env.Xauthority, "UTF8_STRING"); !ok || string(got) != "hello" {
		t.Fatalf("text: %q %v", got, ok)
	}
	if err := Set(dir, "", nil); err != nil {
		t.Fatal(err)
	}
	if _, ok := nativeRead(t, env.Display, env.Xauthority, "UTF8_STRING"); ok {
		t.Fatal("cleared clipboard still answered")
	}
}
