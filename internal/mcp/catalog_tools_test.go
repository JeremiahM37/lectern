package mcp

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func catalogFake(t *testing.T, created *map[string]any, started *map[string]any) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agents", jsonHandler(200, []map[string]any{
		{"name": "claude", "model_flag": "--model", "yolo_args": []string{"--permission-mode", "bypassPermissions"}},
		{"name": "gemini"},
	}))
	mux.HandleFunc("GET /api/agents/capabilities", jsonHandler(200, map[string]any{
		"claude": map[string]any{"installed": true}, "gemini": map[string]any{"installed": false},
	}))
	mux.HandleFunc("GET /api/models", jsonHandler(200, map[string]any{"claude": []string{"opus", "claude-opus-5-5", "claude-haiku-5-5"}}))
	mux.HandleFunc("GET /api/targets", jsonHandler(200, []map[string]any{{"id": 4.0, "kind": "ssh"}, {"id": 1.0, "kind": "local"}}))
	mux.HandleFunc("GET /api/projects", jsonHandler(200, []map[string]any{{"id": 1.0, "name": "existing", "repo_path": "/x"}}))
	mux.HandleFunc("POST /api/projects", func(w http.ResponseWriter, r *http.Request) {
		*created = decodeBody(t, r)
		jsonHandler(201, map[string]any{"id": 9.0, "name": (*created)["name"]})(w, r)
	})
	mux.HandleFunc("POST /api/sessions", func(w http.ResponseWriter, r *http.Request) {
		*started = decodeBody(t, r)
		jsonHandler(201, map[string]any{"id": 3.0, "name": "s", "status": "starting"})(w, r)
	})
	return httptest.NewServer(mux)
}

func TestListAgentsReportsInstalledAndModels(t *testing.T) {
	var c, s map[string]any
	srv := catalogFake(t, &c, &s)
	defer srv.Close()
	out, err := New(srv.URL, "").call("list_agents", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	rows := out.([]map[string]any)
	if len(rows) != 2 || rows[0]["name"] != "claude" || rows[0]["installed"] != true || rows[0]["takes_model"] != true || rows[0]["skip_approvals"] != true {
		t.Fatalf("unexpected %v", rows)
	}
	if rows[1]["installed"] != false {
		t.Fatalf("gemini must read as not installed: %v", rows[1])
	}
}

func TestCreateProjectCreatesDirGitAndWorkerEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	var c, s map[string]any
	srv := catalogFake(t, &c, &s)
	defer srv.Close()
	out, err := New(srv.URL, "").call("create_project", map[string]any{
		"name": "zz-new", "worker_model": "claude-haiku-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, "projects", "zz-new")
	if c["repo_path"] != want || c["target_id"] != float64(1) {
		t.Fatalf("project body wrong: %v", c)
	}
	if env, _ := c["env"].(map[string]any); env[subagentModelEnv] != "claude-haiku-5-5" {
		t.Fatalf("worker model not set in env: %v", c["env"])
	}
	if _, err := os.Stat(filepath.Join(want, ".git")); err != nil {
		t.Fatalf("git repo not created: %v", err)
	}
	if m := out.(map[string]any); m["model_warning"] != nil {
		t.Fatalf("listed model must not warn: %v", m)
	}
}

func TestCreateProjectRefusesExistingAndEscapes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var c, s map[string]any
	srv := catalogFake(t, &c, &s)
	defer srv.Close()
	m := New(srv.URL, "")
	if _, err := m.call("create_project", map[string]any{"name": "Existing"}); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("want already-exists, got %v", err)
	}
	for _, p := range []string{"/etc/evil", "../x", "a/b"} {
		if _, err := m.call("create_project", map[string]any{"name": "n", "path": p}); err == nil {
			t.Fatalf("path %q must be refused", p)
		}
	}
	if c != nil {
		t.Fatal("no project may be registered after refusals")
	}
}

// The user's model is passed through untouched, listed or not, and an
// unlisted one is flagged rather than substituted.
func TestStartSessionPassesModelVerbatim(t *testing.T) {
	var c, s map[string]any
	srv := catalogFake(t, &c, &s)
	defer srv.Close()
	m := New(srv.URL, "")
	out, err := m.call("start_session", map[string]any{"prompt": "go", "workdir": "/w", "model": "claude-opus-5-5"})
	if err != nil {
		t.Fatal(err)
	}
	if s["model"] != "claude-opus-5-5" || s["yolo"] != true {
		t.Fatalf("model/yolo not passed: %v", s)
	}
	if out.(map[string]any)["model_warning"] != nil {
		t.Fatalf("listed model must not warn")
	}
	out, err = m.call("start_session", map[string]any{"prompt": "go", "workdir": "/w", "model": "claude-opus-9-9"})
	if err != nil {
		t.Fatal(err)
	}
	if s["model"] != "claude-opus-9-9" {
		t.Fatalf("unlisted model must be passed verbatim: %v", s)
	}
	if w, _ := out.(map[string]any)["model_warning"].(string); !strings.Contains(w, "passed to the agent verbatim") {
		t.Fatalf("unlisted model must carry a warning: %v", out)
	}
}

func TestStartSessionUnknownProjectPointsAtCreateProject(t *testing.T) {
	var c, s map[string]any
	srv := catalogFake(t, &c, &s)
	defer srv.Close()
	_, err := New(srv.URL, "").call("start_session", map[string]any{"prompt": "go", "project": "nope"})
	if err == nil || !strings.Contains(err.Error(), "create_project") {
		t.Fatalf("got %v", err)
	}
}
