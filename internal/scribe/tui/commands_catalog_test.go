package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/icons"
)

// flashOf runs a command that should return a flash message and returns it.
func flashOf(t *testing.T, cmd tea.Cmd) flashMsg {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command that reports a flash, got nil")
	}
	fm, ok := cmd().(flashMsg)
	if !ok {
		t.Fatalf("command did not produce a flashMsg")
	}
	return fm
}

func TestCommandNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, c := range commandTable {
		if seen[c.Name] {
			t.Errorf("command /%s is registered more than once", c.Name)
		}
		seen[c.Name] = true
	}
}

func TestCatalogNamesResolve(t *testing.T) {
	for line, want := range map[string]string{
		"/mode":                "mode",
		"/mode plan":           "mode",
		"/checkpoint undo":     "checkpoint",
		"/font nerd":           "font",
		"/providers":           "provider-settings",
		"/provider":            "provider-settings",
		"/api-keys":            "keys",
		"/repo-map rebuild":    "repo-map",
		"/lsp restart gopls":   "lsp restart",
		"/lsp status":          "lsp status",
		"/lsp-restart":         "lsp-restart",
		"/editor open main.go": "editor",
		"/terminals hide":      "terminals",
		"/split":               "split",
	} {
		c, _ := findCommand(line)
		if c == nil || c.Name != want {
			got := "<nil>"
			if c != nil {
				got = c.Name
			}
			t.Errorf("%q resolved to %s, want %s", line, got, want)
		}
	}
}

func TestModeCommandSwitchesAndRejects(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	m.runCommand("/mode plan")
	if fb.Mode() != "plan" {
		t.Fatalf("mode = %q, want plan", fb.Mode())
	}
	fm := flashOf(t, m.runCommand("/mode nonsense"))
	if fm.kind != "err" || !strings.Contains(fm.text, "Unknown mode nonsense") {
		t.Fatalf("bad mode gave %+v", fm)
	}
	if fb.Mode() != "plan" {
		t.Fatalf("a rejected mode must not change the mode, got %q", fb.Mode())
	}
}

func TestStatusPanelOpens(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.runCommand("/status")
	if len(m.modals) == 0 || m.modals[len(m.modals)-1].Title() != "Status" {
		t.Fatal("/status should open the Status panel")
	}
	joined := strings.Join(m.statusLines(), "\n")
	for _, want := range []string{"Model", "Mode", "Context", "Git", "Servers"} {
		if !strings.Contains(joined, want) {
			t.Errorf("status panel is missing %q", want)
		}
	}
}

func TestDiagnoseReportsMapServersAndKeys(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	out := strings.Join(m.diagnoseLines(), "\n")
	for _, want := range []string{"Code map", "Servers", "Provider keys"} {
		if !strings.Contains(out, want) {
			t.Errorf("diagnose output is missing the %q section", want)
		}
	}
}

func TestChangesListsOnlyLiveTurnFiles(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	fb.checkpoints = []CheckpointInfo{
		{Turn: 1, Files: []string{"b.go", "a.go"}},
		{Turn: 2, Files: []string{"a.go"}},
		{Turn: 3, Files: []string{"undone.go"}, Undone: true},
	}
	out := strings.Join(m.changesLines(), "\n")
	for _, want := range []string{"  a.go", "  b.go"} {
		if !strings.Contains(out, want) {
			t.Errorf("changes should list %q", want)
		}
	}
	if strings.Contains(out, "undone.go") {
		t.Error("a file named only by an undone turn must not be listed")
	}
	if strings.Count(out, "  a.go") != 1 {
		t.Error("a file changed by two turns should be listed once")
	}
}

func TestCheckpointSubcommandRouting(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	fm := flashOf(t, m.runCommand("/checkpoint bogus"))
	if fm.kind != "err" {
		t.Fatalf("unknown checkpoint subcommand gave %+v", fm)
	}
	m.runCommand("/checkpoint list")
	if len(m.modals) == 0 {
		t.Fatal("/checkpoint list should open the checkpoint browser")
	}
}

func TestFontNerdTogglesConfig(t *testing.T) {
	t.Cleanup(func() { icons.SetNerd(false) })
	m, fb := newTestModel(t, 100, 30)
	m.runCommand("/font nerd")
	on, _ := fb.Config()["nerd_font"].(bool)
	if !on {
		t.Fatal("/font nerd should turn Nerd Font icons on")
	}
	m.runCommand("/font nerd")
	off, _ := fb.Config()["nerd_font"].(bool)
	if off {
		t.Fatal("a second /font nerd should turn them off again")
	}
}

func TestSettingsShortcutsOpenTheRightTab(t *testing.T) {
	for line, tab := range map[string]int{"/provider-settings": stProviders, "/router": stRouter} {
		m, _ := newTestModel(t, 100, 30)
		m.runCommand(line)
		s, ok := m.modals[len(m.modals)-1].(*settings)
		if !ok {
			t.Fatalf("%s should open the settings screen", line)
		}
		if s.tab != tab {
			t.Errorf("%s opened tab %d, want %d", line, s.tab, tab)
		}
	}
}

func TestLSPRestartWithNoMatchSaysSo(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	fm := flashOf(t, m.runCommand("/lsp restart zzz-no-such-server"))
	if fm.kind != "info" || !strings.Contains(fm.text, "No language servers match") {
		t.Fatalf("filter with no match gave %+v", fm)
	}
}

func TestPanickingHandlerIsReportedNotFatal(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	overrideCommands(&command{Name: "zz-panic-test", Group: "System",
		Run: func(m *Model, _ string) tea.Cmd { panic("boom") }})
	t.Cleanup(func() {
		for i, c := range commandTable {
			if c.Name == "zz-panic-test" {
				commandTable = append(commandTable[:i], commandTable[i+1:]...)
				break
			}
		}
	})
	if cmd := m.runCommand("/zz-panic-test"); cmd != nil {
		t.Fatal("a panicking command should return no follow-up command")
	}
}
