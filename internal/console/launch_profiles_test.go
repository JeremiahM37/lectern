package console

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLaunchProfilesAvailableWithoutSessions(t *testing.T) {
	m := sampleDashboard()
	m.rows, m.visible = nil, nil
	want := map[string]bool{"blank-shell": false, "launch-profiles": false, "recent-sessions": false}
	for _, a := range m.actions() {
		if _, ok := want[a.Operation]; ok {
			want[a.Operation] = true
		}
	}
	for op, found := range want {
		if !found {
			t.Fatalf("empty session dashboard missing %s", op)
		}
	}
	m.Update(key("P"))
	if m.form == nil || m.form.fields[0].Value != "new" {
		t.Fatal("P did not open profile management")
	}
}

func TestSessionFormUsesSelectedProfileAgentAndKeepsDraftOnError(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/api/sessions" {
			t.Error(r.URL.Path)
		}
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(409)
		w.Write([]byte(`{"detail":"profile temporarily unavailable"}`))
	}))
	defer srv.Close()
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	m.profiles = []row{{"id": float64(7), "name": "Work account", "agent": "codex"}}
	m.newForm()
	for i := range m.form.fields {
		switch m.form.fields[i].Key {
		case "name":
			m.form.fields[i].Value = "keep profile draft"
		case "profile_id":
			m.form.fields[i].Value = "7"
		case "agent":
			m.form.fields[i].Value = "claude"
		}
	}
	body, err := formBody(m.form.fields)
	if err != nil {
		t.Fatal(err)
	}
	cmd := m.form.submit(body)
	m.Update(cmd())
	if received["profile_id"] != float64(7) || received["agent"] != nil {
		t.Fatal("profile selection sent a conflicting agent", received)
	}
	if m.form == nil || m.form.fields[0].Value != "keep profile draft" {
		t.Fatal("launch failure lost the profile draft")
	}
}

func TestProfileFormDeclaresBriefingFieldsAndClearsExplicitly(t *testing.T) {
	var received map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(200)
		w.Write([]byte(`{}`))
	}))
	defer srv.Close()
	m := sampleDashboard()
	m.client = New(srv.URL, "")
	m.agents = []row{{"name": "codex"}}
	m.profiles = []row{{"id": float64(7), "name": "Work account", "agent": "codex",
		"description": "old description", "instructions": "old briefing"}}
	if cmd := m.profileSettingsForm(m.profiles[0]); cmd == nil {
		t.Fatal("profile form did not open")
	}
	index := map[string]int{}
	for i := range m.form.fields {
		index[m.form.fields[i].Key] = i
	}
	desc, ok := index["description"]
	if !ok {
		t.Fatal("profile form has no description field")
	}
	ins, ok := index["instructions"]
	if !ok {
		t.Fatal("profile form has no instructions field")
	}
	if !m.form.fields[ins].Multiline {
		t.Fatal("instructions should be a multiline field")
	}
	// The operator edits the description and clears the briefing.
	m.form.fields[desc].Value = "new description"
	m.form.fields[ins].Value = ""
	body, err := formBody(m.form.fields)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := body["instructions"]; present {
		t.Fatal("formBody unexpectedly kept the blank instructions field")
	}
	cmd := m.form.submit(body)
	if cmd == nil {
		t.Fatal("submit produced no request")
	}
	cmd()
	if _, present := received["instructions"]; !present || received["instructions"] != "" {
		t.Fatalf("cleared briefing must be sent as an explicit empty string: %v", received)
	}
	if received["description"] != "new description" {
		t.Fatalf("description not sent: %v", received)
	}
}
