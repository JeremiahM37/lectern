package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
)

func restoreServer(t *testing.T, rows []map[string]any, calls *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*calls = append(*calls, r.Method+" "+r.URL.RequestURI())
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/sessions/restorable":
			json.NewEncoder(w).Encode(rows)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/reopen"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			if r.URL.Path == "/api/sessions/9/reopen" {
				w.WriteHeader(409)
				json.NewEncoder(w).Encode(map[string]any{"detail": "no conversation is bound to this record; choose one from its saved conversations", "needs_history": true})
				return
			}
			w.WriteHeader(201)
			json.NewEncoder(w).Encode(map[string]any{"session": map[string]any{"id": 30, "name": "parser", "agent": body["agent"]}, "message": "Resumed its saved conversation."})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestRestoreArgs(t *testing.T) {
	o, err := parseRestoreArgs([]string{"parser", "fix", "--agent", "codex", "--model", "m", "--profile", "3", "--no-attach"})
	if err != nil || o.Query != "parser fix" || o.Agent != "codex" || o.Model != "m" || o.Profile != 3 || !o.NoAttach {
		t.Fatalf("parse: %#v %v", o, err)
	}
	for _, bad := range [][]string{{"--agent"}, {"--profile", "x"}, {"--last", "q"}, {"--bogus"}} {
		if _, err := parseRestoreArgs(bad); err == nil {
			t.Fatalf("%v should be refused", bad)
		}
	}
}

func TestRestoreCommandListsMatchesAndReopens(t *testing.T) {
	var calls []string
	rows := []map[string]any{
		{"id": 12, "name": "parser", "agent": "claude", "project_name": "lectern", "reason_label": "Exited on its own", "action_label": "Resume", "preview": "fix the parser", "updated_at": 1},
		{"id": 11, "name": "docs", "agent": "codex", "reason_label": "Archived", "action_label": "Resume", "updated_at": 1},
	}
	srv := restoreServer(t, rows, &calls)
	cfg := &config.Config{}
	var out bytes.Buffer
	if err := restoreCommand(cfg, nil, srv.URL, "", false, &out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "#12") || !strings.Contains(out.String(), "→ Resume: “fix the parser”") || !strings.Contains(out.String(), "no project") {
		t.Fatalf("list output:\n%s", out.String())
	}
	out.Reset()
	// Two matches: list them and ask for an id instead of guessing.
	err := restoreCommand(cfg, []string{"p"}, srv.URL, "", false, &out, false)
	if err == nil || !strings.Contains(err.Error(), "lectern restore 12") || !strings.Contains(out.String(), "2 sessions match") {
		t.Fatalf("ambiguous: %v\n%s", err, out.String())
	}
	calls = nil
	out.Reset()
	if err := restoreCommand(cfg, []string{"12", "--agent", "codex"}, srv.URL, "", false, &out, false); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "POST /api/sessions/12/reopen" || !strings.Contains(out.String(), "Restored #30") || !strings.Contains(out.String(), "lectern attach session 30") {
		t.Fatalf("reopen by id: %v\n%s", calls, out.String())
	}
	calls = nil
	if err := restoreCommand(cfg, []string{"--last"}, srv.URL, "", false, &out, false); err != nil {
		t.Fatal(err)
	}
	if strings.Join(calls, ",") != "GET /api/sessions/restorable?limit=1,POST /api/sessions/12/reopen" {
		t.Fatalf("--last: %v", calls)
	}
	err = restoreCommand(cfg, []string{"9"}, srv.URL, "", false, &out, false)
	if err == nil || !strings.Contains(err.Error(), "press h") {
		t.Fatalf("history needed: %v", err)
	}
}
