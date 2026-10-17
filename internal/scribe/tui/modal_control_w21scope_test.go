package tui

// Tests for the "Save to" scope bar on the Vim Hints and Agent Features
// pickers (audit item 23, spec 02 §1.2 and §4). The bar picks the scope each
// toggle writes to, starts where the setting already lives, and moves a saved
// setting when the bar changes.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

// w21Picker returns the picker on top of the stack, failing the test otherwise.
func w21Picker(t *testing.T, m *Model) *Picker {
	t.Helper()
	p, ok := m.topModal().(*Picker)
	if !ok {
		t.Fatalf("expected a picker, top modal is %T", m.topModal())
	}
	return p
}

// writeProjectConfig writes the project config file for root, so the scope a
// setting already lives in can be tested.
func writeProjectConfig(t *testing.T, root, body string) {
	t.Helper()
	path := config.ProjectPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestVimHintsHasAProjectAndGlobalScopeBar(t *testing.T) {
	m, _ := newFlat7Backend(t)
	m.vimCommand("")
	p := w21Picker(t, m)
	if got := strings.Join(p.ScopeOptions, ","); got != "project,global" {
		t.Fatalf("scope options = %q, want project,global", got)
	}
	if p.Scope() != "global" {
		t.Fatalf("with nothing saved in the project the bar should start at global, got %q", p.Scope())
	}
	if bar := p.scopeBar(); !strings.Contains(bar, "[global]") || !strings.Contains(bar, "Save to") {
		t.Fatalf("scope bar %q does not show the chosen scope", bar)
	}
}

func TestVimHintsStartsAtProjectWhenTheProjectSetsIt(t *testing.T) {
	m, b := newFlat7Backend(t)
	writeProjectConfig(t, b.root, `{"vim": true}`)
	m.vimCommand("")
	if p := w21Picker(t, m); p.Scope() != "project" {
		t.Fatalf("the project file sets vim, so the bar should start at project, got %q", p.Scope())
	}
}

func TestVimHintsToggleSavesToTheChosenScope(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.vimCommand("")
	send(m, "ctrl+o") // global -> project
	if p := w21Picker(t, m); p.Scope() != "project" {
		t.Fatalf("ctrl+o should move the bar to project, got %q", p.Scope())
	}
	pressSpace(t, m)
	last := b.FakeBackend.writes[len(b.FakeBackend.writes)-1]
	if last.Key != "vim" || last.Value != true || !last.Project {
		t.Fatalf("space with the bar on project should save vim on to the project, got %+v", last)
	}
}

func TestVimHintsScopeChangeMovesASavedSetting(t *testing.T) {
	m, b := newFlat7Backend(t)
	writeProjectConfig(t, b.root, `{"vim": true}`)
	m.vimCommand("")
	send(m, "ctrl+o") // project -> global, the setting lives in project
	if !containsString(b.FakeBackend.ops, "move vim global") {
		t.Fatalf("moving the bar to global should move vim there, ops = %v", b.FakeBackend.ops)
	}
}

func TestVimHintsScopeChangeLeavesAnUnsavedSettingAlone(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.vimCommand("")
	send(m, "ctrl+o") // global -> project; vim is saved in neither file
	for _, op := range b.FakeBackend.ops {
		if strings.HasPrefix(op, "move vim") {
			t.Fatalf("an unsaved setting should not be moved, ops = %v", b.FakeBackend.ops)
		}
	}
}

func TestAgentFeaturesToggleSavesToTheChosenScope(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.openAgentFeatures()
	send(m, "ctrl+o") // global -> project
	send(m, "enter")  // toggle the first feature
	last := b.writes[len(b.writes)-1]
	if last.Key != "agent_features.desloppify" || last.Value != true || !last.Project {
		t.Fatalf("enter with the bar on project should save the feature to the project, got %+v", last)
	}
}

func TestAgentFeaturesStartsAtProjectWhenTheProjectSetsAFeature(t *testing.T) {
	m, b := newFlat7Backend(t)
	writeProjectConfig(t, b.root, `{"agent_features": {"desloppify": true}}`)
	m.openAgentFeatures()
	if p := w21Picker(t, m); p.Scope() != "project" {
		t.Fatalf("the project sets a feature, so the bar should start at project, got %q", p.Scope())
	}
}

func TestAgentFeaturesScopeChangeMovesOnlySavedFeatures(t *testing.T) {
	m, b := newFlat7Backend(t)
	on := true
	b.cfg.AgentFeatures = &config.AgentFeatures{Desloppify: &on}
	writeProjectConfig(t, b.root, `{"agent_features": {"desloppify": true}}`)
	m.openAgentFeatures()
	send(m, "ctrl+o") // project -> global

	moved := b.moves
	if len(moved) != 1 || moved[0].Key != "agent_features.desloppify" || moved[0].Project || moved[0].Value != true {
		t.Fatalf("only desloppify, which the project saves, should move to global with its value; moves = %+v", moved)
	}
}

func TestAgentFeaturesScopeChangeWithNothingSavedMovesNothing(t *testing.T) {
	m, b := newFlat7Backend(t)
	m.openAgentFeatures()
	send(m, "ctrl+o") // global -> project; no feature is saved in either file
	if len(b.moves) != 0 {
		t.Fatalf("no feature is saved, so nothing should move; moves = %+v", b.moves)
	}
}

// containsString reports whether list holds s.
func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
