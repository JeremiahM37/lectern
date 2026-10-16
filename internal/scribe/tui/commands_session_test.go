package tui

import (
	"strings"
	"testing"
)

// continueBackend is a FakeBackend that can continue an interrupted turn.
type continueBackend struct {
	*FakeBackend
	unfinished bool
	ran        bool
}

func (c *continueBackend) Continue() (bool, error) {
	if !c.unfinished {
		return false, nil
	}
	c.ran = true
	return true, nil
}

func TestSessionContinueNamesResolve(t *testing.T) {
	for line, want := range map[string]string{
		"/session continue": "session",
		"/continue":         "continue",
	} {
		c, args := findCommand(line)
		if c == nil || c.Name != want {
			t.Fatalf("%q resolved to %v, want %s", line, c, want)
		}
		if line == "/session continue" && args != "continue" {
			t.Fatalf("%q args = %q, want continue", line, args)
		}
	}
}

func TestSessionContinueRunsUnfinishedTurn(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	cb := &continueBackend{FakeBackend: NewFakeBackend(), unfinished: true}
	m.tabs[0].be = cb
	m.runCommand("/session continue")
	if !cb.ran {
		t.Fatal("/session continue should re-run the unfinished turn")
	}
	if !m.cur().busy {
		t.Fatal("the tab should show a running turn")
	}
}

func TestSessionContinueSaysWhenNothingIsUnfinished(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	cb := &continueBackend{FakeBackend: NewFakeBackend()}
	m.tabs[0].be = cb
	m.phase = phaseChat // the landing page hides the transcript
	m.runCommand("/continue")
	if cb.ran || m.cur().busy {
		t.Fatal("a finished turn must not run again")
	}
	// The info row is in the transcript; the frame does not always draw its body.
	found := false
	for _, r := range m.cur().chat.Export() {
		found = found || strings.Contains(r.Text, "Nothing to continue")
	}
	if !found {
		t.Fatal("the transcript should say there is nothing to continue")
	}
}

func TestSessionContinueNeedsContinuerBackend(t *testing.T) {
	m, _ := newTestModel(t, 100, 30) // a plain FakeBackend cannot continue
	runCmd(m, m.runCommand("/session continue"))
	if !strings.Contains(m.flash, "cannot continue") {
		t.Fatalf("flash = %q, want a refusal", m.flash)
	}
}

func TestSessionUsageForUnknownSubcommand(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	runCmd(m, m.runCommand("/session nonsense"))
	if !strings.Contains(m.flash, "Usage: /session") {
		t.Fatalf("flash = %q, want usage", m.flash)
	}
}

func TestSavedBannerCarriesFullIDForResume(t *testing.T) {
	got := SavedBanner("7f3c9a21-0000-4000-8000-000000000000")
	want := "Session saved: 7f3c9a21-0000-4000-8000-000000000000 — resume with: lectern code -s 7f3c9a21-0000-4000-8000-000000000000"
	if got != want {
		t.Fatalf("banner = %q", got)
	}
}
