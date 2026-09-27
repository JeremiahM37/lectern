package api_test

// The session browser, Design Mode's send and the agent browser tools, against
// a real headless Chromium on a real local target (docs/browser.md). Each test
// runs a private server with its own database and tmux; none reaches the live
// service.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image/png"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/store"
)

const designFixture = `<!doctype html><html><head><title>Fixture shop</title><style>
body{margin:0;font-family:sans-serif}
#card{position:absolute;left:30px;top:50px;width:160px;height:90px;background:rgb(0,0,255);border-radius:6px}
</style></head><body>
<h1>Fixture shop</h1>
<div id="card" class="card">Blue card</div>
<form style="margin-top:180px"><label>Name <input id="name"></label>
<button type="button" id="save" onclick="document.querySelector('#msg').textContent='saved '+document.querySelector('#name').value;console.log('saved')">Save</button></form>
<p id="msg">unsaved</p></body></html>`

// needChromium skips where no browser can be started.
func needChromium(t *testing.T) {
	t.Helper()
	for _, b := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if _, err := exec.LookPath(b); err == nil {
			return
		}
	}
	matches, _ := filepath.Glob(filepath.Join(os.Getenv("HOME"), ".cache/ms-playwright/chromium*/chrome-linux*/*"))
	if len(matches) == 0 {
		t.Skip("no Chromium on this machine")
	}
}

type browserFixture struct {
	h       *harness
	sess    *store.Session
	dir     string
	appPort int
}

func newBrowserFixture(t *testing.T, tweak ...func(*config.Config)) *browserFixture {
	t.Helper()
	requireRealTools(t)
	needChromium(t)
	h := newHarness(t, append([]func(*config.Config){func(c *config.Config) { c.Mock = false }}, tweak...)...)
	dir := t.TempDir()
	mustRun(t, "", "git", "init", "-q", dir)
	target, err := h.App.DB.InsertTarget(&store.Target{Name: "browser-local", Kind: "local", MaxConcurrent: 1})
	if err != nil {
		t.Fatal(err)
	}
	// The stand-in agent: a shell that writes whatever is typed into it to a file.
	tmuxName := fmt.Sprintf("browser-%d", time.Now().UnixNano())
	mustRun(t, dir, "tmux", "new-session", "-d", "-s", tmuxName, "bash --norc")
	mustRun(t, dir, "tmux", "send-keys", "-t", tmuxName, "cat > received.txt", "Enter")
	sess, err := h.App.DB.InsertSession(&store.Session{TargetID: target.ID, Name: "browser", Agent: "claude", Workdir: dir,
		TmuxSession: tmuxName, Status: "idle", Origin: "adopted"})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	app := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, designFixture)
	})}
	go app.Serve(ln)
	t.Cleanup(func() {
		app.Close()
		h.request("DELETE", fmt.Sprintf("/api/sessions/%d/browser", sess.ID), nil, nil)
	})
	return &browserFixture{h: h, sess: sess, dir: dir, appPort: ln.Addr().(*net.TCPAddr).Port}
}

func (f *browserFixture) tool(t *testing.T, name string, args obj) (string, []map[string]any) {
	t.Helper()
	raw, _ := json.Marshal(args)
	frames := mcpCall(t, f.h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+name+`","arguments":`+string(raw)+`}}`)
	result, _ := frames[0]["result"].(map[string]any)
	content, _ := result["content"].([]any)
	var blocks []map[string]any
	for _, c := range content {
		blocks = append(blocks, c.(map[string]any))
	}
	if len(blocks) == 0 {
		t.Fatalf("%s: no content in %v", name, frames[0])
	}
	text, _ := blocks[0]["text"].(string)
	if isErr, _ := result["isError"].(bool); isErr {
		return "ERROR: " + text, blocks
	}
	return text, blocks
}

func refFor(t *testing.T, tree, label string) int {
	t.Helper()
	for _, line := range strings.Split(tree, "\n") {
		if strings.Contains(line, label) {
			if i := strings.Index(line, "[ref="); i >= 0 {
				n, _ := strconv.Atoi(strings.TrimRight(line[i+5:strings.Index(line[i:], "]")+i], "]"))
				return n
			}
		}
	}
	t.Fatalf("no ref for %s in\n%s", label, tree)
	return 0
}

