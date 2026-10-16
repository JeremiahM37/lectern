package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
)

// moveBackend is a FakeBackend that also moves keys between scopes, as the
// real backend does. It records each move and reports a fixed workdir.
type moveBackend struct {
	*FakeBackend
	root  string
	moves []ConfigWrite
}

func (b *moveBackend) Workdir() string { return b.root }

func (b *moveBackend) MoveConfig(key string, value any, toProject, fromProject bool) error {
	b.moves = append(b.moves, ConfigWrite{Key: key, Value: value, Project: toProject})
	return nil
}

// writeConfigFile writes a JSON config file for a scope under home or root.
func writeConfigFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSavedInFindsTopLevelAndNestedKeys(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	root := t.TempDir()
	writeConfigFile(t, config.GlobalPath(), `{"verbose": true, "router": {"explore": "a/b"}}`)
	writeConfigFile(t, config.ProjectPath(root), `{"reasoning_by_provider": {"anthropic": {"effort": "high"}}}`)

	cases := []struct {
		project bool
		key     string
		want    bool
	}{
		{false, "verbose", true},
		{false, "vim", false},
		{false, "router.explore", true},
		{false, "router.code", false},
		{true, "reasoning_by_provider.anthropic.effort", true},
		{true, "reasoning_by_provider.anthropic.budget", false},
		{true, "verbose", false},
	}
	for _, c := range cases {
		if got := savedIn(root, c.project, c.key); got != c.want {
			t.Errorf("savedIn(project=%v, %q) = %v, want %v", c.project, c.key, got, c.want)
		}
	}
	if savedIn("", true, "verbose") {
		t.Error("with no workdir there is no project file to hold a key")
	}
}

func TestMoveScopeMovesOnlyKeysInTheOldFile(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	writeConfigFile(t, config.GlobalPath(), `{"verbose": true}`)
	b := &moveBackend{FakeBackend: NewFakeBackend(), root: t.TempDir()}
	s := &settings{m: m, be: b, tab: stGeneral, styles: map[string]modelStyle{}, loading: map[string]bool{}}

	s.moveScope(true)
	if !s.project {
		t.Fatal("the scope should now be project")
	}
	if len(b.moves) != 1 || b.moves[0].Key != "verbose" || !b.moves[0].Project {
		t.Fatalf("only verbose is saved in global, so only it should move: %+v", b.moves)
	}
	if !strings.Contains(s.note, "moved 1 to project") {
		t.Fatalf("the result should be reported, got %q", s.note)
	}

	// Already in the project scope: pressing right again does nothing.
	s.moveScope(true)
	if len(b.moves) != 1 {
		t.Fatalf("a second right should not move again: %+v", b.moves)
	}

	// Back to global: the project file holds nothing on this tab.
	s.moveScope(false)
	if s.project || len(b.moves) != 1 {
		t.Fatalf("moving to global with nothing to move should only switch scope: project=%v moves=%+v", s.project, b.moves)
	}
	if !strings.Contains(s.note, "Nothing on this tab to move") {
		t.Fatalf("an empty move should say so, got %q", s.note)
	}
}

func TestMoveScopeWithoutMoverSaysSo(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	t.Setenv("SCRIBE_HOME", t.TempDir())
	writeConfigFile(t, config.GlobalPath(), `{"verbose": true}`)
	// noMover hides MoveConfig: only the Backend methods are promoted.
	type noMover struct{ Backend }
	s := &settings{m: m, be: noMover{fb}, tab: stGeneral, styles: map[string]modelStyle{}, loading: map[string]bool{}}

	s.moveScope(true)
	if !strings.Contains(s.note, "cannot move settings") {
		t.Fatalf("a backend without MoveConfig should report it, got %q", s.note)
	}
	if len(fb.ConfigWrites()) != 0 {
		t.Fatalf("nothing should be written when the move is refused: %+v", fb.ConfigWrites())
	}
}

