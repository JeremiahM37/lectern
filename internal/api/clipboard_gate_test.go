package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
)

// An agent on the host is KindLocal and, under Tailscale identity, not human.
// It must be able neither to register as somebody's clipboard, nor to answer
// for one, nor to report activity, nor to fill a session's clipboard.
func TestHostProcessCannotActAsAClipboardClient(t *testing.T) {
	s := &Server{Auth: &auth.Resolver{Mode: auth.ModeTailscale}}
	local := auth.Principal{Kind: auth.KindLocal}
	for name, tc := range map[string]struct {
		h      http.HandlerFunc
		method string
		target string
	}{
		"listen":  {s.clipboardListen, "GET", "/api/clipboard/listen?client=evil-client-1&session=1"},
		"active":  {s.clipboardActive, "POST", "/api/clipboard/active?client=laptop-1234"},
		"respond": {s.clipboardRespond, "POST", "/api/clipboard/respond/x?client=laptop-1234"},
		"mirror":  {s.clipboardMirror, "PUT", "/api/clipboard/mirror?session=1"},
		"share":   {s.shareTarget, "POST", "/share-target"},
		"stash":   {s.shareStashGet, "GET", "/api/share-stash/x"},
		"clear":   {s.clipboardMirrorClear, "DELETE", "/api/clipboard/mirror?session=1"},
	} {
		r := httptest.NewRequest(tc.method, tc.target, strings.NewReader("x"))
		r.Header.Set("Content-Type", "image/png")
		r = r.WithContext(auth.WithPrincipal(r.Context(), local))
		w := httptest.NewRecorder()
		tc.h(w, r)
		if w.Code != 403 {
			t.Errorf("%s by a host process: %d %s", name, w.Code, w.Body.String())
		}
	}
	// a signed-in person passes the gate (and then fails on the missing session)
	r := httptest.NewRequest("POST", "/api/clipboard/active?client=laptop-1234", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Kind: auth.KindTailscale, Login: "me", Human: true}))
	w := httptest.NewRecorder()
	s.clipboardActive(w, r)
	if w.Code != 404 {
		t.Fatalf("a person: %d", w.Code)
	}
}
