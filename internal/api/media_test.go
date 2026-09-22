package api_test

// Media is how an agent shows its work. These tests post through the same MCP
// tool an agent calls and read back through the same URLs the browser plays.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func (h *harness) raw(method, path string, body io.Reader, headers map[string]string) (*http.Response, []byte) {
	h.t.Helper()
	req, err := http.NewRequest(method, h.URL+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

func TestPostMediaToolCopiesTheFileAndAttributesTheSession(t *testing.T) {
	h := newHarness(t)
	sess := h.post("/api/sessions/adopt", obj{"target_id": h.firstTargetID(),
		"tmux_session": "legacy-claude", "name": "recorder", "workdir": "/mock/demo-app"}, 201)
	source := filepath.Join(t.TempDir(), "demo run.mp4")
	payload := bytes.Repeat([]byte("frame-"), 4096)
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	args, _ := json.Marshal(obj{"path": source, "title": "Checkout passing", "note": "watch the total",
		"session_id": sess.id()})
	frames := mcpCall(t, h, fmt.Sprintf(
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"post_media","arguments":%s}}`, args))
	var row obj
	if err := json.Unmarshal([]byte(toolText(t, frames[0])), &row); err != nil {
		t.Fatalf("tool result: %v — %s", err, toolText(t, frames[0]))
	}
	if row.str("kind") != "file" || row.str("mime") != "video/mp4" || row.str("name") != "demo run.mp4" {
		t.Fatalf("row: %v", row)
	}
	if row.str("source") != "mcp" || row.str("session_name") != "recorder" {
		t.Errorf("attribution: %v", row)
	}
	// The post must outlive the file it came from: a worktree is cleaned up long
	// before anyone gets round to watching the recording.
	if err := os.Remove(source); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("/api/media/%d/content", row.id())
	resp, body := h.raw("GET", content, nil, nil)
	if resp.StatusCode != 200 || !bytes.Equal(body, payload) {
		t.Fatalf("content: %d, %d bytes", resp.StatusCode, len(body))
	}
	if got := resp.Header.Get("Content-Security-Policy"); !strings.HasPrefix(got, "sandbox") {
		t.Errorf("posted content must be sandboxed away from the Lectern origin, got %q", got)
	}
	// Seeking inside a recording is a range request; without it a browser has to
	// download the whole video before the scrubber works.
	resp, body = h.raw("GET", content, nil, map[string]string{"Range": "bytes=6-11"})
	if resp.StatusCode != 206 || string(body) != "frame-" {
		t.Fatalf("range: %d %q", resp.StatusCode, body)
	}
	feed := h.getList(fmt.Sprintf("/api/media?session_id=%d", sess.id()))
	if len(feed) != 1 || feed[0].id() != row.id() {
		t.Fatalf("feed: %v", feed)
	}
	if code := h.status("DELETE", fmt.Sprintf("/api/media/%d", row.id()), nil); code != 204 {
		t.Fatalf("delete: %d", code)
	}
	entries, _ := os.ReadDir(h.App.Cfg.MediaDir())
	if len(entries) != 0 {
		t.Errorf("delete left bytes behind: %v", entries)
	}
}

func TestMediaFindsItsSessionFromTheTmuxName(t *testing.T) {
	h := newHarness(t)
	sess := h.post("/api/sessions/adopt", obj{"target_id": h.firstTargetID(),
		"tmux_session": "legacy-claude", "name": "by-name", "workdir": "/mock/demo-app"}, 201)
	link := h.post("/api/media", obj{"url": "http://127.0.0.1:5173/cart", "title": "Dev server",
		"tmux_session": "legacy-claude", "source": "cli"}, 201)
	if int64(link.num("session_id")) != sess.id() || link.str("kind") != "link" {
		t.Fatalf("link: %v", link)
	}
	// An unknown tmux name still posts: losing the evidence is worse than
	// losing its label.
	stray := h.post("/api/media", obj{"url": "https://example.com", "title": "stray",
		"tmux_session": "not-an-lectern-session"}, 201)
	if stray["session_id"] != nil {
		t.Errorf("stray post must be unattributed: %v", stray)
	}
	// But a session the poster named explicitly has to exist.
	if code := h.status("POST", "/api/media", obj{"url": "https://example.com", "title": "x", "session_id": 99999}); code != 422 {
		t.Errorf("unknown explicit session: %d", code)
	}
	if code := h.status("POST", "/api/media", obj{"url": "file:///etc/passwd", "title": "x"}); code != 422 {
		t.Errorf("non-http url: %d", code)
	}
}

func TestMediaUploadIsBoundedAndNamesCannotSteerTheStore(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.MediaMaxBytes = 64 })
	resp, body := h.raw("POST", "/api/media?name=big.bin&title=big", bytes.NewReader(make([]byte, 65)), nil)
	if resp.StatusCode != 413 {
		t.Fatalf("over the limit: %d %s", resp.StatusCode, body)
	}
	resp, body = h.raw("POST", "/api/media?title=t&name=..%2F..%2F..%2Fescape.html", strings.NewReader("<p>hi</p>"), nil)
	if resp.StatusCode != 201 {
		t.Fatalf("post: %d %s", resp.StatusCode, body)
	}
	var row obj
	_ = json.Unmarshal(body, &row)
	if row.str("name") != "escape.html" {
		t.Errorf("name must be reduced to its base: %q", row.str("name"))
	}
	entries, _ := os.ReadDir(h.App.Cfg.MediaDir())
	if len(entries) != 1 || entries[0].Name() != fmt.Sprintf("media-%d", row.id()) {
		t.Errorf("store holds %v — the rejected upload must leave nothing, and the blob is named by id", entries)
	}
}
