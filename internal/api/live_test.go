package api_test

// Live views widen what is reachable, so these tests are about the refusals:
// the forwarding itself is covered where a real target exists, in the forward
// package and the browser suite.

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func TestLiveViewsRefuseATargetWhoseLoopbackCannotBeReached(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.Live = true })
	// The scripted target has no network of its own to forward into.
	code, body := h.request("POST", "/api/live/ports", obj{"port": 8080}, nil)
	if code != 409 || !strings.Contains(string(body), "cannot forward ports") {
		t.Fatalf("port: %d %s", code, body)
	}
	code, body = h.request("POST", "/api/live/desktops", obj{"title": "x"}, nil)
	if code != 409 {
		t.Fatalf("desktop: %d %s", code, body)
	}
	if views := h.get("/api/live").list("views"); len(views) != 0 {
		t.Errorf("a refused request left a view behind: %v", views)
	}
	if code := h.status("DELETE", "/api/live/1", nil); code != 404 {
		t.Errorf("stopping nothing: %d", code)
	}
}

func TestLiveViewsStayShutOnATokenProtectedServer(t *testing.T) {
	h := newHarness(t, func(c *config.Config) { c.AuthToken = "secret123"; c.Live = true })
	auth := map[string]string{"Authorization": "Bearer secret123"}
	// Even a caller holding the token is refused: the port it would open could
	// not ask the next caller for one.
	code, body := h.request("POST", "/api/live/ports", obj{"port": 8080}, auth)
	if code != 409 || !strings.Contains(string(body), "LECTERN_LIVE_UNAUTHENTICATED") {
		t.Fatalf("got %d %s", code, body)
	}
	if code, _ := h.request("POST", "/api/live/ports", obj{"port": 8080}, nil); code != 401 {
		t.Errorf("without the token the API itself must refuse first: %d", code)
	}
}

func TestLiveViewsAreOffUntilTheOperatorAsksForThem(t *testing.T) {
	h := newHarness(t)
	state := h.get("/api/live")
	if state["enabled"] != false || len(state.list("views")) != 0 {
		t.Fatalf("a default server must report live views off: %v", state)
	}
	for _, path := range []string{"/api/live/ports", "/api/live/desktops"} {
		code, body := h.request("POST", path, obj{"port": 8080, "title": "x"}, nil)
		if code != 409 || !strings.Contains(string(body), "LECTERN_LIVE=1") {
			t.Errorf("%s: %d %s — the refusal must say how to turn it on", path, code, body)
		}
	}
	// An agent's tool call gets the same answer, in words it can relay.
	args := `{"title":"watch me"}`
	frames := mcpCall(t, h, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"open_live_view","arguments":`+args+`}}`)
	if text := toolText(t, frames[0]); !strings.Contains(text, "LECTERN_LIVE=1") {
		t.Errorf("tool result: %s", text)
	}
}
