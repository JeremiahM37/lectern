package tui

import (
	"slices"
	"strings"
	"testing"
)

// skillsTabs opens the browser on the given tab by pressing tab until the
// strip shows it. The browser opens on Search when the backend has a registry.
func skillsTabs(m *Model, want string) {
	for i := 0; i < 3; i++ {
		if strings.Contains(screen(m), "["+want+"]") {
			return
		}
		send(m, "tab")
	}
}

// removeRecBackend records RemoveSkill calls on top of the fake catalogue.
type removeRecBackend struct {
	*recBackend
	removed []string
}

func (r *removeRecBackend) RemoveSkill(name string) error {
	r.removed = append(r.removed, name)
	return nil
}

// skillNoOptBackend hides every optional skill interface: no catalogue, no
// registry, no removal.
type skillNoOptBackend struct{ Backend }

func TestSkillsEnterLoadsOnInstalledAndUnloadsOnActive(t *testing.T) {
	b := newRecBackend()
	m := modalModel(t, 140, 36, b)
	gitKey(m, "ctrl+s")
	skillsTabs(m, "Installed")
	// The first row is db-migrations (project scope sorts first).
	runCmd(m, send(m, "enter"))
	if !slices.Contains(b.ActiveSkills(), "db-migrations") {
		t.Fatalf("enter should load db-migrations, active=%v", b.ActiveSkills())
	}
	if !strings.Contains(m.flash, "loaded into AI context") {
		t.Fatalf("flash = %q", m.flash)
	}

	skillsTabs(m, "Active")
	mustContain(t, screen(m), "db-migrations")
	gitKey(m, "enter")
	if slices.Contains(b.ActiveSkills(), "db-migrations") {
		t.Fatalf("enter on Active should unload it, active=%v", b.ActiveSkills())
	}
	if !strings.Contains(m.flash, "unloaded from AI context") {
		t.Fatalf("flash = %q", m.flash)
	}
}

func TestSkillsTabsCycleAndClearTheQuery(t *testing.T) {
	m := modalModel(t, 140, 36, newRecBackend())
	gitKey(m, "ctrl+s")
	mustContain(t, screen(m), "[Search]", "Installed", "Active")
	typeText(m, "rel")
	send(m, "tab")
	mustContain(t, screen(m), "[Installed]")
	if strings.Contains(screen(m), "rel▌") {
		t.Fatalf("switching tabs should clear the query:\n%s", screen(m))
	}
	send(m, "shift+tab")
	mustContain(t, screen(m), "[Search]")
}

func TestSkillsPopularListOnSearchTab(t *testing.T) {
	m := modalModel(t, 140, 36, newRecBackend())
	gitKey(m, "ctrl+s")
	mustContain(t, screen(m), "acme/review-skill", "1,234↓", "infra/k8s-debug")
}

func TestSkillsSearchWaitsThenShowsMatches(t *testing.T) {
	m := modalModel(t, 140, 36, newRecBackend())
	gitKey(m, "ctrl+s")
	typeText(m, "chan")
	cmd := send(m, "g") // the last key starts the pause before a search
	if cmd == nil {
		t.Fatal("typing in Search should schedule a registry search")
	}
	gitPump(m, cmd)
	mustContain(t, screen(m), "acme/changelog")
	if strings.Contains(screen(m), "acme/review-skill") {
		t.Fatalf("results should be filtered by the query:\n%s", screen(m))
	}
}

func TestSkillsStaleSearchIsDropped(t *testing.T) {
	m := modalModel(t, 140, 36, newRecBackend())
	gitKey(m, "ctrl+s")
	typeText(m, "chan")
	old := skillSearchDue{seq: 1, q: "chan"}
	typeText(m, "g") // a newer keystroke makes the old search stale
	if _, cmd := m.Update(old); cmd != nil {
		t.Fatal("a stale search must not run")
	}
}

