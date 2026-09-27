package browser

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixturePage is a small app with the things an agent and Design Mode touch:
// a form, a button that changes the page, a styled card, console output.
const fixturePage = `<!doctype html><html><head><title>Fixture app</title>
<style>
body{margin:0;font-family:sans-serif}
.card{position:absolute;left:40px;top:60px;width:200px;height:100px;background:rgb(255,0,0);border-radius:8px;padding:0}
#long{margin-top:400px}
</style></head><body>
<h1>Fixture heading</h1>
<div class="card" id="card"><span>Red card</span></div>
<form id="f" style="margin-top:200px">
  <label>Email <input id="email" name="email" type="text"></label>
  <label>Secret <input id="secret" type="password" value="hunter2"></label>
  <select id="size"><option value="s">Small</option><option value="l">Large</option></select>
  <button type="button" id="go" onclick="document.getElementById('out').textContent='clicked '+document.getElementById('email').value;console.log('go pressed')">Go</button>
</form>
<p id="out">idle</p>
<div id="long">` + "<script>console.warn('fixture loaded');fetch('/api/ping')</script>" + `</div>
</body></html>`

func fixtureServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/ping":
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
		case "/other":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(`<title>Other page</title><p>second</p>`))
		default:
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte(fixturePage))
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func localRun(ctx context.Context, script string) (string, error) {
	out, err := exec.CommandContext(ctx, "sh", "-c", script).Output()
	return string(out), err
}

func localDial(ctx context.Context, addr string) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
}

func testOwner() string {
	raw := make([]byte, 8)
	_, _ = rand.Read(raw)
	return hex.EncodeToString(raw)
}

// startBrowser launches a real headless browser, or skips when the machine
// has none.
func startBrowser(t *testing.T, vp Viewport) *Browser {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	proc, err := Launch(ctx, localRun, testOwner(), LaunchOptions{Width: vp.Width, Height: vp.Height})
	if err == ErrNoBrowser {
		t.Skip("no Chromium on this machine")
	}
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = Stop(c, localRun, proc.Dir)
	})
	b, err := Open(ctx, localDial, proc, vp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(b.Close)
	return b
}

func TestAgentDrivesAPageThroughSnapshotRefs(t *testing.T) {
	srv := fixtureServer(t)
	b := startBrowser(t, Viewport{Width: 900, Height: 700})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	st, err := b.Navigate(ctx, srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if st.Title != "Fixture app" || !strings.HasPrefix(st.URL, srv.URL) {
		t.Fatalf("state after navigate: %+v", st)
	}
	snap, err := b.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`heading "Fixture heading"`, `button "Go"`, `textbox "Email"`} {
		if !strings.Contains(snap.Tree, want) {
			t.Fatalf("snapshot lacks %q:\n%s", want, snap.Tree)
		}
	}
	ref := func(label string) int {
		for _, line := range strings.Split(snap.Tree, "\n") {
			if strings.Contains(line, label) {
				var n int
				if i := strings.Index(line, "[ref="); i >= 0 {
					_, _ = fmtSscan(line[i+5:], &n)
					return n
				}
			}
		}
		t.Fatalf("no ref for %q in\n%s", label, snap.Tree)
		return 0
	}
	if _, err := b.Fill(ctx, Target{Ref: ref(`textbox "Email"`)}, "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Fill(ctx, Target{Selector: "#size"}, "Large"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Click(ctx, Target{Ref: ref(`button "Go"`)}); err != nil {
		t.Fatal(err)
	}
	out, err := b.Evaluate(ctx, "document.querySelector('#out').textContent + '|' + document.querySelector('#size').value")
	if err != nil {
		t.Fatal(err)
	}
	if out != "clicked ada@example.com|l" {
		t.Fatalf("page after click: %v", out)
	}
	// Keys reach the focused field.
	if _, err := b.Click(ctx, Target{Selector: "#email"}); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"End", "!", "Backspace", "?"} {
		if err := b.Press(ctx, k); err != nil {
			t.Fatal(err)
		}
	}
	if v, _ := b.Evaluate(ctx, "document.querySelector('#email').value"); v != "ada@example.com?" {
		t.Fatalf("typed value: %v", v)
	}
	// Evaluate is read-only.
	if _, err := b.Evaluate(ctx, "document.body.innerHTML = 'gone'"); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("a write through evaluate was not refused: %v", err)
	}
	if v, _ := b.Evaluate(ctx, "document.querySelector('h1').textContent"); v != "Fixture heading" {
		t.Fatalf("the page changed: %v", v)
	}
	// Console and network were recorded.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if len(b.Console()) >= 2 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	var logs []string
	for _, e := range b.Console() {
		logs = append(logs, e.Level+":"+e.Text)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "warning:fixture loaded") || !strings.Contains(strings.Join(logs, "\n"), "log:go pressed") {
		t.Fatalf("console: %v", logs)
	}
	var sawPing bool
	for _, n := range b.Network() {
		if strings.HasSuffix(n.URL, "/api/ping") && n.Status == 200 {
			sawPing = true
		}
	}
	if !sawPing {
		t.Fatalf("network log lacks the fetch: %+v", b.Network())
	}
	// History works.
	if _, err := b.Navigate(ctx, srv.URL+"/other"); err != nil {
		t.Fatal(err)
	}
	if st, _ := b.History(ctx, -1); st.Title != "Fixture app" {
		t.Fatalf("back: %+v", st)
	}
	if _, err := b.Navigate(ctx, "file:///etc/passwd"); err == nil {
		t.Fatal("a file URL was accepted")
	}
}

