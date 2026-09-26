package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func endSessionFake(t *testing.T, archived *map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	sessions := []map[string]any{
		{"id": 7.0, "name": "chat-test", "status": "waiting"},
		{"id": 8.0, "name": "real-work", "status": "running"},
	}
	mux.HandleFunc("GET /api/sessions", jsonHandler(200, sessions))
	mux.HandleFunc("GET /api/sessions/7", jsonHandler(200, sessions[0]))
	mux.HandleFunc("POST /api/sessions/7/archive", func(w http.ResponseWriter, r *http.Request) {
		*archived = decodeBody(t, r)
		jsonHandler(200, map[string]any{"id": 7.0, "archived": true})(w, r)
	})
	mux.HandleFunc("POST /api/sessions/8/archive", func(w http.ResponseWriter, r *http.Request) {
		t.Error("real-work must never be archived by these tests")
	})
	return httptest.NewServer(mux)
}

func TestEndSessionArchivesWithStopByExactName(t *testing.T) {
	var archived map[string]any
	srv := endSessionFake(t, &archived)
	defer srv.Close()
	out, err := New(srv.URL, "").call("end_session", map[string]any{"session": "chat-test"})
	if err != nil {
		t.Fatal(err)
	}
	if archived["stop"] != true {
		t.Fatalf("archive must be called with stop=true, got %v", archived)
	}
	if m, _ := out.(map[string]any); m["archived"] != true {
		t.Fatalf("unexpected result %v", out)
	}
}

func TestEndSessionByID(t *testing.T) {
	var archived map[string]any
	srv := endSessionFake(t, &archived)
	defer srv.Close()
	if _, err := New(srv.URL, "").call("end_session", map[string]any{"session": "7"}); err != nil {
		t.Fatal(err)
	}
	if archived == nil {
		t.Fatal("session 7 was not archived")
	}
}

// A unique partial match is enough to send a message, never to end a session.
func TestEndSessionRefusesPartialName(t *testing.T) {
	var archived map[string]any
	srv := endSessionFake(t, &archived)
	defer srv.Close()
	_, err := New(srv.URL, "").call("end_session", map[string]any{"session": "chat"})
	if err == nil || !strings.Contains(err.Error(), "exact name") {
		t.Fatalf("expected a refusal naming the exact-name rule, got %v", err)
	}
	if archived != nil {
		t.Fatal("a partial name must not archive anything")
	}
}
