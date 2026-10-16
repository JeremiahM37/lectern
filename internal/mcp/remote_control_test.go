package mcp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// start_session passes remote_control only when asked; enable_remote_control
// calls the session's remote-control route and returns the link; list and
// read report the flag.
func TestRemoteControlMCPTools(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/projects", jsonHandler(200, []map[string]any{{"id": 5.0, "name": "proj"}}))
	var bodies []map[string]any
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		bodies = append(bodies, decodeBody(t, r))
		jsonHandler(201, map[string]any{"id": 42.0, "name": "sess-1", "status": "starting", "remote_control": true})(w, r)
	})
	mux.HandleFunc("GET /api/sessions", jsonHandler(200, []map[string]any{
		{"id": 7.0, "name": "phone", "agent": "claude", "status": "waiting", "updated_at": 2.0,
			"remote_control": true, "remote_control_url": "https://claude.ai/code/session_abc"},
		{"id": 8.0, "name": "plain", "agent": "claude", "status": "waiting", "updated_at": 1.0},
	}))
	enabled := 0
	mux.HandleFunc("POST /api/sessions/8/remote-control", func(w http.ResponseWriter, r *http.Request) {
		enabled++
		jsonHandler(200, map[string]any{"id": 8.0, "name": "plain", "remote_control": true,
			"remote_control_url": "https://claude.ai/code/session_def"})(w, r)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	s := New(srv.URL, "")

	if _, err := s.call("start_session", map[string]any{"project": "proj", "prompt": "go"}); err != nil {
		t.Fatal(err)
	}
	raw, err := s.call("start_session", map[string]any{"project": "proj", "prompt": "go", "remote_control": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, set := bodies[0]["remote_control"]; set || bodies[1]["remote_control"] != true {
		t.Fatalf("remote_control must be sent only when asked: %v", bodies)
	}
	if out := raw.(map[string]any); out["remote_control"] != true {
		t.Fatalf("start result: %v", out)
	}

	raw, err = s.call("list_sessions", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	rows := raw.([]map[string]any)
	if rows[0]["remote_control"] != true || rows[0]["remote_control_url"] != "https://claude.ai/code/session_abc" || rows[1]["remote_control"] != false {
		t.Fatalf("list_sessions: %v", rows)
	}

	raw, err = s.call("enable_remote_control", map[string]any{"session": "plain"})
	if err != nil {
		t.Fatal(err)
	}
	out := raw.(map[string]any)
	if enabled != 1 || out["remote_control"] != true || !strings.HasSuffix(out["url"].(string), "session_def") {
		t.Fatalf("enable_remote_control: %v (calls %d)", out, enabled)
	}
	if toolScope["enable_remote_control"] == "" {
		t.Fatal("the web connector needs a scope for the new tool")
	}
}
