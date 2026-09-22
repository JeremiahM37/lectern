package sessions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
)

func TestSavedLaunchConfigurationIgnoresLaterAgentSettings(t *testing.T) {
	m, row := pollRig(t)
	original := LaunchConfiguration{Version: 1, Spec: Spec{Name: row.Agent, Command: "/original/agent", Env: map[string]string{"CODEX_HOME": "/original/home", "TOKEN": "private-value"}, Args: []string{"--original"}, ResumeIDArgs: []string{"resume", "{id}"}}, Yolo: true}
	row.LaunchConfigJSON = store.J(original)
	m.Specs = func() []Spec { return nil }
	got, err := m.SessionLaunchConfiguration(row)
	if err != nil || got.Spec.Command != "/original/agent" || got.Spec.Env["CODEX_HOME"] != "/original/home" || !got.Yolo {
		t.Fatalf("configuration lost: %v", err)
	}
	payload, err := json.Marshal(row)
	if err != nil || strings.Contains(string(payload), "private-value") || strings.Contains(string(payload), "launch_config") {
		t.Fatal("private settings exposed")
	}
	// A bad snapshot must not silently redirect history to today's config.
	for _, bad := range []string{"null", "{", `{"version":2,"spec":{"name":"claude","command":"claude"}}`, `{"version":1,"spec":{"name":"other","command":"other"}}`} {
		row.LaunchConfigJSON = bad
		if _, err := m.SessionLaunchConfiguration(row); err == nil {
			t.Fatalf("accepted invalid snapshot %s", bad)
		}
	}
}
