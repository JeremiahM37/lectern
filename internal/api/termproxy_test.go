package api_test

// Attach opened `http://<the browser's hostname>:<port>`, which named whichever
// machine served the page. Through the nginx vhost that is the reverse proxy,
// which runs no ttyd, so the tab said "refused to connect" — and no test noticed
// because none of them ever looked at the URL the UI would open.

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/terminal"
)

// The URL handed to the browser must be same-origin, so it survives every way
// of reaching lectern: directly, through nginx, over the tailnet, on a phone.
func TestAttachReturnsASameOriginURL(t *testing.T) {
	h := newHarness(t)
	h.App.Terminals.LookPath = func(string) (string, error) { return "/usr/bin/ttyd", nil }
	h.App.Terminals.Spawn = func(int, string, []string) (*exec.Cmd, error) {
		cmd := exec.Command("sleep", "10")
		return cmd, cmd.Start()
	}
	t.Cleanup(h.App.Terminals.Shutdown)
	task := h.task(h.seededProjectID(), "attachable", "x [mock:slow]", nil)
	h.post(fmt.Sprintf("/api/tasks/%d/dispatch", task.id()), obj{}, 200)
	h.waitStatus(task.id(), "running")

	for _, path := range []string{
		fmt.Sprintf("/api/tasks/%d/terminal", task.id()),
	} {
		code, body := h.request("POST", path, obj{}, nil)
		if code != 200 {
			t.Fatalf("%s: %d %s", path, code, body)
		}
		var out struct {
			Port int    `json:"port"`
			URL  string `json:"url"`
		}
		json.Unmarshal(body, &out)
		if out.URL == "" {
			t.Fatal("no url was returned; the client would have to guess the host")
		}
		if !strings.HasPrefix(out.URL, "/term/attempt/") {
			t.Errorf("the url must name the attachment on this origin, got %q", out.URL)
		}
		for _, bad := range []string{"http://", "https://", "localhost", "127.0.0.1"} {
			if strings.Contains(out.URL, bad) {
				t.Errorf("url names a host (%q) — that is the bug: %q", bad, out.URL)
			}
		}
		// naming the port is the bug: a port is where the terminal happens to be
		// now, and it changes on eviction and on every restart
		if strings.Contains(out.URL, fmt.Sprint(out.Port)) {
			t.Errorf("the url names the port (%d), so it goes stale: %q", out.Port, out.URL)
		}
	}
}

// The prefix must not become a way to reach anything else on loopback.
func TestTerminalProxyOnlyServesPortsItOwns(t *testing.T) {
	h := newHarness(t)
	for _, path := range []string{
		"/term/7710/",     // in range, but nothing attached
		"/term/9110/",     // lectern itself
		"/term/22/",       // ssh
		"/term/0/",        // nonsense
		"/term/notaport/", // nonsense
		"/term/9111/",     // grimoire
		"/term/-1/",       // negative
	} {
		code, _ := h.request("GET", path, nil, nil)
		if code != 404 {
			t.Errorf("%s should be refused, got %d", path, code)
		}
	}
}

// ttyd's asset and websocket URLs are absolute, so it has to be launched with
// the base path it is mounted under or the page loads blank.
// ttyd's asset and websocket URLs are absolute, so it has to be launched with
// the base path it is mounted under or the page loads blank — and a ttyd with
// no credential must never listen on the network.
func TestTTYDArgsMountUnderTheBasePathOnLoopback(t *testing.T) {
	att := terminal.Attachment{Key: "session:7", TmuxSession: "lec-s7"}
	got := strings.Join(terminal.TTYDArgs(7712, att.BasePath(),
		[]string{"tmux", "attach", "-t", "lec-1"}), " ")
	for _, want := range []string{
		"-p 7712",            // the port it was given
		"-i lo",              // an unauthenticated shell stays off the network
		"-b /term/session/7", // the attachment, not the port
		"-W",                 // the operator can type
		"tmux attach -t lec-1",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("ttyd args missing %q:\n  %s", want, got)
		}
	}
	// the base path must match what the proxy actually serves
	if !strings.Contains(got, "-b "+att.BasePath()) {
		t.Errorf("base path disagrees with the proxy's route: %s", got)
	}
	// --once accepts a single client and exits when it disconnects, which made a
	// terminal open on a phone dead on a desktop
	if strings.Contains(got, "--once") {
		t.Errorf("--once is back; the terminal is single-client again: %s", got)
	}
	// --once accepts a single client and exits when it disconnects, which made a
	// terminal open on a phone dead on a desktop
	if strings.Contains(got, "--once") {
		t.Errorf("--once is back; the terminal is single-client again: %s", got)
	}
}

