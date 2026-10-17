package tui

import (
	"strings"
	"testing"
)

// The scope bar is hidden on a tab with no config-backed rows, where a save
// cannot land in either file, and shown on tabs whose saves do.
func TestSettingsScopeBarHiddenWhereScopeCannotApply(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	s := openSettings(t, m)
	for tab := 0; tab < stTabCount; tab++ {
		s.tab = tab
		got := screen(m)
		keyed := hasConfigRows(s.rows())
		shown := strings.Contains(got, "Saving to")
		if shown != keyed {
			t.Errorf("tab %s: scope bar shown=%v, config rows=%v", stTabNames[tab], shown, keyed)
		}
	}
	s.tab = stReasoning
	if strings.Contains(screen(m), "move to scope") {
		t.Fatal("the Think (reasoning) tab has no config rows, so it must not offer a scope move")
	}
}
