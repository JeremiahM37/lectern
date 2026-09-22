package sessions

import (
	"testing"

	"github.com/JeremiahM37/lectern/internal/store"
)

func TestLaunchProfileExplicitOverridesAndAgentBoundary(t *testing.T) {
	m, row := pollRig(t)
	m.Specs = func() []Spec {
		return []Spec{{Name: row.Agent, Command: "base-agent", Env: map[string]string{"ENDPOINT": "base"}}}
	}
	p, err := m.DB.SaveLaunchProfile(&store.LaunchProfile{Name: "Local model", Agent: row.Agent, Model: "profile-model", EnvJSON: `{"ENDPOINT":"profile"}`})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := m.ApplyLaunchProfile(LaunchOpts{ProfileID: p.ID, Model: "explicit-model", Yolo: false})
	if err != nil || opts.Model != "explicit-model" || opts.Agent != row.Agent || opts.Configuration.Spec.Env["ENDPOINT"] != "profile" || opts.Configuration.Yolo {
		t.Fatal("explicit model or profile settings were lost", err)
	}
	if m.specs()[0].Env["ENDPOINT"] != "base" {
		t.Fatal("resolving a profile mutated the shared agent definition")
	}
	for _, in := range []LaunchOpts{{ProfileID: p.ID, Agent: "another-agent"}, {ProfileID: p.ID, Configuration: opts.Configuration}} {
		if _, err := m.ApplyLaunchProfile(in); err == nil {
			t.Fatal("profile crossed an agent or captured-configuration boundary")
		}
	}
}