func fmtSscan(s string, n *int) (int, error) {
	v := 0
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		v = v*10 + int(s[i]-'0')
		i++
	}
	*n = v
	return i, nil
}

// Design Mode's payload and its cropped screenshot, against a known page: a
// 200x100 red card at (40,60).
func TestDesignPayloadAndCroppedScreenshot(t *testing.T) {
	srv := fixtureServer(t)
	b := startBrowser(t, Viewport{Width: 800, Height: 600})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := b.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	raw, err := b.DescribeSelector(ctx, "#card")
	if err != nil {
		t.Fatal(err)
	}
	var d struct {
		Selector string            `json:"selector"`
		HTML     string            `json:"html"`
		CSS      map[string]string `json:"css"`
		Rules    []string          `json:"rules"`
		Rect     Clip              `json:"rect"`
		Scroll   struct{ X, Y float64 }
		URL      string `json:"url"`
		Viewport struct {
			Width int `json:"width"`
		} `json:"viewport"`
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d.Selector != "#card" || !strings.Contains(d.HTML, "Red card") || d.Viewport.Width != 800 || !strings.HasPrefix(d.URL, srv.URL) {
		t.Fatalf("payload: %s", raw)
	}
	if d.CSS["background-color"] != "rgb(255, 0, 0)" || d.CSS["position"] != "absolute" || d.CSS["border-top-left-radius"] != "8px" {
		t.Fatalf("css diff misses the card's own styles: %v", d.CSS)
	}
	if _, ok := d.CSS["right"]; ok {
		t.Fatalf("css diff includes an offset nobody wrote: %v", d.CSS)
	}
	if d.CSS["left"] != "40px" || d.CSS["top"] != "60px" {
		t.Fatalf("css diff lost the authored offsets: %v", d.CSS)
	}
	if _, ok := d.CSS["font-style"]; ok {
		t.Fatalf("css diff includes a default value: %v", d.CSS)
	}
	if len(d.Rules) == 0 || !strings.Contains(d.Rules[0], ".card") {
		t.Fatalf("matched rules: %v", d.Rules)
	}
	png1, err := b.Screenshot(ctx, &Clip{X: d.Rect.X + d.Scroll.X, Y: d.Rect.Y + d.Scroll.Y, Width: d.Rect.Width, Height: d.Rect.Height})
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(png1))
	if err != nil {
		t.Fatal(err)
	}
	if w, h := img.Bounds().Dx(), img.Bounds().Dy(); w != 200 || h != 100 {
		t.Fatalf("crop is %dx%d, want 200x100", w, h)
	}
	// The middle of the crop is the card's red, not the page around it.
	r, g, bl, _ := img.At(100, 70).RGBA()
	if r>>8 < 240 || g>>8 > 20 || bl>>8 > 20 {
		t.Fatalf("crop middle is not the red card: %d,%d,%d", r>>8, g>>8, bl>>8)
	}

	// Markup is trimmed: a password value never leaves the page.
	raw, err = b.DescribeSelector(ctx, "#f")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "hunter2") || strings.Contains(string(raw), "onclick") {
		t.Fatalf("form payload leaks a secret or a handler: %s", raw)
	}
	if _, err := b.DescribeSelector(ctx, "#nope"); err != ErrNotFound {
		t.Fatalf("missing element: %v", err)
	}
}

