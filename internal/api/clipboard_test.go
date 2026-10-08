package api_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/JeremiahM37/lectern/v2/internal/clipboard"
)

type fakeReader struct{ types map[string][]byte }

func (f fakeReader) Types(context.Context) ([]string, error) {
	var out []string
	for k := range f.types {
		out = append(out, k)
	}
	return out, nil
}
func (f fakeReader) Read(_ context.Context, m string) ([]byte, error) {
	if d, ok := f.types[m]; ok {
		return d, nil
	}
	return nil, fmt.Errorf("no %s", m)
}

// attachClient connects a device's clipboard client to a session, as
// `lectern claude` does, and waits until the server has registered it.
func attachClient(t *testing.T, h *harness, session int64, id string, png string) *clipboard.Client {
	t.Helper()
	c := &clipboard.Client{Base: h.URL, ID: id, Session: session, Kind: "cli",
		Reader: fakeReader{types: map[string][]byte{"image/png": []byte(png)}}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go c.Serve(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		code, _ := h.request("POST", "/api/clipboard/active?client="+id, nil, nil)
		if code == 204 {
			return c
		}
		time.Sleep(30 * time.Millisecond)
	}
	t.Fatal("client never registered")
	return nil
}

func TestSessionAgentReadsOnlyItsOwnSessionsClient(t *testing.T) {
	h := newHarness(t)
	pid := h.seededProjectID()
	a := h.session(obj{"project_id": pid, "name": "a", "agent": "claude"})
	b := h.session(obj{"project_id": pid, "name": "b", "agent": "claude"})
	tokA, tokB := hookToken(t, h, a.id()), hookToken(t, h, b.id())
	attachClient(t, h, a.id(), "laptop-a-1234", "PNG-OF-A")
	body := `{"op":"read","type":"image/png"}`
	read := func(session int64, tok string) (int, string) {
		code, out := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/clipboard", session), body, tok)
		return code, string(out)
	}

	// the person is typing in session A: A's agent gets the image
	if code, out := read(a.id(), tokA); code != 200 || out != "PNG-OF-A" {
		t.Fatalf("own session: %d %q", code, out)
	}
	// B's agent, with B's own token, asks for the clipboard: B has no client
	if code, out := read(b.id(), tokB); code != 404 || strings.Contains(out, "PNG-OF-A") {
		t.Fatalf("other session read the client: %d %q", code, out)
	}
	// B's token against A's endpoint, no token, a made-up token: refused
	for name, tok := range map[string]string{"other session's token": tokB, "no token": "", "bad token": "nope"} {
		if code, out := read(a.id(), tok); code != 401 || strings.Contains(out, "PNG-OF-A") {
			t.Fatalf("%s: %d %q", name, code, out)
		}
	}
	// the general API cannot ask for a clipboard either: only the hook route exists
	if code, _ := h.request("POST", "/api/clipboard/ask", nil, nil); code == 200 {
		t.Fatal("an ask route exists on the general API")
	}
}

func TestClipboardNeedsRecentTyping(t *testing.T) {
	h := newHarness(t)
	a := h.session(obj{"project_id": h.seededProjectID(), "name": "a", "agent": "claude"})
	tok := hookToken(t, h, a.id())
	c := &clipboard.Client{Base: h.URL, ID: "idle-client-1", Session: a.id(), Kind: "cli",
		Reader: fakeReader{types: map[string][]byte{"image/png": []byte("X")}}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Serve(ctx)
	time.Sleep(300 * time.Millisecond) // registered, never typed
	code, _ := h.rawRequest("POST", fmt.Sprintf("/api/hook/session/%d/clipboard", a.id()), `{"op":"read","type":"image/png"}`, tok)
	if code != 404 {
		t.Fatalf("a client that never typed was asked: %d", code)
	}
}

func TestListAndTextAndRefusals(t *testing.T) {
	h := newHarness(t)
	a := h.session(obj{"project_id": h.seededProjectID(), "name": "a", "agent": "claude"})
	tok := hookToken(t, h, a.id())
	attachClient(t, h, a.id(), "laptop-a-5678", "IMG")
	path := fmt.Sprintf("/api/hook/session/%d/clipboard", a.id())
	code, out := h.rawRequest("POST", path, `{"op":"list"}`, tok)
	if code != 200 || !strings.Contains(string(out), "image/png") {
		t.Fatalf("list: %d %q", code, out)
	}
	for _, body := range []string{`{"op":"write","type":"image/png"}`, `{"op":"read","type":"application/x-secret"}`, `not json`} {
		if code, _ := h.rawRequest("POST", path, body, tok); code == 200 {
			t.Fatalf("%s was served", body)
		}
	}
}
