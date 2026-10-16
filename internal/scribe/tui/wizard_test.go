package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// wizardOpen starts a model with the setup wizard on, the way Init does.
func wizardOpen(t *testing.T, w, h int) (*Model, *FakeBackend, *wizard) {
	t.Helper()
	m, fb := newTestModel(t, w, h)
	fb.NeedSetup = true
	m.maybeOpenWizard()
	wz, ok := m.topModal().(*wizard)
	if !ok {
		t.Fatalf("the wizard should open when the backend needs setup; top modal %T", m.topModal())
	}
	return m, fb, wz
}

func TestWizardOpensOnStartOnlyWhenNeeded(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	fb.NeedSetup = false
	m.Init()
	if m.topModal() != nil {
		t.Fatalf("no wizard when a model is usable, got %T", m.topModal())
	}

	m2, fb2 := newTestModel(t, 100, 30)
	fb2.NeedSetup = true
	m2.Init()
	if _, ok := m2.topModal().(*wizard); !ok {
		t.Fatalf("Init should open the wizard when setup is needed, got %T", m2.topModal())
	}
	if !strings.Contains(screen(m2), "Scribe is a coding agent") {
		t.Fatalf("the welcome step should explain Scribe:\n%s", screen(m2))
	}
}

func TestWizardSlashSetupReopens(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.runCommand("/setup")
	if _, ok := m.topModal().(*wizard); !ok {
		t.Fatalf("/setup should open the wizard, got %T", m.topModal())
	}
}

func TestWizardEndToEnd(t *testing.T) {
	const key = "sk-test-openai-key-7781"
	m, fb, wz := wizardOpen(t, 100, 34)
	drain(m, wz.start())

	send(m, "enter") // welcome -> provider
	if !strings.Contains(screen(m), "Choose a provider") {
		t.Fatalf("expected the provider step:\n%s", screen(m))
	}
	send(m, "down") // anthropic -> openai (no key yet)
	send(m, "enter")
	if !strings.Contains(screen(m), "Set your OpenAI key") {
		t.Fatalf("expected the key step for OpenAI:\n%s", screen(m))
	}

	typeText(m, key)
	if got := screen(m); strings.Contains(got, key) || !strings.Contains(got, "•") {
		t.Fatalf("the key must be masked while typing:\n%s", got)
	}

	drain(m, send(m, "enter")) // save and check: SetKey, then Models
	if fb.keys["openai"] != key {
		t.Fatalf("SetKey was not called with the typed key; stored %q", fb.keys["openai"])
	}
	if got := screen(m); strings.Contains(got, key) {
		t.Fatalf("the key must not be drawn after saving:\n%s", got)
	}
	if !strings.Contains(screen(m), "Pick a model from OpenAI") || !strings.Contains(screen(m), "GPT-6") {
		t.Fatalf("expected the model step with OpenAI's models:\n%s", screen(m))
	}

	drain(m, send(m, "enter")) // the default model, GPT-6, is preselected
	if fb.Model() != "openai/gpt-6" {
		t.Fatalf("SetModel was not called with the chosen model; model %q", fb.Model())
	}
	if got := lastWrite(t, fb); got.Key != "model" || got.Value != "openai/gpt-6" || got.Project {
		t.Fatalf("the model should be saved to the global config: %+v", got)
	}
	if !strings.Contains(screen(m), "Show Nerd Font icons?") {
		t.Fatalf("expected the icons step after the model:\n%s", screen(m))
	}

	drain(m, send(m, "enter")) // icons: keep the default (off) and move on
	if !strings.Contains(screen(m), "Pick your theme") {
		t.Fatalf("expected the theme step:\n%s", screen(m))
	}

	names := theme.Names()
	start := listIndex(names, m.themeName)
	send(m, "down")
	if m.themeName != names[start+1] {
		t.Fatalf("moving the theme cursor should preview it; themeName=%q", m.themeName)
	}
	send(m, "enter")
	if fb.Theme() != names[start+1] {
		t.Fatalf("Enter should save the theme %q, backend has %q", names[start+1], fb.Theme())
	}

	if !strings.Contains(screen(m), "You're all set") {
		t.Fatalf("expected the ready step:\n%s", screen(m))
	}
	send(m, "enter")
	if m.topModal() != nil {
		t.Fatalf("Enter on the ready step should close the wizard, top modal %T", m.topModal())
	}
}

