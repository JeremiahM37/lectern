package tui

import (
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// ctxFake is a fake backend that also keeps a context-window override and
// counts task clears.
type ctxFake struct {
	*FakeBackend
	ref      string
	window   int
	override int
	cleared  int
	saved    []string
}

func (f *ctxFake) ContextWindow() (string, int, int) { return f.ref, f.window, f.override }
func (f *ctxFake) SetContextWindow(n int) error {
	if n < 0 {
		return errors.New("negative")
	}
	f.override = n
	if n > 0 {
		f.window = n
	}
	return nil
}
func (f *ctxFake) ClearTasks() error { f.cleared++; return nil }
func (f *ctxFake) SaveCheckpoint(name string) (string, error) {
	f.saved = append(f.saved, name)
	return name + " (abc123)", nil
}

func TestParseTokenCountTakesHumanForms(t *testing.T) {
	for in, want := range map[string]int{"64000": 64000, "64,000": 64000, "64k": 64000, "128K": 128000, "1.5m": 1500000, "0": 0} {
		got, err := parseTokenCount(in)
		if err != nil || got != want {
			t.Errorf("parseTokenCount(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "lots", "-4k"} {
		if _, err := parseTokenCount(bad); err == nil {
			t.Errorf("parseTokenCount(%q) should fail", bad)
		}
	}
}

func TestCtxwinShowsAndSetsTheOverride(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	f := &ctxFake{FakeBackend: NewFakeBackend(), ref: "fake/m", window: 128000}
	m.tabs[0].be = f
	feed(m, m.runCommand("/ctxwin"))
	feed(m, m.runCommand("/context-window 64k"))
	if f.override != 64000 {
		t.Fatalf("override = %d, want 64000", f.override)
	}
	if !strings.Contains(m.flash, "set to 64000 tokens") {
		t.Fatalf("flash = %q", m.flash)
	}
	feed(m, m.runCommand("/ctxwin 0"))
	if f.override != 0 || !strings.Contains(m.flash, "Removed") {
		t.Fatalf("after 0: override=%d flash=%q", f.override, m.flash)
	}
	feed(m, m.runCommand("/ctxwin lots"))
	if !strings.Contains(m.flash, "Usage: /ctxwin") {
		t.Fatalf("bad input flash = %q", m.flash)
	}
}

func TestCtxwinSaysWhenTheSessionCannotSetIt(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.tabs[0].be = NewFakeBackend()
	feed(m, m.runCommand("/ctxwin 8k"))
	if !strings.Contains(m.flash, "cannot") {
		t.Fatalf("flash = %q", m.flash)
	}
}

// /session clear empties the view, the steering queue and the task list,
// after a confirm.
func TestSessionClearAlsoClearsTasks(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	f := &ctxFake{FakeBackend: NewFakeBackend()}
	m.tabs[0].be = f
	m.tabs[0].steerQ = []string{"queued"}
	feed(m, m.runCommand("/session clear"))
	if f.cleared != 0 {
		t.Fatal("tasks cleared before the confirm")
	}
	if len(m.modals) == 0 {
		t.Fatal("/session clear should ask first")
	}
	confirmYes(m)
	if f.cleared != 1 {
		t.Fatalf("tasks cleared %d times, want 1", f.cleared)
	}
	if len(m.tabs[0].steerQ) != 0 {
		t.Fatal("the steering queue should be cleared too")
	}
}

func TestClearAloneLeavesTasks(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	f := &ctxFake{FakeBackend: NewFakeBackend()}
	m.tabs[0].be = f
	feed(m, m.runCommand("/clear"))
	confirmYes(m)
	if f.cleared != 0 {
		t.Fatalf("/clear cleared tasks (%d)", f.cleared)
	}
}

func TestCheckpointSaveNamesTheCheckpoint(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	f := &ctxFake{FakeBackend: NewFakeBackend()}
	m.tabs[0].be = f
	feed(m, m.runCommand("/checkpoint save before-port"))
	if len(f.saved) != 1 || f.saved[0] != "before-port" {
		t.Fatalf("saved = %v", f.saved)
	}
	if !strings.Contains(m.flash, "Saved checkpoint before-port") {
		t.Fatalf("flash = %q", m.flash)
	}
	feed(m, m.runCommand("/checkpoint save"))
	if len(f.saved) != 2 || f.saved[1] != "" {
		t.Fatalf("unnamed save: %v", f.saved)
	}
}

func TestUnclaimAndForceClaimAreAliases(t *testing.T) {
	for _, name := range []string{"unclaim", "force-claim"} {
		if c := findCommandNamed(name); c == nil {
			t.Fatalf("/%s is not registered", name)
		}
	}
	m, _ := newTestModel(t, 100, 30)
	m.tabs[0].be = NewFakeBackend()
	feed(m, m.runCommand("/unclaim"))
	if m.flash == "" {
		t.Fatal("/unclaim with no path should say what to type")
	}
}

// confirmYes answers yes to the confirm on top.
func confirmYes(m *Model) {
	_, cmd, _ := m.modals[len(m.modals)-1].Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	feed(m, cmd)
}

func findCommandNamed(name string) *command {
	for _, c := range allCommands() {
		if c.Name == name {
			return c
		}
	}
	return nil
}
