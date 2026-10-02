package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/config"
	"github.com/JeremiahM37/lectern/v2/internal/console"
)

// lectern demo starts the web app's demo the same way (scratch folder, the
// demo agent) and comes back to a running one rather than starting another.
func TestDemoStartsOrReusesTheDemoSession(t *testing.T) {
	var running []map[string]any
	var created []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/sessions":
			_ = json.NewEncoder(w).Encode(running)
		case r.Method == "POST" && r.URL.Path == "/api/sessions":
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			created = append(created, body)
			fmt.Fprint(w, `{"id":5,"name":"demo","agent":"demo"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	var out bytes.Buffer
	if err := demoCommand(&config.Config{}, nil, srv.URL, "", false, &out); err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || created[0]["agent"] != "demo" || created[0]["scratch"] != true {
		t.Fatalf("created %v", created)
	}
	if !strings.Contains(out.String(), "Started the demo agent (session #5)") || !strings.Contains(out.String(), "Ctrl+] y") {
		t.Fatalf("output:\n%s", out.String())
	}
	running = []map[string]any{{"id": 3, "name": "myapp", "agent": "claude"}, {"id": 4, "name": "demo", "agent": "demo"}}
	out.Reset()
	if err := demoCommand(&config.Config{}, nil, srv.URL, "", false, &out); err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || !strings.Contains(out.String(), "Back to the demo agent (session #4)") {
		t.Fatalf("did not reuse: created %v\n%s", created, out.String())
	}
	if err := demoCommand(&config.Config{}, []string{"--new"}, srv.URL, "", false, &out); err != nil || len(created) != 2 {
		t.Fatalf("--new: %v %v", err, created)
	}
}

// With no agent installed at all, `lectern claude` points at the demo.
func TestMissingAgentSuggestsTheDemo(t *testing.T) {
	agents := `[{"name":"claude","found":false,"builtin":true},{"name":"codex","found":false,"builtin":true}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `{"agents":%s}`, agents)
	}))
	t.Cleanup(srv.Close)
	err := checkAgentInstalled(console.New(srv.URL, ""), "claude")
	if err == nil || !strings.Contains(err.Error(), "lectern demo") {
		t.Fatalf("no agent: %v", err)
	}
	agents = `[{"name":"claude","found":false,"builtin":true},{"name":"codex","found":true,"builtin":true}]`
	err = checkAgentInstalled(console.New(srv.URL, ""), "claude")
	if err == nil || strings.Contains(err.Error(), "lectern demo") || !strings.Contains(err.Error(), "lectern codex") {
		t.Fatalf("another agent installed: %v", err)
	}
}
