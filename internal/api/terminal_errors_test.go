package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/terminal"
)

func TestMissingTerminalViewerSaysHowToInstallIt(t *testing.T) {
	missing := terminal.MissingViewer{Fix: "sudo apt-get install -y ttyd"}
	rec := httptest.NewRecorder()
	terminalError(rec, missing)
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 503 || body["code"] != "terminal_viewer_missing" || body["fix"] != missing.Fix ||
		body["detail"] != "Terminal viewer isn't installed — run: sudo apt-get install -y ttyd" {
		t.Fatalf("%d %v", rec.Code, body)
	}
	// Anything else keeps its plain, retryable 503.
	rec = httptest.NewRecorder()
	terminalError(rec, errors.New("the tmux session is starting"))
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "terminal_viewer_missing") {
		t.Fatalf("other error: %d %s", rec.Code, rec.Body)
	}
	// The terminal frame gets a page a person can read.
	rec = httptest.NewRecorder()
	terminalPageError(rec, httptest.NewRequest("GET", "/term/session/1/", nil), missing)
	if rec.Code != 503 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "sudo apt-get install -y ttyd") {
		t.Fatalf("page: %d %s", rec.Code, rec.Body)
	}
}
