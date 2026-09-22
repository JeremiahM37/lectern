package console

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func workflowServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	writes := []string{}
	enabled := map[string]bool{}
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			if r.URL.Path != "/api/projects/7/workflows" {
				t.Errorf("unexpected read: %s", r.URL)
			}
			agent := r.URL.Query().Get("agent")
			_ = json.NewEncoder(w).Encode(map[string]any{"workflows": []workflowOption{
				{ID: "spec-kit", Name: "Spec Kit", Enabled: enabled[agent+"/spec-kit"], Commands: []string{"lectern-spec-kit specify"}},
				{ID: "maestro", Name: "Maestro", Enabled: enabled[agent+"/maestro"], Commands: []string{"lectern-maestro diagnose"}},
			}})
			return
		}
		var body struct {
			Agent   string `json:"agent"`
			Enabled bool   `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		key := body.Agent + "/" + r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		enabled[key] = body.Enabled
		state := "off"
		if body.Enabled {
			state = "on"
		}
		writes = append(writes, r.Method+" "+r.URL.Path+" "+body.Agent+" "+state)
		_, _ = w.Write([]byte(`{"reload_required":true}`))
	}))
	t.Cleanup(s.Close)
	return s, &writes
}

func TestWorkflowFormBindsProjectAndProviderAndOnlyWritesSelectedPack(t *testing.T) {
	s, writes := workflowServer(t)
	m := newDashboard(New(s.URL, ""), nil)
	m.section = 3
	m.rows = []row{{"id": float64(7), "name": "Project", "default_agent": "codex"}}
	m.filter()
	load := m.workflowsForm()
	if load == nil {
		t.Fatal("workflow action did not load")
	}
	m.Update(load())
	if m.form == nil {
		t.Fatal(m.notice)
	}
	if m.form.fields[0].Value != "codex" {
		t.Fatal("project provider was not selected")
	}
	if !strings.Contains(m.form.fields[1].Options[0].Label, "claude off / codex off") {
		t.Fatal("current availability is missing")
	}
	// Refreshing the project list must not redirect an open form's write.
	m.rows = []row{{"id": float64(99), "name": "Another project"}}
	m.filter()
	cmd := m.form.submit(map[string]any{"agent": "codex", "workflow": "maestro", "operation": "enable"})
	if cmd == nil {
		t.Fatal(m.notice)
	}
	result := cmd().(resultMsg)
	if result.err != nil {
		t.Fatal(result.err)
	}
	if got := strings.Join(*writes, "\n"); got != "PUT /api/projects/7/workflows/maestro codex on" {
		t.Fatal(got)
	}
}

func TestPlainWorkflowMenuTogglesWithoutTouchingOtherPacks(t *testing.T) {
	s, writes := workflowServer(t)
	var output bytes.Buffer
	u := NewUI(New(s.URL, ""), strings.NewReader("1\n2\n1\nb\n"), &output, nil)
	if err := u.workflowSettings("/projects/7", map[string]any{"default_agent": "claude"}); err != nil {
		t.Fatal(err)
	}
	want := "PUT /api/projects/7/workflows/spec-kit claude on\nPUT /api/projects/7/workflows/maestro claude on\nPUT /api/projects/7/workflows/spec-kit claude off"
	if got := strings.Join(*writes, "\n"); got != want {
		t.Fatal(got)
	}
	for _, expected := range []string{"Spec Kit: on", "Maestro: on", "generated documents are preserved", "lectern-maestro diagnose"} {
		if !strings.Contains(output.String(), expected) {
			t.Fatalf("missing %s", expected)
		}
	}
}