func TestSkillsInstallFromRegistryAsksScope(t *testing.T) {
	b := newRecBackend()
	m := modalModel(t, 140, 36, b)
	gitKey(m, "ctrl+s")
	send(m, "enter") // first popular row: acme/review-skill
	if topTitle(m) != "Install skill to" {
		t.Fatalf("enter on Search should ask for a scope, top=%q", topTitle(m))
	}
	runCmd(m, send(m, "enter"))
	if len(b.installed) != 1 || b.installed[0] != "review-skill" {
		t.Fatalf("installed = %v", b.installed)
	}
	if topTitle(m) != "Skills" {
		t.Fatalf("the browser should be the top modal again, top=%q", topTitle(m))
	}
}

func TestSkillsRemoveAsksThenDeletes(t *testing.T) {
	b := &removeRecBackend{recBackend: newRecBackend()}
	m := modalModel(t, 140, 36, b)
	gitKey(m, "ctrl+s")
	skillsTabs(m, "Installed")
	gitKey(m, "enter") // load it first: removal must unload it
	if !slices.Contains(b.ActiveSkills(), "db-migrations") {
		t.Fatalf("setup: db-migrations should be loaded")
	}
	gitKey(m, "ctrl+d")
	if topTitle(m) != "Remove skill" {
		t.Fatalf("ctrl+d must open a confirm, top=%q", topTitle(m))
	}
	if len(b.removed) != 0 {
		t.Fatalf("removed before the confirm: %v", b.removed)
	}
	gitKey(m, "esc") // cancel: nothing is removed
	if len(b.removed) != 0 || topTitle(m) != "Skills" {
		t.Fatalf("cancel removed %v, top=%q", b.removed, topTitle(m))
	}
	gitKey(m, "ctrl+d")
	gitKey(m, "enter") // the confirm's default is Cancel, so enter removes nothing
	if len(b.removed) != 0 {
		t.Fatalf("default choice removed the skill: %v", b.removed)
	}
	gitKey(m, "ctrl+d")
	gitKey(m, "y")
	if !slices.Equal(b.removed, []string{"db-migrations"}) {
		t.Fatalf("removed = %v", b.removed)
	}
	if slices.Contains(b.ActiveSkills(), "db-migrations") {
		t.Fatalf("a removed skill must be unloaded first, active=%v", b.ActiveSkills())
	}
	if m.flash != `Skill "db-migrations" removed.` {
		t.Fatalf("flash = %q", m.flash)
	}
}

func TestSkillsRemoveNeedsABackendThatCanRemove(t *testing.T) {
	m := modalModel(t, 140, 36, skillNoOptBackend{newRecBackend()})
	gitKey(m, "ctrl+s")
	mustContain(t, screen(m), "[Installed]") // no registry: opens on Installed
	gitKey(m, "ctrl+d")
	if m.flash != "Removing skills is not available from this backend." {
		t.Fatalf("flash = %q", m.flash)
	}
}

func TestSkillsWithoutRegistrySaysSo(t *testing.T) {
	m := modalModel(t, 140, 36, skillNoOptBackend{newRecBackend()})
	gitKey(m, "ctrl+s")
	skillsTabs(m, "Search")
	mustContain(t, screen(m), "skill registry is not available")
}

func TestSkillsPreviewShowsContextState(t *testing.T) {
	b := newRecBackend()
	if err := b.SetSkillActive("release-notes", true); err != nil {
		t.Fatal(err)
	}
	m := modalModel(t, 140, 36, b)
	gitKey(m, "ctrl+s")
	skillsTabs(m, "Installed")
	send(m, "down") // release-notes, the second project skill
	mustContain(t, screen(m), "context loaded into the AI context")
}

func TestSkillsInstallFromPathAsksScopeThenInstalls(t *testing.T) {
	b := newRecBackend()
	m := modalModel(t, 140, 36, b)
	gitKey(m, "ctrl+s")
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
	gitKey(m, "ctrl+s")
	send(m, "ctrl+n")
	send(m, "enter")
	if topTitle(m) != "Install skill" {
		t.Fatalf("an empty source must keep the prompt open, top=%q", topTitle(m))
	}
	mustContain(t, screen(m), "Nothing entered.")
}