func TestAgentBrowserToolsDriveTheSessionsBrowser(t *testing.T) {
	f := newBrowserFixture(t)
	t.Setenv("LECTERN_SESSION_ID", strconv.FormatInt(f.sess.ID, 10))
	t.Setenv("TMUX", "")
	page := fmt.Sprintf("http://localhost:%d/", f.appPort)
	text, _ := f.tool(t, "browser_navigate", obj{"url": page})
	if !strings.Contains(text, "Fixture shop") {
		t.Fatalf("navigate: %s", text)
	}
	text, blocks := f.tool(t, "browser_snapshot", obj{})
	var snap struct {
		Tree string `json:"tree"`
	}
	if err := json.Unmarshal([]byte(text), &snap); err != nil || !strings.Contains(snap.Tree, `button "Save"`) {
		t.Fatalf("snapshot: %s", text)
	}
	if len(blocks) != 2 || blocks[1]["type"] != "image" || blocks[1]["mimeType"] != "image/png" {
		t.Fatalf("the snapshot's screenshot is not an image block: %v", blocks)
	}
	if shot, _ := base64.StdEncoding.DecodeString(blocks[1]["data"].(string)); len(shot) < 1000 {
		t.Fatal("empty screenshot")
	}
	if text, _ := f.tool(t, "browser_fill", obj{"ref": refFor(t, snap.Tree, `textbox "Name"`), "text": "Grace"}); strings.HasPrefix(text, "ERROR") {
		t.Fatal(text)
	}
	if text, _ := f.tool(t, "browser_click", obj{"ref": refFor(t, snap.Tree, `button "Save"`)}); strings.HasPrefix(text, "ERROR") {
		t.Fatal(text)
	}
	text, _ = f.tool(t, "browser_evaluate", obj{"expression": "document.querySelector('#msg').textContent"})
	if !strings.Contains(text, `"value": "saved Grace"`) {
		t.Fatalf("evaluate after click: %s", text)
	}
	if text, _ := f.tool(t, "browser_evaluate", obj{"expression": "document.title = 'hacked'"}); !strings.Contains(text, "read-only") {
		t.Fatalf("a write through evaluate: %s", text)
	}
	if text, _ := f.tool(t, "browser_console", obj{}); !strings.Contains(text, "saved") {
		t.Fatalf("console: %s", text)
	}

	// The operator takes over: the agent is refused until they hand it back.
	f.h.post(fmt.Sprintf("/api/sessions/%d/browser", f.sess.ID), obj{"action": "control", "mode": "user"}, 200)
	if text, _ := f.tool(t, "browser_click", obj{"selector": "#save"}); !strings.Contains(text, "taken over") {
		t.Fatalf("agent click while the operator has control: %s", text)
	}
	f.h.post(fmt.Sprintf("/api/sessions/%d/browser", f.sess.ID), obj{"action": "control", "mode": "stopped"}, 200)
	if text, _ := f.tool(t, "browser_snapshot", obj{}); !strings.Contains(text, "stopped agent control") {
		t.Fatalf("agent while stopped: %s", text)
	}
	f.h.post(fmt.Sprintf("/api/sessions/%d/browser", f.sess.ID), obj{"action": "control", "mode": "agent"}, 200)
	if text, _ := f.tool(t, "browser_screenshot", obj{"selector": "#card"}); strings.HasPrefix(text, "ERROR") {
		t.Fatal(text)
	}
	st := f.h.get(fmt.Sprintf("/api/sessions/%d/browser", f.sess.ID))
	if st["running"] != true || st["where"] != "target" || st["last_action"] != "screenshot" {
		t.Fatalf("status: %v", st)
	}
	if text, _ := f.tool(t, "browser_close", obj{}); !strings.Contains(text, `"closed": true`) {
		t.Fatalf("close: %s", text)
	}
}

