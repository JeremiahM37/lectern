package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func restoreFake(t *testing.T, reopened *map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	rows := []map[string]any{
		{"id": 7.0, "name": "chat-test", "agent": "claude", "reason_label": "Archived", "action_label": "Resume", "preview": "last words"},
		{"id": 5.0, "name": "twin", "agent": "codex"},
		{"id": 4.0, "name": "twin", "agent": "codex"},
	}
	mux.HandleFunc("GET /api/sessions/restorable", jsonHandler(200, rows))
	mux.HandleFunc("POST /api/sessions/7/reopen", func(w http.ResponseWriter, r *http.Request) {
		*reopened = decodeBody(t, r)
		jsonHandler(201, map[string]any{"session": map[string]any{"id": 9.0, "name": "chat-test", "status": "starting"}, "message": "Resumed its saved conversation."})(w, r)
	})
	return httptest.NewServer(mux)
}

func TestRestoreSessionListsReopensAndRefusesAmbiguity(t *testing.T) {
	var reopened map[string]any
	srv := restoreFake(t, &reopened)
	defer srv.Close()
	c := New(srv.URL, "")
	out, err := c.call("restore_session", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := out.(map[string]any)["restorable"].([]map[string]any)
	if len(list) != 3 || list[0]["last_message"] != "last words" || list[0]["reopen_does"] != "Resume" {
		t.Fatalf("list: %#v", out)
	}
	out, err = c.call("restore_session", map[string]any{"session": "chat-test", "agent": "codex"})
	if err != nil {
		t.Fatal(err)
	}
	if m := out.(map[string]any); m["restored"] != true || m["session_id"] != 9.0 || reopened["agent"] != "codex" {
		t.Fatalf("restore by name: %#v body=%#v", out, reopened)
	}
	if _, err := c.call("restore_session", map[string]any{"session": "twin"}); err == nil || !strings.Contains(err.Error(), "pass the id") {
		t.Fatalf("ambiguous name: %v", err)
	}
	if _, err := c.call("restore_session", map[string]any{"session": "nope"}); err == nil {
		t.Fatal("unknown session should error")
	}
}
