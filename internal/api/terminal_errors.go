package api

import (
	"errors"
	"html"
	"net/http"

	"github.com/JeremiahM37/lectern/v2/internal/terminal"
)

// terminalError answers a failed attach. A missing terminal viewer is not a
// passing hiccup worth retrying, so it carries a code the web app can tell
// apart from one, and the command that fixes it.
func terminalError(w http.ResponseWriter, err error) {
	var missing terminal.MissingViewer
	if errors.As(err, &missing) {
		writeJSON(w, 503, map[string]any{"detail": err.Error(), "code": "terminal_viewer_missing", "fix": missing.Fix})
		return
	}
	httpError(w, 503, "%s", err.Error())
}

// terminalPageError is terminalError for /term/, which a browser frame
// loads directly: it gets a readable page instead of a bare status line.
func terminalPageError(w http.ResponseWriter, r *http.Request, err error) {
	var missing terminal.MissingViewer
	if !errors.As(err, &missing) || r.Header.Get("Upgrade") != "" {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<style>:root{color-scheme:light dark}body{font:15px/1.5 system-ui,sans-serif;margin:24px 16px}code{padding:.1em .35em;border-radius:4px;background:rgba(127,127,127,.18)}</style>` +
		`<p><strong>Terminal viewer isn't installed.</strong></p><p>Run <code>` + html.EscapeString(missing.Fix) +
		`</code> on the computer running Lectern, then try again.</p>`))
}
