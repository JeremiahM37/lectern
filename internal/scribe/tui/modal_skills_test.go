package tui

import (
	"slices"
	"strings"
	"testing"
)

func TestSkillsCtrlALoadsAndUnloadsSkill(t *testing.T) {
	b := newRecBackend()
	m := modalModel(t, 140, 36, b)
	send(m, "ctrl+s")
	// The first row is db-migrations (project scope sorts first).
	runCmd(m, send(m, "ctrl+a"))
	if !slices.Contains(b.ActiveSkills(), "db-migrations") {
		t.Fatalf("ctrl+a should load db-migrations, active=%v", b.ActiveSkills())
	}
	if !strings.Contains(m.flash, "loaded into AI context") {
		t.Fatalf("flash = %q", m.flash)
	}

	runCmd(m, send(m, "ctrl+a"))
	if slices.Contains(b.ActiveSkills(), "db-migrations") {
		t.Fatalf("second ctrl+a should unload it, active=%v", b.ActiveSkills())
	}
}

func TestSkillsPreviewShowsContextState(t *testing.T) {
	b := newRecBackend()
	if err := b.SetSkillActive("release-notes", true); err != nil {
		t.Fatal(err)
	}
	m := modalModel(t, 140, 36, b)
	send(m, "ctrl+s")
	send(m, "down") // release-notes, the second project skill
	mustContain(t, screen(m), "context loaded into the AI context")
}

func TestSkillsInstallFromPathAsksScopeThenInstalls(t *testing.T) {
	b := newRecBackend()
	m := modalModel(t, 140, 36, b)
	send(m, "ctrl+s")
	send(m, "ctrl+n")
	if topTitle(m) != "Install skill" {
		t.Fatalf("ctrl+n should ask for a source, top=%q", topTitle(m))
	}
	typeText(m, "https://example.invalid/acme/review-skill.git")
	send(m, "enter")
	if topTitle(m) != "Install skill to" {
		t.Fatalf("a source should lead to the scope choice, top=%q", topTitle(m))
	}
	// Choose "This project" (the first row). The install runs in the
	// background and its result is delivered to the browser.
	cmd := send(m, "enter")
	if cmd == nil {
		t.Fatal("installing should return a background command")
	}
	runCmd(m, cmd)

	if topTitle(m) != "Skills" {
		t.Fatalf("the browser should be the top modal again, top=%q", topTitle(m))
	}
	if len(b.installed) != 1 || b.installed[0] != "review-skill" {
		t.Fatalf("installed = %v", b.installed)
	}
}

func TestSkillsInstallRefusesEmptySource(t *testing.T) {
	m := modalModel(t, 140, 36, newRecBackend())
	send(m, "ctrl+s")
	send(m, "ctrl+n")
	send(m, "enter")
	if topTitle(m) != "Install skill" {
		t.Fatalf("an empty source must keep the prompt open, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "Nothing entered.")
}