func TestWizardBadKeyShowsErrorAndStaysOnKeyStep(t *testing.T) {
	m, fb, wz := wizardOpen(t, 100, 34)
	drain(m, wz.start())
	send(m, "enter")
	send(m, "down")
	send(m, "enter")
	typeText(m, "   ")
	drain(m, send(m, "enter"))
	if !strings.Contains(wz.keyErr, "Paste or type") {
		t.Fatalf("an empty key should be refused, keyErr %q", wz.keyErr)
	}

	// The fake accepts any key that is not empty, so give it an unknown
	// provider to make Models fail after SetKey.
	wz.chosen = ProviderStatus{ID: "nosuch", Name: "Nope"}
	typeText(m, "k")
	drain(m, send(m, "enter"))
	if wz.step != wzKey || !strings.Contains(wz.keyErr, "not accepted") {
		t.Fatalf("a rejected key should keep the wizard on the key step; step %d, err %q", wz.step, wz.keyErr)
	}
	if _, ok := fb.keys["nosuch"]; !ok {
		t.Fatal("SetKey should have been called for the chosen provider")
	}
}

func TestWizardBackAndSkip(t *testing.T) {
	m, _, wz := wizardOpen(t, 100, 30)
	drain(m, wz.start())
	send(m, "enter")
	send(m, "esc")
	if wz.step != wzWelcome {
		t.Fatalf("Esc on the provider step should go back to welcome, step %d", wz.step)
	}
	send(m, "esc")
	if m.topModal() != nil {
		t.Fatal("Esc on welcome should close the wizard")
	}
}

func TestWizardLocalProviderNotRunning(t *testing.T) {
	m, _, wz := wizardOpen(t, 100, 34)
	drain(m, wz.start())
	wz.detect["ollama"] = 2
	send(m, "enter")
	send(m, "down")
	send(m, "down") // ollama
	send(m, "enter")
	if wz.step != wzProvider || !strings.Contains(wz.note, "not running") {
		t.Fatalf("Ollama that is not running should not advance; step %d, note %q", wz.step, wz.note)
	}
}

func TestWizardCustomProviderIsAdded(t *testing.T) {
	m, fb, wz := wizardOpen(t, 100, 34)
	drain(m, wz.start())
	send(m, "enter")
	for i := 0; i < len(wz.provs); i++ {
		send(m, "down")
	}
	send(m, "enter") // custom row: id
	typeText(m, "litellm")
	send(m, "enter")
	typeText(m, "http://localhost:4000/v1")
	send(m, "enter")
	send(m, "enter") // blank key variable is allowed
	got := lastWrite(t, fb)
	want := map[string]any{"id": "litellm", "base_url": "http://localhost:4000/v1", "api_key_env": ""}
	if got.Key != "provider.add" || !reflect.DeepEqual(got.Value, want) {
		t.Fatalf("custom provider: %+v", got)
	}
	if wz.step != wzKey || wz.chosen.ID != "litellm" {
		t.Fatalf("after adding, the key step should be for litellm; step %d, chosen %q", wz.step, wz.chosen.ID)
	}
}

func TestWizardLinesFitTerminalWidth(t *testing.T) {
	for _, w := range []int{80, 140} {
		m, _, wz := wizardOpen(t, w, 36)
		drain(m, wz.start())
		check := func(label string) {
			t.Helper()
			for i, l := range strings.Split(m.View(), "\n") {
				if got := ansi.StringWidth(l); got != w {
					t.Fatalf("wizard %s at %d cols: line %d is %d wide: %q", label, w, i, got, ansi.Strip(l))
				}
			}
		}
		check("welcome")
		send(m, "enter")
		check("provider")
		send(m, "down")
		send(m, "enter")
		typeText(m, "sk-abc")
		check("key")
		send(m, "esc") // back to the provider list
		send(m, "up")  // anthropic has a key: straight to the model step
		drain(m, send(m, "enter"))
		check("model")
	}
}