func TestDesignModePicksThroughTheLiveBrowser(t *testing.T) {
	srv := fixtureServer(t)
	b := startBrowser(t, Viewport{Width: 800, Height: 600})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := b.Navigate(ctx, srv.URL+"/"); err != nil {
		t.Fatal(err)
	}
	frames, updates, stop := b.Watch(ctx)
	defer stop()
	select {
	case f := <-frames:
		if len(f.JPEG) < 100 {
			t.Fatal("empty frame")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no frame for a new watcher")
	}
	if err := b.SetDesign(ctx, true); err != nil {
		t.Fatal(err)
	}
	// A click on the card selects it instead of reaching the page.
	if err := b.ClickAt(ctx, 100, 100); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case u := <-updates:
			if u.Kind != "design" {
				continue
			}
			var msg struct {
				Type    string `json:"type"`
				Element struct {
					Selector string `json:"selector"`
				} `json:"element"`
			}
			_ = json.Unmarshal(u.Design, &msg)
			if msg.Type != "select" {
				continue
			}
			if msg.Element.Selector != "#card" && !strings.HasPrefix(msg.Element.Selector, "#card") {
				t.Fatalf("selected %q", msg.Element.Selector)
			}
			goto selected
		case <-deadline:
			t.Fatal("no design selection arrived")
		}
	}
selected:
	// Navigation keeps the picker while Design Mode is on, and it goes away when off.
	if _, err := b.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := b.Evaluate(ctx, "typeof window.__lecternDesign"); v != "object" {
		t.Fatalf("picker after reload: %v", v)
	}
	if err := b.SetDesign(ctx, false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if v, _ := b.Evaluate(ctx, "typeof window.__lecternDesign"); v != "undefined" {
		t.Fatalf("picker survived Design Mode off: %v", v)
	}
}

func TestParseKey(t *testing.T) {
	for combo, want := range map[string]string{"Enter": "Enter/0", "Control+a": "a/2", "Shift+Tab": "Tab/8", "+": "+/0"} {
		def, mods, err := parseKey(combo)
		if err != nil {
			t.Fatalf("%s: %v", combo, err)
		}
		if got := def.key + "/" + string(rune('0'+mods)); got != want {
			t.Fatalf("%s: got %s want %s", combo, got, want)
		}
	}
	if _, _, err := parseKey("Hyper+x"); err == nil {
		t.Fatal("unknown modifier accepted")
	}
}

// A browser on the control plane stands in for a target with none: through the
// loopback proxy, its localhost is the target's.
func TestHostBrowserSeesTheTargetsLocalhost(t *testing.T) {
	srv := fixtureServer(t)
	// A port nothing on this machine listens on; only the proxy can make it answer.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	closed := l.Addr().(*net.TCPAddr).Port
	l.Close()
	proxy, err := StartLoopbackProxy(func(ctx context.Context, addr string) (net.Conn, error) {
		if addr != fmt.Sprintf("127.0.0.1:%d", closed) {
			return nil, fmt.Errorf("unexpected %s", addr)
		}
		return localDial(ctx, srv.Listener.Addr().String())
	})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	proc, err := Launch(ctx, localRun, testOwner(), LaunchOptions{ProxyPort: proxy.Port})
	if err == ErrNoBrowser {
		t.Skip("no Chromium on this machine")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), localRun, proc.Dir)
	b, err := Open(ctx, localDial, proc, Viewport{})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	st, err := b.Navigate(ctx, fmt.Sprintf("http://localhost:%d/", closed))
	if err != nil || st.Title != "Fixture app" {
		t.Fatalf("host browser via proxy: %+v %v", st, err)
	}
}

// Two servers can share a machine: a browser another server still keeps alive
// survives a launch; one abandoned for 5 minutes is reaped.
func TestLaunchReapsOnlyAbandonedBrowsers(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	first, err := Launch(ctx, localRun, testOwner(), LaunchOptions{})
	if err == ErrNoBrowser {
		t.Skip("no Chromium on this machine")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), localRun, first.Dir)
	second, err := Launch(ctx, localRun, testOwner(), LaunchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), localRun, second.Dir)
	if _, err := os.Stat(first.Dir); err != nil {
		t.Fatalf("a live browser of another server was reaped: %v", err)
	}
	if err := KeepAlive(ctx, localRun, first.Dir); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(filepath.Join(first.Dir, "alive"), old, old); err != nil {
		t.Fatal(err)
	}
	third, err := Launch(ctx, localRun, testOwner(), LaunchOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer Stop(context.Background(), localRun, third.Dir)
	if _, err := os.Stat(first.Dir); !os.IsNotExist(err) {
		t.Fatalf("an abandoned browser survived: %v", err)
	}
	if _, err := os.Stat(second.Dir); err != nil {
		t.Fatalf("the kept browser was reaped: %v", err)
	}
}
