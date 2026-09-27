package api

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"

	"github.com/JeremiahM37/lectern/v2/internal/auth"
)

// UI preferences (docs/workspace.md): the web app's per-person settings —
// theme, shortcuts, saved layouts, quick commands — stored here so a phone and
// a desk see the same ones. Values are opaque JSON owned by the web app; the
// server only bounds their size and number.
//
// Reads are open to any caller the API admits, because they are only the
// caller's own rows. Writes need a signed-in person, like approvals: a quick
// command is text a person later sends into a terminal with one tap, so an
// agent or local process must not be able to plant one.

var uiPrefKey = regexp.MustCompile(`^[a-z][a-z0-9._:-]{0,79}$`)

const (
	uiPrefMaxBytes = 256 << 10
	uiPrefMaxKeys  = 200
)

// uiPrefOwner names whose preferences a request reads and writes: the
// signed-in login, or one shared "operator" when there is no identity.
func uiPrefOwner(r *http.Request) string {
	if principal, _ := auth.FromContext(r.Context()); principal.Login != "" {
		return principal.Login
	}
	return "operator"
}

// GET /api/ui/prefs returns {"prefs": {key: value}, "updated": {key: at}}.
func (s *Server) getUIPrefs(w http.ResponseWriter, r *http.Request) {
	rows, err := s.DB.UIPrefs(uiPrefOwner(r))
	if err != nil {
		respondErr(w, err)
		return
	}
	prefs, updated := map[string]json.RawMessage{}, map[string]float64{}
	for _, row := range rows {
		if !json.Valid([]byte(row.Value)) {
			continue
		}
		prefs[row.Key] = json.RawMessage(row.Value)
		updated[row.Key] = row.UpdatedAt
	}
	writeJSON(w, 200, map[string]any{"prefs": prefs, "updated": updated})
}

// PUT /api/ui/prefs/{key} stores the request body (any JSON value) as-is.
func (s *Server) putUIPref(w http.ResponseWriter, r *http.Request) {
	if !s.humanPrincipal(w, r, "changing preferences") {
		return
	}
	key := r.PathValue("key")
	if !uiPrefKey.MatchString(key) {
		httpError(w, 400, "invalid preference key %q", key)
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, uiPrefMaxBytes+1))
	if err != nil {
		httpError(w, 400, "%s", err.Error())
		return
	}
	if len(raw) > uiPrefMaxBytes {
		httpError(w, 413, "preference %q is larger than %d KiB", key, uiPrefMaxBytes>>10)
		return
	}
	if !json.Valid(raw) {
		httpError(w, 422, "preference %q must be JSON", key)
		return
	}
	owner := uiPrefOwner(r)
	existing, err := s.DB.UIPrefs(owner)
	if err != nil {
		respondErr(w, err)
		return
	}
	known := false
	for _, row := range existing {
		known = known || row.Key == key
	}
	if !known && len(existing) >= uiPrefMaxKeys {
		httpError(w, 409, "too many stored preferences (limit %d)", uiPrefMaxKeys)
		return
	}
	at, err := s.DB.SetUIPref(owner, key, string(raw))
	if err != nil {
		respondErr(w, err)
		return
	}
	// Other devices refetch their own rows; the value itself is not broadcast,
	// because the board stream reaches every signed-in person.
	s.Bus.Publish("board", "ui_prefs", map[string]any{"key": key, "updated_at": at})
	writeJSON(w, 200, map[string]any{"key": key, "updated_at": at})
}

// DELETE /api/ui/prefs/{key} forgets one preference (back to the default).
func (s *Server) deleteUIPref(w http.ResponseWriter, r *http.Request) {
	if !s.humanPrincipal(w, r, "changing preferences") {
		return
	}
	key := r.PathValue("key")
	if !uiPrefKey.MatchString(key) {
		httpError(w, 400, "invalid preference key %q", key)
		return
	}
	if err := s.DB.DeleteUIPref(uiPrefOwner(r), key); err != nil {
		respondErr(w, err)
		return
	}
	s.Bus.Publish("board", "ui_prefs", map[string]any{"key": key})
	writeJSON(w, 200, map[string]any{"key": key, "deleted": true})
}
