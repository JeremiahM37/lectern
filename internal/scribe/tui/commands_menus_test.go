package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// runFlash runs a command and feeds its message back, so the flash it set is
// on the model. It returns the flash text.
func runFlash(t *testing.T, m *Model, cmd tea.Cmd) string {
	t.Helper()
	if cmd == nil {
		t.Fatal("expected a command")
	}
	m.Update(cmd())
	return m.flash
}

func TestClearAsksBeforeClearingTheView(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	m.runCommand("/clear")
	if _, ok := m.topModal().(*Confirm); !ok {
		t.Fatalf("/clear should ask first, top modal is %T", m.topModal())
	}
}

func TestContextClearUnloadsSkills(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	cmd := m.runCommand("/context clear skills")
	if got := runFlash(t, m, cmd); !strings.Contains(got, "Skills unloaded") {
		t.Fatalf("flash = %q", got)
	}
	if ops := fb.Ops(); len(ops) != 1 || ops[0] != "clear-context skills" {
		t.Fatalf("ops = %v", ops)
	}
}

func TestContextRejectsUnknownSubcommand(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	m.contextCommand("wipe")
	if len(fb.Ops()) != 0 {
		t.Fatalf("an unknown subcommand must not touch the backend: %v", fb.Ops())
	}
}

func TestParseCommitArgs(t *testing.T) {
	cases := []struct {
		in, msg string
		staged  bool
	}{
		{"fix parser", "fix parser", false},
		{"--staged fix parser", "fix parser", true},
		{"  --staged   spaced  ", "spaced", true},
		{"--staged", "", true},
		{"--stagedish", "--stagedish", false},
	}
	for _, c := range cases {
		msg, staged := parseCommitArgs(c.in)
		if msg != c.msg || staged != c.staged {
			t.Errorf("parseCommitArgs(%q) = %q, %v; want %q, %v", c.in, msg, staged, c.msg, c.staged)
		}
	}
}

func TestCommitStagedCommitsOnlyWhatIsStaged(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	runFlash(t, m, m.runCommand("/git commit --staged parser: trim"))
	if ops := fb.Ops(); len(ops) != 1 || ops[0] != "commit all=false parser: trim" {
		t.Fatalf("ops = %v", ops)
	}
}

func TestCommitStagedNeedsAMessage(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	if got := runFlash(t, m, m.commitCommand("--staged")); !strings.Contains(got, "Usage") {
		t.Fatalf("flash = %q, want usage", got)
	}
	if len(fb.Ops()) != 0 {
		t.Fatalf("nothing should be committed: %v", fb.Ops())
	}
}

func TestGitInitBranchAndPullUseTheBackend(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	for _, line := range []string{"/git init", "/git branch feature/x", "/git pull"} {
		runFlash(t, m, m.runCommand(line))
	}
	ops := fb.Ops()
	want := []string{"git-init", "git-branch feature/x", "git-pull"}
	for _, w := range want {
		found := false
		for _, o := range ops {
			found = found || o == w
		}
		if !found {
			t.Fatalf("ops %v missing %q", ops, w)
		}
	}
}

func TestScopeMovesOneSettingToProject(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	m.runCommand("/scope verbose project")
	ops := fb.Ops()
	if len(ops) != 1 || ops[0] != "move verbose project" {
		t.Fatalf("ops = %v", ops)
	}
	m.runCommand("/scope verbose global")
	if got := fb.Ops(); got[len(got)-1] != "move verbose global" {
		t.Fatalf("ops after global move = %v", got)
	}
}

func TestScopeRejectsUnknownSettingAndBadScope(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	if got := runFlash(t, m, m.runCommand("/scope nosuchsetting project")); !strings.Contains(got, "No setting") {
		t.Fatalf("flash = %q", got)
	}
	m.runCommand("/scope verbose sideways")
	if len(fb.Ops()) != 0 {
		t.Fatalf("a bad request must not write: %v", fb.Ops())
	}
}

func TestModelScopeMovesTheModel(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	m.runCommand("/model-scope project")
	if ops := fb.Ops(); len(ops) != 1 || !strings.HasPrefix(ops[0], "move model project") {
		t.Fatalf("ops = %v", ops)
	}
}

func TestModelScopeWithoutArgumentAsksWhere(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	m.runCommand("/model-scope")
	if _, ok := m.topModal().(*Picker); !ok {
		t.Fatalf("top modal is %T, want the scope picker", m.topModal())
	}
	if len(fb.Ops()) != 0 {
		t.Fatalf("opening the picker must not write: %v", fb.Ops())
	}
}

func TestExportAllWritesTheDiagnosticDump(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	got := runFlash(t, m, m.runCommand("/export all"))
	if !strings.Contains(got, "scribe-diagnostic.json") {
		t.Fatalf("flash = %q, want the diagnostic path", got)
	}
}

func TestExportClipboardUsesMarkdownWithoutAFile(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	m.runCommand("/export clipboard")
	ops := fb.Ops()
	if len(ops) != 1 || ops[0] != "export-markdown" {
		t.Fatalf("ops = %v, want the markdown export", ops)
	}
}

func TestPlanWithoutArgumentToggles(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	m.runCommand("/plan")
	if fb.Mode() != "plan" {
		t.Fatalf("mode = %q after first /plan", fb.Mode())
	}
	m.runCommand("/plan")
	if fb.Mode() != "default" {
		t.Fatalf("mode = %q after second /plan, want default", fb.Mode())
	}
}

func TestPlanWithTaskStartsPlanning(t *testing.T) {
	m, fb := newTestModel(t, 100, 40)
	m.runCommand("/plan add a cache to lookups")
	if fb.Mode() != "plan" {
		t.Fatalf("mode = %q, want plan", fb.Mode())
	}
	if !m.cur().busy {
		t.Fatal("the task should start a turn")
	}
}

func TestCommandsNeedingCapabilitiesSayWhenMissing(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	// A backend that lacks the optional interfaces: the plain FakeBackend has
	// them, so wrap it in a struct that embeds only the required Backend.
	m.tabs[0].be = struct{ Backend }{m.tabs[0].be}
	if got := runFlash(t, m, m.runCommand("/scope verbose project")); !strings.Contains(got, "cannot move") {
		t.Fatalf("flash = %q", got)
	}
}
