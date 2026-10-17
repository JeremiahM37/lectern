package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// gitBackend answers MissingRequired from the real PATH lookup, the same check
// the app uses, so these tests exercise PATH rather than a fake list.
type gitBackend struct{ *FakeBackend }

func (gitBackend) MissingRequired() []string { return RequiredMissing() }

func gitModel(t *testing.T) *Model {
	t.Helper()
	fb := NewFakeBackend()
	fb.Delay = 0
	m := NewModel(gitBackend{fb}, func() (Backend, error) {
		n := NewFakeBackend()
		n.Delay = 0
		return n, nil
	}, Options{Width: 100, Height: 40, Theme: "scribe"})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 40})
	return m
}

// emptyPath points PATH at a directory with no programs in it.
func emptyPath(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// gitOnPath puts a stand-in git on PATH, so the lookup finds it.
func gitOnPath(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "git"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestGitGateShownWithoutGit(t *testing.T) {
	emptyPath(t)
	m := gitModel(t)
	m.maybeOpenWizard()
	if _, ok := m.topModal().(*gitGate); !ok {
		t.Fatalf("without git the setup screen should open, top modal %T", m.topModal())
	}
	out := screen(m)
	for _, want := range []string{"Git is required", "install:", "r check again", "q quit"} {
		if !strings.Contains(out, want) {
			t.Fatalf("gate should contain %q:\n%s", want, out)
		}
	}
	if _, ok := m.topModal().(*wizard); ok {
		t.Fatal("the first-run wizard must not open over the gate")
	}
}

func TestGitGateSurvivesEscAndCtrlC(t *testing.T) {
	emptyPath(t)
	m := gitModel(t)
	m.maybeOpenWizard()
	send(m, "esc")
	if _, ok := m.topModal().(*gitGate); !ok {
		t.Fatalf("esc must not get past the gate, top modal %T", m.topModal())
	}
	// Ctrl+C closes the top modal first. The gate must come straight back.
	send(m, "ctrl+c")
	if _, ok := m.topModal().(*gitGate); !ok {
		t.Fatalf("one ctrl+c must not get past the gate, top modal %T", m.topModal())
	}
	if m.phase == phaseShutdown {
		t.Fatal("one ctrl+c should not quit")
	}
}

func TestGitGateRecheckOpensChat(t *testing.T) {
	emptyPath(t)
	m := gitModel(t)
	m.maybeOpenWizard()

	// Git is installed while the screen is up; r checks again.
	gitOnPath(t)
	send(m, "r")
	if m.topModal() != nil {
		t.Fatalf("once git is found the gate should close, top modal %T", m.topModal())
	}
}

func TestGitGateRecheckStaysWhileMissing(t *testing.T) {
	emptyPath(t)
	m := gitModel(t)
	m.maybeOpenWizard()
	send(m, "r")
	if _, ok := m.topModal().(*gitGate); !ok {
		t.Fatalf("r without git should keep the gate, top modal %T", m.topModal())
	}
}

func TestGitGateQuitKey(t *testing.T) {
	emptyPath(t)
	m := gitModel(t)
	m.maybeOpenWizard()
	send(m, "q")
	if m.phase != phaseShutdown {
		t.Fatalf("q should start shutdown, phase %v", m.phase)
	}
	if _, ok := m.topModal().(*gitGate); ok {
		t.Fatal("quitting must not put the gate back")
	}
}

func TestGitGateNotShownWithGit(t *testing.T) {
	gitOnPath(t)
	m := gitModel(t)
	m.maybeOpenWizard()
	if m.topModal() != nil {
		t.Fatalf("with git present nothing should open, top modal %T", m.topModal())
	}
}

func TestGitGateBeatsPrompt(t *testing.T) {
	emptyPath(t)
	m := gitModel(t)
	m.opts.Prompt = "fix the tests"
	m.maybeOpenWizard()
	if _, ok := m.topModal().(*gitGate); !ok {
		t.Fatalf("a prompt must not skip the git gate, top modal %T", m.topModal())
	}
}

func TestGitGateOnWizardOpen(t *testing.T) {
	emptyPath(t)
	m := gitModel(t)
	m.openWizard()
	if _, ok := m.topModal().(*gitGate); !ok {
		t.Fatalf("openWizard without git should push the gate, top modal %T", m.topModal())
	}
}

func TestRequiredMissingNamesGitOnly(t *testing.T) {
	emptyPath(t)
	got := RequiredMissing()
	if len(got) != 1 || got[0] != "Git" {
		t.Fatalf("RequiredMissing with no PATH = %v, want [Git]", got)
	}
	gitOnPath(t)
	if got := RequiredMissing(); len(got) != 0 {
		t.Fatalf("RequiredMissing with git = %v, want none", got)
	}
}
