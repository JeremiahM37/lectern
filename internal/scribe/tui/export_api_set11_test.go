package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
)

func TestExportAPIToggleFlipsTheDump(t *testing.T) {
	fakeAPIDumpOn = false
	t.Cleanup(func() { fakeAPIDumpOn = false })
	m, _ := newTestModel(t, 100, 40)
	if got := runFlash(t, m, m.runCommand("/session export api")); !strings.Contains(got, "API request dump on") {
		t.Fatalf("first toggle flash = %q, want the dump turned on", got)
	}
	if got := runFlash(t, m, m.runCommand("/session export api")); !strings.Contains(got, "API request dump off") {
		t.Fatalf("second toggle flash = %q, want the dump turned off", got)
	}
}

func TestExportAPIExplicitOnOff(t *testing.T) {
	fakeAPIDumpOn = false
	t.Cleanup(func() { fakeAPIDumpOn = false })
	m, _ := newTestModel(t, 100, 40)
	runFlash(t, m, m.runCommand("/export api on"))
	if !fakeAPIDumpOn {
		t.Fatal("export api on did not switch the dump on")
	}
	runFlash(t, m, m.runCommand("/export api off"))
	if fakeAPIDumpOn {
		t.Fatal("export api off did not switch the dump off")
	}
}

func TestExportAPIRejectsOtherWords(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	if got := runFlash(t, m, m.runCommand("/export api maybe")); !strings.Contains(got, "Usage: /session export api") {
		t.Fatalf("flash = %q, want the api usage line", got)
	}
}

func TestPresetNamesSplitOnCommasAndSpaces(t *testing.T) {
	got := presetNames(" a, b  c ")
	if strings.Join(got, "|") != "a|b|c" {
		t.Fatalf("presetNames = %v", got)
	}
}

func TestPresetRemoveArgs(t *testing.T) {
	if names, ok := presetRemoveArgs("remove a b"); !ok || strings.Join(names, "|") != "a|b" {
		t.Fatalf("remove a b = %v %v", names, ok)
	}
	if _, ok := presetRemoveArgs("remove"); ok {
		t.Fatal("a bare remove must not be a removal")
	}
	if _, ok := presetRemoveArgs("fast"); ok {
		t.Fatal("a plain name must apply, not remove")
	}
}

func TestEditorSettingsOpensTheIntegrationToggles(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	m.runCommand("/editor settings")
	p, ok := m.modals[len(m.modals)-1].(*Picker)
	if !ok {
		t.Fatalf("top modal = %T, want the editor integrations picker", m.modals[len(m.modals)-1])
	}
	if p.Title() != "Editor integrations" || len(p.Items) != len(config.EditorIntegrationKeys) {
		t.Fatalf("picker %q with %d rows, want the %d integration toggles", p.Title(), len(p.Items), len(config.EditorIntegrationKeys))
	}
	for _, it := range p.Items {
		if it.Kind != PickToggle {
			t.Fatalf("row %q is not a toggle", it.Label)
		}
	}
}

func TestProviderKeyStatusNamesItsSource(t *testing.T) {
	if got := keyStatus(ProviderStatus{HasKey: true, KeySource: "env:OPENAI_API_KEY"}); got != "✓ [env] OPENAI_API_KEY" {
		t.Fatalf("env status = %q", got)
	}
	if got := keyStatus(ProviderStatus{HasKey: true, KeySource: "store"}); got != "✓ [config]" {
		t.Fatalf("store status = %q", got)
	}
	if got := keyStatus(ProviderStatus{}); got != "✗ no key" {
		t.Fatalf("no key status = %q", got)
	}
}

func TestCompactSettingsOffersThresholds(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	m.openCompactStrategy()
	p, ok := m.modals[len(m.modals)-1].(*Picker)
	if !ok {
		t.Fatalf("top modal = %T, want the compaction picker", m.modals[len(m.modals)-1])
	}
	var labels []string
	for _, it := range p.Items {
		labels = append(labels, it.Label)
	}
	joined := strings.Join(labels, "|")
	for _, want := range []string{"v2", "Disabled", "60%", "80%", "90%"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("picker items %q lack %q", joined, want)
		}
	}
	if th, ok := thresholdFor(thresholdValue(0.7)); !ok || th.label != "70%" {
		t.Fatalf("thresholdFor(0.70) = %+v, %v", th, ok)
	}
	if _, ok := thresholdFor("v2"); ok {
		t.Fatal("a strategy value must not read as a threshold")
	}
}
