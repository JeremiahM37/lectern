package sessions

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/store"
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

func TestProfileInstructionsAreCapturedWithTheLaunchNotReResolved(t *testing.T) {
	m, row := pollRig(t)
	m.Specs = func() []Spec {
		return []Spec{{Name: row.Agent, Command: "base-agent", Env: map[string]string{"ENDPOINT": "base"}}}
	}
	p, err := m.DB.SaveLaunchProfile(&store.LaunchProfile{
		Name: "Briefed", Agent: row.Agent, EnvJSON: "{}", Instructions: "first briefing\nsecond line",
	})
	if err != nil {
		t.Fatal(err)
	}
	opts, err := m.ApplyLaunchProfile(LaunchOpts{ProfileID: p.ID})
	if err != nil || opts.Configuration == nil || opts.Configuration.ProfileInstructions != "first briefing\nsecond line" {
		t.Fatalf("profile instructions not captured: %+v %v", opts.Configuration, err)
	}
	// Editing the reusable profile afterwards must not rewrite the snapshot a
	// continuation reuses.
	p.Instructions = "edited briefing"
	if _, err := m.DB.SaveLaunchProfile(p); err != nil {
		t.Fatal(err)
	}
	got, err := m.SessionLaunchConfiguration(&store.Session{Agent: row.Agent, LaunchConfigJSON: store.J(opts.Configuration)})
	if err != nil || got.ProfileInstructions != "first briefing\nsecond line" {
		t.Fatalf("continuation consulted a later profile edit: %+v %v", got, err)
	}
	briefing := profileBriefing(got)
	if !strings.Contains(briefing, "first briefing") || !strings.Contains(briefing, "Briefed") {
		t.Fatalf("briefing is not labelled with its profile: %q", briefing)
	}
	if profileBriefing(&LaunchConfiguration{ProfileName: "Blank"}) != "" {
		t.Fatal("a profile without instructions produced a briefing")
	}
}

func TestExplicitlySelectedProfileBriefsAResumeSwitch(t *testing.T) {
	m, row := pollRig(t)
	m.Specs = func() []Spec {
		return []Spec{{Name: row.Agent, Command: "base-agent", Env: map[string]string{}}}
	}
	p, err := m.DB.SaveLaunchProfile(&store.LaunchProfile{
		Name: "Review mode", Agent: row.Agent, EnvJSON: "{}", Instructions: "review the diff before editing",
	})
	if err != nil {
		t.Fatal(err)
	}
	next, err := m.handoffLaunch(row, HandoffOpts{Successor: true, ProfileID: p.ID})
	if err != nil {
		t.Fatal(err)
	}
	if next.Configuration == nil || next.Configuration.ProfileInstructions != "review the diff before editing" || next.ProfileID != 0 {
		t.Fatalf("an explicitly chosen profile was not captured for the switch: %+v", next.Configuration)
	}
}
