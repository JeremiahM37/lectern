package tui

import (
	"reflect"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// drain runs cmd and feeds every message back into m, following the commands
// those messages return. runCmd does not follow chains; the wizard needs it.
func drain(m *Model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	msg := cmd()
	if msg == nil {
		return
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			drain(m, c)
		}
		return
	}
	_, next := m.Update(msg)
	drain(m, next)
}

// openSettings opens the settings screen the way the user does.
func openSettings(t *testing.T, m *Model) *settings {
	t.Helper()
	m.runCommand("/settings")
	s, ok := m.topModal().(*settings)
	if !ok {
		t.Fatalf("/settings did not open the settings screen; top modal is %T", m.topModal())
	}
	return s
}

// selectRow moves the cursor of the current tab onto the row with label.
func selectRow(t *testing.T, s *settings, label string) {
	t.Helper()
	for i, r := range s.rows() {
		if r.label == label {
			s.cur[s.tab] = i
			return
		}
	}
	t.Fatalf("no row %q on tab %s; rows: %v", label, stTabNames[s.tab], rowLabels(s))
}

func rowLabels(s *settings) []string {
	var out []string
	for _, r := range s.rows() {
		out = append(out, r.label)
	}
	return out
}

func lastWrite(t *testing.T, fb *FakeBackend) ConfigWrite {
	t.Helper()
	ws := fb.ConfigWrites()
	if len(ws) == 0 {
		t.Fatal("no SetConfig call was made")
	}
	return ws[len(ws)-1]
}

func TestSettingsEachTabRenders(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	s := openSettings(t, m)
	want := map[int]string{
		stGeneral:     "Checkpoints",
		stProviders:   "+ Add a custom",
		stRouter:      "Fallback chain",
		stAppearance:  theme.Names()[0],
		stPermissions: "Allow without asking",
	}
	for tab := 0; tab < stTabCount; tab++ {
		s.tab = tab
		if got := screen(m); !strings.Contains(got, want[tab]) {
			t.Fatalf("tab %s should show %q:\n%s", stTabNames[tab], want[tab], got)
		}
	}
	// Tab and Shift+Tab move between tabs and wrap.
	s.tab = stGeneral
	send(m, "shift+tab")
	if s.tab != stPermissions {
		t.Fatalf("shift+tab from General should wrap to Permissions, got tab %d", s.tab)
	}
	send(m, "tab")
	if s.tab != stGeneral {
		t.Fatalf("tab should return to General, got tab %d", s.tab)
	}
	if !strings.Contains(screen(m), "Saving to") {
		t.Fatal("the scope line should show where changes are saved")
	}
}

func TestSettingsTogglePersistsWithScope(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	s := openSettings(t, m)
	selectRow(t, s, "Checkpoints")

	send(m, "enter")
	if got, want := lastWrite(t, fb), (ConfigWrite{Key: "checkpoints", Value: false, Project: false}); !reflect.DeepEqual(got, want) {
		t.Fatalf("toggle in global scope: got %+v want %+v", got, want)
	}

	send(m, "p")
	send(m, "enter")
	if got, want := lastWrite(t, fb), (ConfigWrite{Key: "checkpoints", Value: true, Project: true}); !reflect.DeepEqual(got, want) {
		t.Fatalf("toggle after p (project): got %+v want %+v", got, want)
	}

	send(m, "g")
	send(m, " ")
	if got := lastWrite(t, fb); got.Project || got.Value != false {
		t.Fatalf("after g, space should write the global file: %+v", got)
	}
}

func TestSettingsChoiceCyclesAndStoresOffAsEmpty(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	s := openSettings(t, m)
	selectRow(t, s, "Reasoning")
	send(m, "enter") // medium -> high
	if got := lastWrite(t, fb); got.Key != "reasoning" || got.Value != "high" {
		t.Fatalf("reasoning should cycle to high: %+v", got)
	}
	send(m, "enter") // high -> off, stored as ""
	if got := lastWrite(t, fb); got.Key != "reasoning" || got.Value != "" {
		t.Fatalf("off must be stored as an empty value: %+v", got)
	}
}

func TestSettingsModelRowOpensPicker(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	s := openSettings(t, m)
	selectRow(t, s, "Default model")
	send(m, "enter")
	if _, ok := m.topModal().(*modelChooser); !ok {
		t.Fatalf("Default model should open a model chooser, got %T", m.topModal())
	}
}

