package computer

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/desktop"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
)

// fakeDesktop answers every desktop script with OK, and a screenshot with a
// fixed PNG. It records the scripts so a test can see what would have run.
type fakeDesktop struct {
	mu      sync.Mutex
	scripts []string
}

func (f *fakeDesktop) run(_ context.Context, script string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, script)
	if strings.Contains(script, "lectern-desktop-screenshot") {
		return "PNG " + base64.StdEncoding.EncodeToString([]byte("\x89PNG-fake")) + "\n", nil
	}
	return "OK\n", nil
}

func (f *fakeDesktop) ran(sub string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.scripts {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// fakeHost answers approvals with one decision and records each question.
type fakeHost struct {
	tools.Host
	decision tools.Decision
	asked    []tools.Approval
}

func (h *fakeHost) Approve(_ context.Context, a tools.Approval) tools.Decision {
	h.asked = append(h.asked, a)
	return h.decision
}

func newTestRunner(f *fakeDesktop) *Runner {
	return &Runner{
		run: f.run,
		d:   &desktop.Desktop{Dir: "/tmp/lectern-live-abc123", Display: 99, Width: Width, Height: Height},
	}
}

func call(t *testing.T, r *Runner, h tools.Host, in string) tools.Result {
	t.Helper()
	return r.Run(context.Background(), h, json.RawMessage(in))
}

func TestScreenshotReturnsTheImageWithoutAsking(t *testing.T) {
	f := &fakeDesktop{}
	h := &fakeHost{decision: tools.Deny}
	res := call(t, newTestRunner(f), h, `{"action":"screenshot"}`)
	if res.IsError || string(res.Image) != "\x89PNG-fake" || res.ImageType != "image/png" {
		t.Fatalf("screenshot = %+v", res)
	}
	if len(h.asked) != 0 {
		t.Fatalf("a screenshot must not ask: %v", h.asked)
	}
}

func TestClickIsRefusedWhenTheUserSaysNo(t *testing.T) {
	f := &fakeDesktop{}
	h := &fakeHost{decision: tools.Deny}
	res := call(t, newTestRunner(f), h, `{"action":"left_click","coordinate":[10,20]}`)
	if !res.IsError || !strings.Contains(res.Output, "declined") {
		t.Fatalf("declined click = %+v", res)
	}
	if f.ran("mousemove") {
		t.Fatal("a declined click must not reach the desktop")
	}
}

func TestAllowSessionStopsAskingForInput(t *testing.T) {
	f := &fakeDesktop{}
	h := &fakeHost{decision: tools.AllowSession}
	r := newTestRunner(f)
	if res := call(t, r, h, `{"action":"left_click","coordinate":[10,20]}`); res.IsError {
		t.Fatalf("click = %+v", res)
	}
	if res := call(t, r, h, `{"action":"type","text":"hi"}`); res.IsError {
		t.Fatalf("type = %+v", res)
	}
	if len(h.asked) != 1 {
		t.Fatalf("asked %d times, want once for the session", len(h.asked))
	}
	if !f.ran("mousemove --sync 10 20 click 1") || !f.ran("xdotool type") {
		t.Fatal("approved actions did not run")
	}
}

func TestScrollDefaultsToTheCentreAndThreeClicks(t *testing.T) {
	f := &fakeDesktop{}
	res := call(t, newTestRunner(f), &fakeHost{decision: tools.AllowOnce}, `{"action":"scroll","scroll_direction":"down"}`)
	if res.IsError {
		t.Fatalf("scroll = %+v", res)
	}
	if !f.ran("mousemove --sync 512 384 click --repeat 3 5") {
		t.Fatal("scroll did not use the centre and default amount")
	}
}

func TestBadInputIsAnErrorNotACrash(t *testing.T) {
	r := newTestRunner(&fakeDesktop{})
	h := &fakeHost{decision: tools.AllowOnce}
	for _, in := range []string{`{`, `{"action":""}`, `{"action":"left_click"}`, `{"action":"drag"}`, `{"action":"type"}`} {
		if res := call(t, r, h, in); !res.IsError {
			t.Fatalf("%s: want an error, got %+v", in, res)
		}
	}
}

func TestWaitIsCappedAndCancellable(t *testing.T) {
	r := newTestRunner(&fakeDesktop{})
	start := time.Now()
	if res := call(t, r, nil, `{"action":"wait","duration":0.01}`); res.IsError {
		t.Fatalf("wait = %+v", res)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("a short wait took too long")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res := r.Run(ctx, nil, json.RawMessage(`{"action":"wait","duration":5}`))
	if !res.IsError {
		t.Fatalf("a cancelled wait must report an error: %+v", res)
	}
}

func TestToActionNamesEveryAction(t *testing.T) {
	for _, in := range []input{
		{Action: "right_click", Coordinate: []int{1, 2}},
		{Action: "double_click", Coordinate: []int{1, 2}},
		{Action: "mouse_move", Coordinate: []int{1, 2}},
		{Action: "key", Text: "ctrl+l"},
	} {
		if _, _, err := toAction(in); err != nil {
			t.Fatalf("%s: %v", in.Action, err)
		}
	}
	if _, _, err := toAction(input{Action: "left_click_drag"}); err == nil {
		t.Fatal("an unsupported action must be refused")
	}
}