func TestDesignSendStagesFilesAndTypesOneMessage(t *testing.T) {
	f := newBrowserFixture(t, func(c *config.Config) { c.Live = true; c.Host = "127.0.0.1" })
	view := f.h.post(fmt.Sprintf("/api/sessions/%d/browser/views", f.sess.ID),
		obj{"port": f.appPort, "design": true, "parent_origin": "http://127.0.0.1:1"}, 201)
	listen := int(view["listen_port"].(float64))
	// What the picker in the pane reports for the card: the view's own URL, its
	// selector, a viewport, and a client capture that must NOT be used when a
	// headless browser can render the page.
	el := obj{"selector": "#card", "breadcrumb": "html › body › div#card", "tag": "div", "text": "Blue card",
		"html": `<div id="card" class="card">Blue card</div>`, "css": obj{"background-color": "rgb(0, 0, 255)"},
		"rules": []string{"#card { background: blue; }"}, "rect": obj{"x": 30, "y": 50, "width": 160, "height": 90},
		"viewport": obj{"width": 800, "height": 600, "dpr": 1}, "url": fmt.Sprintf("http://127.0.0.1:%d/?tab=1", listen),
		"title": "Fixture shop", "client_png": "data:image/png;base64,AAAA"}
	out := f.h.post(fmt.Sprintf("/api/sessions/%d/design", f.sess.ID),
		obj{"note": "Make this card green", "source": "frame", "view_id": view["id"], "elements": []obj{el}}, 200)
	files := map[string]string{}
	for _, raw := range out["files"].([]any) {
		file := raw.(map[string]any)
		files[file["name"].(string)] = file["path"].(string)
	}
	for _, name := range []string{"design.md", "element-1.html", "element-1.png"} {
		if !strings.HasPrefix(files[name], filepath.Join(f.dir, ".lectern", "context")+"/") {
			t.Fatalf("%s not staged in the workspace: %v", name, files)
		}
	}
	shot := out["screenshots"].([]any)[0].(map[string]any)
	if !strings.Contains(shot["source"].(string), "headless Chromium on browser-local") {
		t.Fatalf("screenshot source: %v", shot)
	}
	raw, _ := os.ReadFile(files["element-1.png"])
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 160 || b.Dy() != 90 {
		t.Fatalf("crop %dx%d, want 160x90", b.Dx(), b.Dy())
	}
	if r, g, bl, _ := img.At(80, 45).RGBA(); r>>8 > 20 || g>>8 > 20 || bl>>8 < 240 {
		t.Fatalf("crop middle is not the blue card: %d %d %d", r>>8, g>>8, bl>>8)
	}
	md, _ := os.ReadFile(files["design.md"])
	for _, want := range []string{"Make this card green", "Selector: `#card`", "background-color: rgb(0, 0, 255);",
		fmt.Sprintf("http://localhost:%d/?tab=1", f.appPort), "#card { background: blue; }"} {
		if !strings.Contains(string(md), want) {
			t.Fatalf("design.md lacks %q:\n%s", want, md)
		}
	}
	if strings.Contains(string(md), strconv.Itoa(listen)) {
		t.Fatalf("design.md names the proxy port instead of the dev server:\n%s", md)
	}
	// The message reached the agent's terminal, naming the files.
	received := filepath.Join(f.dir, "received.txt")
	f.h.waitUntil("the message arrives", func() bool {
		got, _ := os.ReadFile(received)
		return strings.Contains(string(got), files["element-1.png"]) && strings.Contains(string(got), "Make this card green")
	})
}

func TestComputerUseNeedsADesktopAndPermission(t *testing.T) {
	h := newHarness(t)
	code, body := h.request("POST", "/api/computer", obj{"action": "screenshot"}, nil)
	if code != 422 || !strings.Contains(string(body), "no Lectern session") {
		t.Fatalf("no session: %d %s", code, body)
	}
	proj := h.post("/api/projects", obj{"name": "p", "repo_path": "/tmp/p", "target_id": h.firstTargetID()}, 201)
	if proj["computer_use"] != float64(0) {
		t.Fatalf("computer use must start off: %v", proj["computer_use"])
	}
	id := int64(proj["id"].(float64))
	patched := h.request2("PATCH", fmt.Sprintf("/api/projects/%d", id), obj{"computer_use": true}, 200)
	if patched["computer_use"] != float64(1) {
		t.Fatalf("patch: %v", patched)
	}
}
