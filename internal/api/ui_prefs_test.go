package api

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
	"github.com/JeremiahM37/lectern/v2/internal/bus"
	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func uiPrefCall(t *testing.T, s *Server, method, key, body string, who auth.Principal) *httptest.ResponseRecorder {
	t.Helper()
	// The path value is set directly: some keys under test are not valid URLs.
	r := httptest.NewRequest(method, "/api/ui/prefs/x", strings.NewReader(body))
	r.SetPathValue("key", key)
	r = r.WithContext(auth.WithPrincipal(r.Context(), who))
	w := httptest.NewRecorder()
	switch method {
	case "GET":
		s.getUIPrefs(w, r)
	case "PUT":
		s.putUIPref(w, r)
	case "DELETE":
		s.deleteUIPref(w, r)
	}
	return w
}

func uiPrefServer(t *testing.T) *Server {
	s := mcpTestServer(t, auth.ModeTailscale)
	s.Bus = bus.New()
	s.Cfg = &config.Config{}
	return s
}

func readPrefs(t *testing.T, w *httptest.ResponseRecorder) map[string]json.RawMessage {
	t.Helper()
	var out struct {
		Prefs map[string]json.RawMessage `json:"prefs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", w.Body.String(), err)
	}
	return out.Prefs
}

// Preferences follow a person, not a device, and one person never sees
// another's: the owner is the signed-in login.
func TestUIPrefsArePerPersonAndRoundTrip(t *testing.T) {
	s := uiPrefServer(t)
	alice := auth.Principal{Kind: auth.KindTailscale, Login: "alice@example.com", Human: true}
	bob := auth.Principal{Kind: auth.KindTailscale, Login: "bob@example.com", Human: true}
	if w := uiPrefCall(t, s, "PUT", "appearance", `{"theme":"light","accent":"#0a7"}`, alice); w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	if w := uiPrefCall(t, s, "PUT", "shortcuts", `{"palette.open":["Mod+P"]}`, bob); w.Code != 200 {
		t.Fatalf("put: %d %s", w.Code, w.Body.String())
	}
	got := readPrefs(t, uiPrefCall(t, s, "GET", "", "", alice))
	if string(got["appearance"]) != `{"theme":"light","accent":"#0a7"}` {
		t.Fatalf("alice's value must come back byte-for-byte: %s", got["appearance"])
	}
	if _, leaked := got["shortcuts"]; leaked {
		t.Fatal("bob's shortcuts must not appear in alice's preferences")
	}
	// A second write replaces, and delete returns the key to its default.
	uiPrefCall(t, s, "PUT", "appearance", `{"theme":"dark"}`, alice)
	if got := readPrefs(t, uiPrefCall(t, s, "GET", "", "", alice)); string(got["appearance"]) != `{"theme":"dark"}` {
		t.Fatalf("replace: %s", got["appearance"])
	}
	if w := uiPrefCall(t, s, "DELETE", "appearance", "", alice); w.Code != 200 {
		t.Fatalf("delete: %d", w.Code)
	}
	if got := readPrefs(t, uiPrefCall(t, s, "GET", "", "", alice)); len(got) != 0 {
		t.Fatalf("after delete: %v", got)
	}
}

// A quick command is text a person later sends to a terminal with one tap, so
// an agent or other local process may read but never write preferences.
func TestUIPrefWritesNeedAPerson(t *testing.T) {
	s := uiPrefServer(t)
	local := auth.Principal{Kind: auth.KindLocal}
	if w := uiPrefCall(t, s, "PUT", "quick-commands", `[{"text":"rm -rf ~"}]`, local); w.Code != 403 {
		t.Fatalf("a local process writing a preference: got %d, want 403", w.Code)
	}
	if w := uiPrefCall(t, s, "DELETE", "quick-commands", "", local); w.Code != 403 {
		t.Fatalf("a local process deleting a preference: got %d, want 403", w.Code)
	}
	if w := uiPrefCall(t, s, "GET", "", "", local); w.Code != 200 {
		t.Fatalf("reading stays open: %d", w.Code)
	}
}

func TestUIPrefsRejectBadKeysValuesAndSizes(t *testing.T) {
	s := uiPrefServer(t)
	me := auth.Principal{Kind: auth.KindTailscale, Login: "me@example.com", Human: true}
	for _, key := range []string{"Upper", "../x", "has space", strings.Repeat("a", 81)} {
		if w := uiPrefCall(t, s, "PUT", key, `1`, me); w.Code != 400 {
			t.Errorf("key %q: got %d, want 400", key, w.Code)
		}
	}
	if w := uiPrefCall(t, s, "PUT", "layouts", `{not json`, me); w.Code != 422 {
		t.Errorf("invalid JSON: got %d, want 422", w.Code)
	}
	big := `"` + strings.Repeat("x", uiPrefMaxBytes) + `"`
	if w := uiPrefCall(t, s, "PUT", "layouts", big, me); w.Code != 413 {
		t.Errorf("oversized value: got %d, want 413", w.Code)
	}
	// Project-scoped keys carry an id after a colon.
	if w := uiPrefCall(t, s, "PUT", "quick-commands:project:12", `[]`, me); w.Code != 200 {
		t.Errorf("scoped key: got %d %s", w.Code, w.Body.String())
	}
}

func TestUIPrefsCapTheNumberOfKeys(t *testing.T) {
	s := uiPrefServer(t)
	me := auth.Principal{Kind: auth.KindTailscale, Login: "me@example.com", Human: true}
	for i := 0; i < uiPrefMaxKeys; i++ {
		if w := uiPrefCall(t, s, "PUT", "k"+strconv.Itoa(i), `1`, me); w.Code != 200 {
			t.Fatalf("key %d: %d", i, w.Code)
		}
	}
	if w := uiPrefCall(t, s, "PUT", "one-more", `1`, me); w.Code != 409 {
		t.Fatalf("over the cap: got %d, want 409", w.Code)
	}
	// Updating an existing key is still allowed at the cap.
	if w := uiPrefCall(t, s, "PUT", "k7", `2`, me); w.Code != 200 {
		t.Fatalf("update at the cap: got %d %s", w.Code, w.Body.String())
	}
}
