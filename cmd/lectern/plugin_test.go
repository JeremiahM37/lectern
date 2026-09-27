package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/console"
)

func TestPluginNewThenValidate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "hello")
	var out bytes.Buffer
	if ok, err := pluginOffline([]string{"new", "acme.hello", dir}, &out); !ok || err != nil {
		t.Fatalf("new: %v %v", ok, err)
	}
	if info, err := os.Stat(filepath.Join(dir, "hooks", "on_finish.py")); err != nil || info.Mode().Perm()&0o100 == 0 {
		t.Fatalf("hook script not executable: %v", err)
	}
	out.Reset()
	if ok, err := pluginOffline([]string{"validate", dir}, &out); !ok || err != nil {
		t.Fatalf("validate: %v %v", ok, err)
	}
	if !strings.Contains(out.String(), "acme.hello 0.1.0 — valid") || !strings.Contains(out.String(), "host_exec") {
		t.Fatalf("validate output:\n%s", out.String())
	}
	// A broken manifest is reported with every problem.
	os.WriteFile(filepath.Join(dir, "lectern-plugin.yaml"), []byte("id: acme.hello\nname: x\nversion: 1\ncapabilities: {}\ncontributes:\n  hooks: [{event: task.finished, run: host, command: [true]}]\n"), 0o644)
	if _, err := pluginOffline([]string{"validate", dir}, &out); err == nil || !strings.Contains(err.Error(), "host_exec") {
		t.Fatalf("invalid manifest: %v", err)
	}
	if ok, _ := pluginOffline([]string{"list"}, &out); ok {
		t.Fatal("list was handled offline")
	}
}

func TestPluginInstallAsksAndConsentsToExactlyWhatWasShown(t *testing.T) {
	var installs []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/plugins/preview":
			json.NewEncoder(w).Encode(map[string]any{"hash": "abc", "manifest": map[string]any{"id": "acme.x", "version": "1"},
				"capabilities": []map[string]any{{"key": "host_exec", "detail": []string{"task.finished: run"}}},
				"accept": []string{"host_exec: task.finished: run"}, "contributions": map[string]int{"hooks": 1}})
		case "/api/plugins/install":
			var body map[string]any
			raw, _ := io.ReadAll(r.Body)
			json.Unmarshal(raw, &body)
			installs = append(installs, body)
			w.Write([]byte(`{"id":"acme.x","status":"active"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	c := console.New(srv.URL, "")
	raw, _ := c.JSON("POST", "/api/plugins/preview", map[string]any{})
	var out bytes.Buffer
	if _, err := consentFlow(c, raw, pluginFlags{}, &out, strings.NewReader("n\n")); err == nil || len(installs) != 0 {
		t.Fatalf("declining still installed: %v %v", err, installs)
	}
	if !strings.Contains(out.String(), "task.finished: run") {
		t.Fatalf("the prompt did not show the capability:\n%s", out.String())
	}
	if _, err := consentFlow(c, raw, pluginFlags{projects: []int64{3}}, &out, strings.NewReader("y\n")); err != nil {
		t.Fatal(err)
	}
	if len(installs) != 1 || installs[0]["hash"] != "abc" || strs(installs[0]["accept"]) != "host_exec: task.finished: run" {
		t.Fatalf("install body %v", installs)
	}
}

func strs(v any) string {
	var out []string
	for _, x := range v.([]any) {
		out = append(out, x.(string))
	}
	return strings.Join(out, ",")
}