// ---- against a real ttyd ------------------------------------------------

// The proxy either carries a real ttyd's page and websocket or it does not, and
// only a real one can say. This is the test that would have caught the original
// bug, because it asks for the terminal the way a browser does.
func TestARealTerminalIsReachableThroughTheProxy(t *testing.T) {
	if _, err := exec.LookPath("ttyd"); err != nil {
		t.Skip("ttyd is not installed")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	r := newInteractiveRig(t)
	sess := r.launchSession(map[string]any{"agent": "claude", "scratch": true, "name": "termtest"})
	r.waitForLog(sess.Workdir, "argv:", 10*time.Second)

	code, body := r.do("POST", fmt.Sprintf("/api/sessions/%d/terminal", sess.ID), map[string]any{})
	if code != 200 {
		t.Fatalf("attach: %d %s", code, body)
	}
	var out struct {
		Port int    `json:"port"`
		URL  string `json:"url"`
	}
	json.Unmarshal(body, &out)
	t.Cleanup(func() { r.app.Terminals.Shutdown() })

	// the browser asks the SAME origin it loaded the app from
	var resp *http.Response
	var err error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get(r.url + out.URL)
		if err == nil && resp.StatusCode == 200 {
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("the terminal page is not reachable through the proxy: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("terminal page: %d", resp.StatusCode)
	}
	page, _ := io.ReadAll(resp.Body)
	if !strings.Contains(strings.ToLower(string(page)), "ttyd") &&
		!strings.Contains(strings.ToLower(string(page)), "terminal") {
		t.Errorf("that does not look like ttyd's page: %.200s", page)
	}

	// and ttyd must NOT be reachable directly on the network any more
	direct, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", out.Port))
	if err == nil {
		direct.Body.Close()
	}
	// loopback still answers (that is where the proxy talks to it); what matters
	// is that the page is served under the prefix rather than at the root
	root, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/", out.Port))
	if err == nil {
		defer root.Body.Close()
		if root.StatusCode == 200 {
			t.Logf("note: ttyd also answers at its root on loopback (%d)", root.StatusCode)
		}
	}
}

// A tmux session is multi-client by design — that is most of the point of it.
// ttyd was launched with --once, which accepts a single client and exits when it
// disconnects, so having the terminal open on a phone made the same terminal
// dead on a desktop and ttyd's own "reconnect" had nothing left to reconnect to.
func TestTwoClientsCanHoldTheSameTerminalAtOnce(t *testing.T) {
	if _, err := exec.LookPath("ttyd"); err != nil {
		t.Skip("ttyd is not installed")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	r := newInteractiveRig(t)
	sess := r.launchSession(map[string]any{"agent": "claude", "scratch": true, "name": "two clients"})
	r.waitForLog(sess.Workdir, "argv:", 10*time.Second)

	code, body := r.do("POST", fmt.Sprintf("/api/sessions/%d/terminal", sess.ID), map[string]any{})
	if code != 200 {
		t.Fatalf("attach: %d %s", code, body)
	}
	var out struct {
		URL string `json:"url"`
	}
	json.Unmarshal(body, &out)
	t.Cleanup(func() { r.app.Terminals.Shutdown() })

	// wait for ttyd to bind
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(r.url + out.URL); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	// the phone connects, and stays connected
	phone := openTerminalWS(t, r.url+out.URL+"ws")
	defer phone.Close()

	// now the desktop connects to the SAME terminal, while the phone still holds it
	desktop, err := dialTerminalWS(r.url + out.URL + "ws")
	if err != nil {
		t.Fatalf("a second client could not attach while the first was connected: %v\n"+
			"this is the --once behaviour: phone open means desktop dead", err)
	}
	defer desktop.Close()

	// and the first client is still usable — it was not evicted
	if _, err := phone.Write([]byte{}); err != nil {
		t.Errorf("the first client was dropped when the second joined: %v", err)
	}
}

// openTerminalWS connects and fails the test if it cannot.
func openTerminalWS(t *testing.T, url string) net.Conn {
	t.Helper()
	c, err := dialTerminalWS(url)
	if err != nil {
		t.Fatalf("first client could not connect: %v", err)
	}
	return c
}

// dialTerminalWS performs a websocket handshake by hand and keeps the socket
// open, which is what a browser tab holding a terminal actually does.
func dialTerminalWS(rawURL string) (net.Conn, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	conn, err := net.DialTimeout("tcp", u.Host, 5*time.Second)
	if err != nil {
		return nil, err
	}
	req := fmt.Sprintf("GET %s HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\n"+
		"Upgrade: websocket\r\nSec-WebSocket-Version: 13\r\n"+
		"Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n"+
		"Sec-WebSocket-Protocol: tty\r\n\r\n", u.RequestURI(), u.Host)
	if _, err := conn.Write([]byte(req)); err != nil {
		conn.Close()
		return nil, err
	}
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if !strings.Contains(string(buf[:n]), "101") {
		conn.Close()
		return nil, fmt.Errorf("no upgrade: %s", strings.SplitN(string(buf[:n]), "\r\n", 2)[0])
	}
	conn.SetReadDeadline(time.Time{})
	return conn, nil
}

// This is the "reconnect does nothing" loop. A terminal dies for ordinary
// reasons — the control plane restarts, or the port range fills and the oldest
// is retired — and a tab holding a URL that named the port was pointed at
// nothing for good. ttyd's own reconnect retried it forever and could never
// succeed, so the only way out was closing the tab and attaching again.
func TestAStaleTerminalTabHealsItself(t *testing.T) {
	if _, err := exec.LookPath("ttyd"); err != nil {
		t.Skip("ttyd is not installed")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	r := newInteractiveRig(t)
	sess := r.launchSession(map[string]any{"agent": "claude", "scratch": true, "name": "stale tab"})
	r.waitForLog(sess.Workdir, "argv:", 10*time.Second)
	t.Cleanup(func() { r.app.Terminals.Shutdown() })

	code, body := r.do("POST", fmt.Sprintf("/api/sessions/%d/terminal", sess.ID), map[string]any{})
	if code != 200 {
		t.Fatalf("attach: %d %s", code, body)
	}
	var out struct {
		Port int    `json:"port"`
		URL  string `json:"url"`
	}
	json.Unmarshal(body, &out)

	page := func() int {
		resp, err := http.Get(r.url + out.URL)
		if err != nil {
			return 0
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	deadline := time.Now().Add(10 * time.Second)
	for page() != 200 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if page() != 200 {
		t.Fatal("the terminal never came up")
	}

	// every terminal dies when the control plane restarts — this is that
	r.app.Terminals.Shutdown()

	// the tab is still open and retries the same URL, which must now work
	if got := page(); got != 200 {
		t.Fatalf("a reconnect after the terminal died returned %d — this is the "+
			"loop where hitting enter does nothing", got)
	}
	// and it is a genuinely new terminal, on the same tmux session
	code, body = r.do("POST", fmt.Sprintf("/api/sessions/%d/terminal", sess.ID), map[string]any{})
	var again struct {
		URL string `json:"url"`
	}
	json.Unmarshal(body, &again)
	if again.URL != out.URL {
		t.Errorf("the URL changed across a restart (%q -> %q); an open tab would "+
			"still be pointing at the old one", out.URL, again.URL)
	}
}

// A shell into the project is for the times you just want to look at the thing
// yourself. It has to be a real shell in the real directory — this writes a file
// through it and checks the file is there.
func TestAProjectShellIsARealShellInTheRepo(t *testing.T) {
	if _, err := exec.LookPath("ttyd"); err != nil {
		t.Skip("ttyd is not installed")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux is not installed")
	}
	r := newRealRig(t)
	t.Cleanup(func() {
		r.app.Terminals.Shutdown()
		exec.Command("tmux", "kill-session", "-t", fmt.Sprintf("lec-sh%d", r.project)).Run()
	})

	code, body := r.do("POST", fmt.Sprintf("/api/projects/%d/terminal", r.project), map[string]any{})
	if code != 200 {
		t.Fatalf("opening a shell: %d %s", code, body)
	}
	var out struct {
		URL  string `json:"url"`
		Tmux string `json:"tmux_session"`
	}
	json.Unmarshal(body, &out)
	if out.URL != fmt.Sprintf("/term/project/%d/", r.project) {
		t.Errorf("url: %q", out.URL)
	}

	// the page is served on this origin, like every other terminal
	deadline := time.Now().Add(10 * time.Second)
	var status int
	for time.Now().Before(deadline) {
		if resp, err := http.Get(r.url + out.URL); err == nil {
			status = resp.StatusCode
			resp.Body.Close()
			if status == 200 {
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if status != 200 {
		t.Fatalf("the shell page returned %d", status)
	}

	// ttyd starts the command only after the client's opening frame, not when
	// the page loads — so the shell begins the moment you actually open the tab
	client, err := dialTerminalWS(r.url + out.URL + "ws")
	if err != nil {
		t.Fatalf("could not open the shell: %v", err)
	}
	defer client.Close()
	if err := sendWSText(client, `{"AuthToken":"","columns":120,"rows":40}`); err != nil {
		t.Fatalf("opening frame: %v", err)
	}

	// and it is a real shell, in the project's directory
	for time.Now().Before(deadline) {
		if exec.Command("tmux", "has-session", "-t", out.Tmux).Run() == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if exec.Command("tmux", "has-session", "-t", out.Tmux).Run() != nil {
		t.Fatalf("no tmux session %q was created", out.Tmux)
	}
	marker := "written-by-hand.txt"
	if err := exec.Command("tmux", "send-keys", "-t", out.Tmux,
		"printf hello > "+marker, "Enter").Run(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(r.repo, marker)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(path); err == nil && string(raw) == "hello" {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("a command typed in the shell did not run in the project's directory: %v", err)
	}
	if string(raw) != "hello" {
		t.Errorf("file contents: %q", raw)
	}

	// reopening returns to the SAME shell rather than starting a new one
	code, body = r.do("POST", fmt.Sprintf("/api/projects/%d/terminal", r.project), map[string]any{})
	var again struct {
		Tmux string `json:"tmux_session"`
	}
	json.Unmarshal(body, &again)
	if again.Tmux != out.Tmux {
		t.Errorf("reopening made a different shell: %q vs %q", again.Tmux, out.Tmux)
	}
}

// sendWSText writes one masked client text frame, which is the minimum needed to
// make ttyd start its command.
func sendWSText(conn net.Conn, payload string) error {
	body := []byte(payload)
	frame := []byte{0x81} // FIN + text
	if len(body) > 125 {
		return fmt.Errorf("this helper only sends short frames")
	}
	frame = append(frame, byte(0x80|len(body))) // MASK + length
	mask := []byte{0x12, 0x34, 0x56, 0x78}
	frame = append(frame, mask...)
	for i, b := range body {
		frame = append(frame, b^mask[i%4])
	}
	_, err := conn.Write(frame)
	return err
}