func TestSettingsKeyEntryIsMasked(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	const secret = "sk-secret-value-42"
	s := openSettings(t, m)
	s.tab = stProviders
	selectRow(t, s, "OpenAI")
	send(m, "enter")
	if _, ok := m.topModal().(*TextPrompt); !ok {
		t.Fatalf("Enter on a provider should ask for its key, got %T", m.topModal())
	}
	typeText(m, secret)
	if got := screen(m); strings.Contains(got, secret) || strings.Contains(got, "secret") {
		t.Fatalf("the key must never be drawn while typing:\n%s", got)
	}
	send(m, "enter")
	if fb.keys["openai"] != secret {
		t.Fatalf("SetKey was not called with the typed key; stored %q", fb.keys["openai"])
	}
	if got := screen(m); strings.Contains(got, secret) {
		t.Fatalf("the key must not appear after saving:\n%s", got)
	}
	if !strings.Contains(screen(m), "✓ stored") {
		t.Fatal("the provider row should now show a stored key")
	}
}

func TestSettingsLocalProviderNeedsNoKey(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	s := openSettings(t, m)
	s.tab = stProviders
	selectRow(t, s, "Ollama")
	send(m, "enter")
	if m.topModal() != Modal(s) {
		t.Fatalf("a local provider should not open a key prompt, got %T", m.topModal())
	}
	if !strings.Contains(s.note, "no key") || fb.keys["ollama"] != "" {
		t.Fatalf("expected a note that no key is needed; note %q", s.note)
	}
}

func TestSettingsThemePreviewAndRevert(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	names := theme.Names()
	if len(names) < 3 {
		t.Fatalf("need at least three themes, have %v", names)
	}
	start := m.themeName
	s := openSettings(t, m)
	s.tab = stAppearance
	s.cur[stAppearance] = listIndex(names, start)

	send(m, "down")
	if m.themeName != names[listIndex(names, start)+1] {
		t.Fatalf("moving the cursor should preview the theme; themeName=%q", m.themeName)
	}
	send(m, "down")
	send(m, "esc")
	if m.topModal() != nil {
		t.Fatal("Esc should close the settings screen")
	}
	if m.themeName != start {
		t.Fatalf("Esc should revert the preview to %q, got %q", start, m.themeName)
	}
	if fb.Theme() != "scribe" {
		t.Fatalf("a reverted preview must not be saved; backend theme %q", fb.Theme())
	}

	s = openSettings(t, m)
	s.tab = stAppearance
	s.cur[stAppearance] = listIndex(names, start)
	send(m, "down")
	picked := m.themeName
	send(m, "enter")
	send(m, "esc")
	if m.themeName != picked || fb.Theme() != picked {
		t.Fatalf("Enter should keep and save %q; themeName=%q backend=%q", picked, m.themeName, fb.Theme())
	}
}

func TestSettingsRouterClearSlot(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	s := openSettings(t, m)
	s.tab = stRouter
	selectRow(t, s, "Code agents (editing)")
	send(m, "d")
	if got, want := lastWrite(t, fb), (ConfigWrite{Key: "router.code", Value: "", Project: false}); !reflect.DeepEqual(got, want) {
		t.Fatalf("d should clear the slot: got %+v", got)
	}
}

func TestSettingsRouterFallbackAndAgents(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	s := openSettings(t, m)
	s.tab = stRouter
	selectRow(t, s, "Fallback chain")
	send(m, "enter")
	typeText(m, ", groq/llama")
	send(m, "enter")
	if got := lastWrite(t, fb); got.Key != "router.fallback" || got.Value != "openai/gpt-6, groq/llama" {
		t.Fatalf("fallback chain: %+v", got)
	}

	selectRow(t, s, "Concurrent agents")
	send(m, "enter")
	send(m, "ctrl+u")
	typeText(m, "9")
	send(m, "enter")
	if !strings.Contains(s.note, "2 to 8") {
		t.Fatalf("9 agents should be refused; note %q", s.note)
	}
}

func TestSettingsPermissionsAddAndDelete(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	s := openSettings(t, m)
	s.tab = stPermissions
	selectRow(t, s, "Allow without asking")
	send(m, "a")
	typeText(m, "make test")
	send(m, "enter")
	want := []string{"go test", "git status", "make test"}
	if got := lastWrite(t, fb); got.Key != "allow" || !reflect.DeepEqual(got.Value, want) {
		t.Fatalf("add allow: got %+v want %v", got, want)
	}

	selectRow(t, s, "rm -rf")
	send(m, "d")
	if got := lastWrite(t, fb); got.Key != "deny" || len(got.Value.([]string)) != 0 {
		t.Fatalf("delete deny entry: %+v", got)
	}
}

func TestSettingsLinesFitTerminalWidth(t *testing.T) {
	for _, w := range []int{80, 140} {
		m, _ := newTestModel(t, w, 30)
		openSettings(t, m)
		for tab := 0; tab < stTabCount; tab++ {
			m.topModal().(*settings).tab = tab
			for i, l := range strings.Split(m.View(), "\n") {
				if got := ansi.StringWidth(l); got != w {
					t.Fatalf("settings tab %s at %d cols: line %d is %d wide: %q", stTabNames[tab], w, i, got, ansi.Strip(l))
				}
			}
		}
	}
}
