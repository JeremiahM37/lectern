package tui

import "testing"

// mapCtl is a recBackend that also implements MapController, recording calls.
type mapCtl struct {
	*recBackend
	prefs      MapPrefs
	applied    []MapPrefs
	cleared    int
	sumsClear  int
	regenerate int
}

func (c *mapCtl) MapPrefs() MapPrefs { return c.prefs }

func (c *mapCtl) SetMapPrefs(p MapPrefs) error {
	c.applied = append(c.applied, p)
	c.prefs = p
	return nil
}

func (c *mapCtl) ClearMap() error { c.cleared++; return nil }

func (c *mapCtl) ClearSummaries() error { c.sumsClear++; return nil }

func (c *mapCtl) RegenerateSummaries() error { c.regenerate++; return nil }

func newMapCtl() *mapCtl {
	return &mapCtl{recBackend: mapFixture(), prefs: MapPrefs{Mode: "ast", SummaryLimit: 300}}
}

func TestMapModeKeyThenApply(t *testing.T) {
	c := newMapCtl()
	m := modalModel(t, 140, 36, c)
	m.runCommand("/repo-map")
	send(m, "3")
	mustContain(t, screen(m), "Not applied yet")
	if len(c.applied) != 0 {
		t.Fatalf("settings must not apply before Enter")
	}
	send(m, "enter")
	if len(c.applied) != 1 || c.applied[0].Mode != "synthetic" {
		t.Fatalf("applied = %+v, want synthetic", c.applied)
	}
	mustContain(t, screen(m), "Applied.")
}

func TestMapBudgetStepsWithArrows(t *testing.T) {
	c := newMapCtl()
	m := modalModel(t, 140, 36, c)
	m.runCommand("/repo-map")
	send(m, "down")
	send(m, "down")
	send(m, "right")
	send(m, "enter")
	if len(c.applied) != 1 || c.applied[0].Budget != 2000 {
		t.Fatalf("budget after one step from auto = %+v, want 2000", c.applied)
	}
}

func TestMapClearMapNeedsSecondPress(t *testing.T) {
	c := newMapCtl()
	m := modalModel(t, 140, 36, c)
	m.runCommand("/repo-map")
	send(m, "X")
	if c.cleared != 0 {
		t.Fatalf("one X must only arm the clear")
	}
	send(m, "X")
	if c.cleared != 1 {
		t.Fatalf("second X should clear, got %d", c.cleared)
	}
}

func TestMapRegenerateNeedsLLMMode(t *testing.T) {
	c := newMapCtl()
	m := modalModel(t, 140, 36, c)
	m.runCommand("/repo-map")
	send(m, "G")
	mustContain(t, screen(m), "Press 4 or 5 first.")
	if c.regenerate != 0 {
		t.Fatalf("regenerate must wait for an llm or full mode")
	}
	send(m, "4")
	send(m, "G")
	if c.regenerate != 1 {
		t.Fatalf("regenerate = %d, want 1", c.regenerate)
	}
}

func TestMapClearSummariesNeedsSecondPress(t *testing.T) {
	c := newMapCtl()
	m := modalModel(t, 140, 36, c)
	m.runCommand("/repo-map")
	send(m, "C")
	send(m, "C")
	if c.sumsClear != 1 {
		t.Fatalf("C twice should clear summaries once, got %d", c.sumsClear)
	}
}

func TestMapWithoutControllerSaysSo(t *testing.T) {
	m := modalModel(t, 140, 36, mapFixture())
	m.runCommand("/repo-map")
	send(m, "X")
	send(m, "X")
	mustContain(t, screen(m), unsupportedNote)
}

func TestMapModalShowsSettingsRows(t *testing.T) {
	m := modalModel(t, 80, 30, newMapCtl())
	m.runCommand("/repo-map")
	mustContain(t, screen(m), "Summaries", "Limit", "Budget", "Auto-regen")
	checkWidths(t, m, 80, 30)
}

// mapToggle is a mapCtl that also saves the on/off switch, and reports the
// value in use through MapDetailer.
type mapToggle struct {
	*mapCtl
	enabled bool
	saves   []bool
	saveErr error
}

func (c *mapToggle) MapDetail() MapDetail { return MapDetail{Enabled: c.enabled, Running: true} }

func (c *mapToggle) SetMapEnabled(on bool) error {
	c.saves = append(c.saves, on)
	if c.saveErr != nil {
		return c.saveErr
	}
	c.enabled = on
	return nil
}

// The Enabled row toggles with e and is saved on Enter, with a note that it
// applies at the next start.
func TestMapEnabledRowSavesOnEnter(t *testing.T) {
	c := &mapToggle{mapCtl: newMapCtl(), enabled: true}
	m := modalModel(t, 140, 36, c)
	m.runCommand("/repo-map")
	send(m, "e")
	mustContain(t, screen(m), "Not applied yet")
	if len(c.saves) != 0 {
		t.Fatal("the on/off switch must not save before Enter")
	}
	send(m, "enter")
	if len(c.saves) != 1 || c.saves[0] {
		t.Fatalf("saves = %v, want one save of false", c.saves)
	}
	mustContain(t, screen(m), "takes effect the next time Scribe starts")
}

// When the save fails, the row shows the value in use again rather than the
// value that was typed.
func TestMapEnabledRowRevertsOnSaveError(t *testing.T) {
	c := &mapToggle{mapCtl: newMapCtl(), enabled: true, saveErr: errTestMapSave}
	m := modalModel(t, 140, 36, c)
	m.runCommand("/repo-map")
	send(m, "e")
	send(m, "enter")
	mustContain(t, screen(m), "Could not save the on/off setting")
	if len(c.saves) != 1 {
		t.Fatalf("saves = %v, want one attempt", c.saves)
	}
	mustContain(t, screen(m), "Enabled")
	send(m, "enter")
	if len(c.saves) != 1 {
		t.Fatalf("after the revert there is nothing to save, got %v", c.saves)
	}
}

var errTestMapSave = errStr("save refused")

type errStr string

func (e errStr) Error() string { return string(e) }
