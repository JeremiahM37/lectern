package api

import (
	"errors"
	"html"
	"net/http"

	"github.com/JeremiahM37/lectern/v2/internal/terminal"
)

// terminalError answers a failed attach. A terminal server that cannot be
// started at all is not a passing hiccup worth retrying, so it carries a
// code the web app can tell apart from one.
func terminalError(w http.ResponseWriter, err error) {
	var unavailable terminal.ViewerUnavailable
	if errors.As(err, &unavailable) {
		writeJSON(w, 503, map[string]any{"detail": err.Error(), "code": "terminal_viewer_unavailable", "reason": unavailable.Reason})
		return
	}
	httpError(w, 503, "%s", err.Error())
}

// terminalPageError is terminalError for /term/, which a browser frame
// loads directly: it gets a readable page instead of a bare status line.
func terminalPageError(w http.ResponseWriter, r *http.Request, err error) {
	var unavailable terminal.ViewerUnavailable
	if !errors.As(err, &unavailable) || r.Header.Get("Upgrade") != "" {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(`<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">` +
		`<style>:root{color-scheme:light dark}body{font:15px/1.5 system-ui,sans-serif;margin:24px 16px}code{padding:.1em .35em;border-radius:4px;background:rgba(127,127,127,.18)}</style>` +
		`<p><strong>The web terminal could not start.</strong></p><p>` + html.EscapeString(unavailable.Reason) +
		`</p><p>Restart Lectern and try again: <code>lectern local stop</code>, then <code>lectern up</code>.</p>`))
}
