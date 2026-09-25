package sessions

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/isolation"
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

// TestLaunchConfigurationIsolationRoundTrips covers the isolation task's
// "launch config persistence" requirement: a saved LaunchConfiguration's
// Isolation survives the exact JSON round trip SessionLaunchConfiguration
// uses, the same way ProfileName/Yolo/PermissionMode already do above.
func TestLaunchConfigurationIsolationRoundTrips(t *testing.T) {
	m, row := pollRig(t)
	cfg := isolation.Config{Mode: isolation.Bwrap, Network: isolation.NetworkDeny, AllowHosts: []string{"api.anthropic.com"}}
	original := LaunchConfiguration{Version: 1, Spec: Spec{Name: row.Agent, Command: "/usr/bin/claude"}, Isolation: cfg}
	row.LaunchConfigJSON = store.J(original)
	got, err := m.SessionLaunchConfiguration(row)
	if err != nil {
		t.Fatal(err)
	}
	if got.Isolation.Mode != isolation.Bwrap || got.Isolation.Network != isolation.NetworkDeny ||
		len(got.Isolation.AllowHosts) != 1 || got.Isolation.AllowHosts[0] != "api.anthropic.com" {
		t.Fatalf("isolation did not round-trip: %+v", got.Isolation)
	}
}

// TestFreshLaunchConfigurationUsesProjectIsolationDefault covers a fresh
// launch (no saved snapshot): it must pick up the project's own default
// isolation.Config, exactly as it already does for the project's env.
func TestFreshLaunchConfigurationUsesProjectIsolationDefault(t *testing.T) {
	m, row := pollRig(t)
	cfg := isolation.Config{Mode: isolation.Docker, Network: isolation.NetworkAllow}
	proj, err := m.DB.InsertProject(&store.Project{
		Name: "iso-project", TargetID: row.TargetID, RepoPath: t.TempDir(),
		DefaultIsolationJSON: store.J(cfg),
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := m.launchConfiguration("claude", &proj.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Isolation.Mode != isolation.Docker {
		t.Fatalf("expected the project's default isolation mode, got %+v", got.Isolation)
	}
	if got.Isolation.DockerImage != isolation.DefaultDockerImage {
		t.Fatalf("expected Normalized() to have filled in the default image, got %+v", got.Isolation)
	}

	// No project at all: isolation stays none, same as every field it
	// otherwise defaults from.
	none, err := m.launchConfiguration("claude", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if none.Isolation.Mode != isolation.None {
		t.Fatalf("expected no isolation with no project, got %+v", none.Isolation)
	}

	// A continuation (saved != nil) must NOT be overridden by the project's
	// default — it keeps exactly what it was launched with.
	saved := &LaunchConfiguration{Version: 1, Spec: Spec{Name: "claude", Command: "/usr/bin/claude"}, Isolation: isolation.Config{}}
	continued, err := m.launchConfiguration("claude", &proj.ID, saved)
	if err != nil {
		t.Fatal(err)
	}
	if continued.Isolation.Mode != isolation.None {
		t.Fatalf("a continuation must keep its own captured isolation, got %+v", continued.Isolation)
	}
}

// TestValidateForTargetRejectsIsolationOnPct is the manager-level half of
// isolation.ValidateForTarget: launch() must refuse an isolation config the
// target cannot honestly support before ever starting a tmux pane.
func TestValidateForTargetRejectsIsolationOnPct(t *testing.T) {
	if err := isolation.ValidateForTarget(isolation.Config{Mode: isolation.Bwrap, Network: isolation.NetworkDeny}, "pct"); err == nil {
		t.Fatal("expected network=deny on a pct target to be rejected")
	}
}
