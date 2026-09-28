package api

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/terminal"
)

func TestUnavailableTerminalServerIsNotRetried(t *testing.T) {
	unavailable := terminal.ViewerUnavailable{Reason: "fork/exec /usr/local/bin/lectern: no such file or directory"}
	rec := httptest.NewRecorder()
	terminalError(rec, unavailable)
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 503 || body["code"] != "terminal_viewer_unavailable" || body["reason"] != unavailable.Reason ||
		!strings.HasPrefix(body["detail"], "The web terminal could not start (") {
		t.Fatalf("%d %v", rec.Code, body)
	}
	// Anything else keeps its plain, retryable 503.
	rec = httptest.NewRecorder()
	terminalError(rec, errors.New("the tmux session is starting"))
	if rec.Code != 503 || strings.Contains(rec.Body.String(), "terminal_viewer_unavailable") {
		t.Fatalf("other error: %d %s", rec.Code, rec.Body)
	}
	// The terminal frame gets a page a person can read.
	rec = httptest.NewRecorder()
	terminalPageError(rec, httptest.NewRequest("GET", "/term/session/1/", nil), unavailable)
	if rec.Code != 503 || !strings.Contains(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(rec.Body.String(), "lectern up") {
		t.Fatalf("page: %d %s", rec.Code, rec.Body)
	}
}