func TestMoveScopeArrowKeysChangeScope(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	t.Setenv("SCRIBE_HOME", t.TempDir())
	b := &moveBackend{FakeBackend: NewFakeBackend(), root: t.TempDir()}
	s := &settings{m: m, be: b, styles: map[string]modelStyle{}, loading: map[string]bool{}}

	s.Update(tea.KeyMsg{Type: tea.KeyRight})
	if !s.project {
		t.Fatal("right should move the scope to project")
	}
	s.Update(tea.KeyMsg{Type: tea.KeyLeft})
	if s.project {
		t.Fatal("left should move the scope back to global")
	}
}

func TestToggleRowShowsItsSwitch(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	s := &settings{m: m, be: NewFakeBackend()}
	cols := rowCols{label: 20, value: 12}
	on := ansi.Strip(s.rowLine(setRow{label: "Verbose", value: "on", toggle: true, on: true}, false, cols))
	off := ansi.Strip(s.rowLine(setRow{label: "Verbose", value: "off", toggle: true}, false, cols))
	if !strings.Contains(on, "[●━] ON") {
		t.Fatalf("an on toggle should draw its switch: %q", on)
	}
	if !strings.Contains(off, "[━●] OFF") {
		t.Fatalf("an off toggle should draw its switch: %q", off)
	}
}

func TestWizardIconsStepTogglesNerdFont(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	t.Cleanup(func() { icons.SetNerd(false) })
	w := newWizard(m)
	w.step = wzIcons

	w.onKey(tea.KeyMsg{Type: tea.KeySpace})
	writes := fb.ConfigWrites()
	if len(writes) == 0 || writes[len(writes)-1].Key != "nerd_font" || writes[len(writes)-1].Value != true {
		t.Fatalf("space should save nerd_font on: %+v", writes)
	}
	if !icons.NerdEnabled() {
		t.Fatal("the glyphs should switch at once")
	}
	if got := wizSections[wzIcons.section()].label; got != "setup" {
		t.Fatalf("the icons step belongs to the setup entry of the sidebar, got %q", got)
	}

	w.onKey(tea.KeyMsg{Type: tea.KeyEnter})
	if w.step != wzIntel {
		t.Fatalf("enter should move on to the code intelligence step, got %v", w.step)
	}
	w.step = wzIcons
	w.onKey(tea.KeyMsg{Type: tea.KeyLeft})
	if w.step != wzModel {
		t.Fatalf("left should go back to the model step, got %v", w.step)
	}
}

func TestWizardKeyFieldShowsLastFourCharacters(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	w := newWizard(m)
	w.step = wzKey
	w.chosen = ProviderStatus{ID: "openai", Name: "OpenAI"}
	w.key = []rune("sk-live-abcdefgh1234")
	out := ansi.Strip(strings.Join(w.keyView(m.th, 60), "\n"))
	if !strings.Contains(out, "1234") || strings.Contains(out, "sk-live") {
		t.Fatalf("the key field should show the last four characters only:\n%s", out)
	}
}

func TestChooseModelMovesToIcons(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	w := newWizard(m)
	w.step = wzModel
	w.chooseModel("anthropic/claude-test")
	if w.step != wzIcons {
		t.Fatalf("after the model the wizard should show icons, got %v", w.step)
	}
}

func TestMoveScopeCarriesPermissionLists(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	t.Setenv("SCRIBE_HOME", t.TempDir())
	writeConfigFile(t, config.GlobalPath(), `{"allow": ["go test"]}`)
	b := &moveBackend{FakeBackend: NewFakeBackend(), root: t.TempDir()}
	s := &settings{m: m, be: b, tab: stPermissions, styles: map[string]modelStyle{}, loading: map[string]bool{}}

	s.moveScope(true)
	found := false
	for _, mv := range b.moves {
		if mv.Key == "allow" && mv.Project {
			found = true
		}
		if mv.Key == "deny" {
			t.Fatalf("deny is not saved anywhere, so it must not move: %+v", b.moves)
		}
	}
	if !found {
		t.Fatalf("the allow list is saved in global and should move: %+v", b.moves)
	}
}
