package tui

import (
	"errors"
	"strings"
	"testing"
)

// fakeMachine makes lookPath find only the named tools for the test.
func fakeMachine(t *testing.T, have ...string) {
	t.Helper()
	old := lookPath
	lookPath = func(name string) (string, error) {
		for _, h := range have {
			if h == name {
				return "/usr/bin/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() { lookPath = old })
}

func TestWizardBlocksOnMissingGit(t *testing.T) {
	fakeMachine(t, "rg")
	m, _, wz := wizardOpen(t, 100, 40)
	drain(m, wz.start())

	send(m, "enter")
	if wz.step != wzWelcome {
		t.Fatalf("wizard advanced without git, step %v", wz.step)
	}
	if !strings.Contains(screen(m), "Git is required") {
		t.Fatalf("missing git should say so:\n%s", screen(m))
	}
	if !strings.Contains(screen(m), "missing") || !strings.Contains(screen(m), "install:") {
		t.Fatalf("welcome should list git as missing with an install command:\n%s", screen(m))
	}
}

func TestWizardRechecksAfterInstall(t *testing.T) {
	fakeMachine(t)
	m, _, wz := wizardOpen(t, 100, 40)
	drain(m, wz.start())
	send(m, "enter")
	if wz.step != wzWelcome {
		t.Fatalf("advanced without git")
	}

	// Git is installed while the wizard is open; r checks again.
	fakeMachine(t, "git")
	send(m, "r")
	send(m, "enter")
	if wz.step != wzProvider {
		t.Fatalf("wizard should advance once git is found, step %v", wz.step)
	}
}

func TestOptionalToolsDoNotBlock(t *testing.T) {
	fakeMachine(t, "git")
	m, _, wz := wizardOpen(t, 100, 40)
	drain(m, wz.start())
	if !strings.Contains(screen(m), "ripgrep") || !strings.Contains(screen(m), "optional") {
		t.Fatalf("optional tools should be listed as optional:\n%s", screen(m))
	}
	send(m, "enter")
	if wz.step != wzProvider {
		t.Fatalf("missing optional tools must not block, step %v", wz.step)
	}
}

func TestInstallHintPerPlatform(t *testing.T) {
	git := prereqTable[0]
	if got := installHint(git, "linux"); got != "sudo apt install git" {
		t.Errorf("linux hint = %q", got)
	}
	if got := installHint(git, "windows"); !strings.Contains(got, "winget") {
		t.Errorf("windows hint = %q", got)
	}
	if got := installHint(git, "plan9"); got != "install it with your package manager" {
		t.Errorf("unknown platform hint = %q", got)
	}
}

func TestCheckPrereqsReportsPaths(t *testing.T) {
	fakeMachine(t, "git", "fd")
	got := checkPrereqs()
	if len(got) != len(prereqTable) {
		t.Fatalf("got %d rows, want %d", len(got), len(prereqTable))
	}
	if got[0].path == "" || got[1].path != "" || got[2].path == "" {
		t.Fatalf("paths wrong: %+v", got)
	}
	if miss := missingRequired(got); len(miss) != 0 {
		t.Fatalf("git is present but reported missing: %+v", miss)
	}
}
