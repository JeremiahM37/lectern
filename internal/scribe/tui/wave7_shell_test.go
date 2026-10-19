//go:build !windows

package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/nvim"
)

func TestLastTurnInterruptedReadsTheNewestTurn(t *testing.T) {
	done := func() agent.Event { return agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{}} }
	aborted := agent.Event{Kind: agent.EvTurnDone, Stats: &agent.TurnStats{Aborted: true}}
	start := agent.Event{Kind: agent.EvTurnStart, Prompt: "x"}
	cases := []struct {
		name string
		evs  []agent.Event
		want bool
	}{
		{"empty", nil, false},
		{"finished", []agent.Event{start, done()}, false},
		{"aborted", []agent.Event{start, aborted}, true},
		{"killed mid-turn", []agent.Event{start, {Kind: agent.EvText, Text: "partial"}}, true},
		{"continued to the end", []agent.Event{start, {Kind: agent.EvText}, start, done()}, false},
		{"interrupted then continued", []agent.Event{start, {Kind: agent.EvText}, {Kind: agent.EvTurnStart}}, true},
	}
	for _, c := range cases {
		if got := LastTurnInterrupted(c.evs); got != c.want {
			t.Errorf("%s: LastTurnInterrupted = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestSavedBannerLinesAddsTheContinueHintOnlyWhenInterrupted(t *testing.T) {
	start := agent.Event{Kind: agent.EvTurnStart, Prompt: "x"}
	plain := SavedBannerLines("abc", []agent.Event{start, {Kind: agent.EvTurnDone, Stats: &agent.TurnStats{}}})
	if len(plain) != 1 || plain[0] != SavedBanner("abc") {
		t.Fatalf("finished session banner = %q", plain)
	}
	cut := SavedBannerLines("abc", []agent.Event{start})
	if len(cut) != 2 || !strings.Contains(cut[1], "interrupted") || !strings.Contains(cut[1], "lectern code -c") {
		t.Fatalf("interrupted session banner = %q", cut)
	}
}

// turnList names one turn, or lists several in order.
func TestTurnListNamesEveryTurn(t *testing.T) {
	cases := map[string][]int{
		"turn 2":           {2},
		"turns 2 and 3":    {2, 3},
		"turns 1, 2 and 3": {1, 2, 3},
	}
	for want, turns := range cases {
		if got := turnList(turns); got != want {
			t.Errorf("turnList(%v) = %q, want %q", turns, got, want)
		}
	}
}

// redoAll is a backend whose one Redo re-applies every undone turn, as the
// real checkpoint store does, but reports only the first (as the backend does).
type redoAll struct {
	Backend
	cps []CheckpointInfo
}

func (r *redoAll) Checkpoints() []CheckpointInfo { return append([]CheckpointInfo(nil), r.cps...) }

func (r *redoAll) Redo() (CheckpointInfo, error) {
	var first CheckpointInfo
	seen := false
	for i := range r.cps {
		if r.cps[i].Undone {
			r.cps[i].Undone = false
			if !seen {
				first, seen = r.cps[i], true
			}
		}
	}
	return first, nil
}

// The redo flash names every turn one redo re-applied, and counts each file once.
func TestRedoFlashNamesEveryReappliedTurn(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	be := &redoAll{cps: []CheckpointInfo{
		{Turn: 1, Files: []string{"a.go"}},
		{Turn: 2, Files: []string{"a.go", "b.go"}, Undone: true},
		{Turn: 3, Files: []string{"c.go"}, Undone: true},
	}}
	b := &cpBrowser{m: m, be: be, th: m.th}
	b.reload()
	cmd := b.redo()
	if _, ok := m.topModal().(*busyWait); !ok {
		t.Fatalf("redo should run behind a busy modal, top is %T", m.topModal())
	}
	runCmd(m, cmd)
	want := "Redid turns 2 and 3 (3 files)"
	if m.flash != want {
		t.Fatalf("flash = %q, want %q", m.flash, want)
	}
}

// /terminals rename names the shown terminal, or one by number, and an empty
// name clears it. The name shows in the list label and the box title.
func TestTerminalsRenameNamesOneTerminal(t *testing.T) {
	m, _ := newTestModel(t, 120, 40)
	ps := m.ps()
	ps.floats = []*floatTerm{{}, {}}
	ps.shown = 1
	m.runCommand("/terminals rename build server")
	if ps.floats[1].name != "build server" || ps.floats[0].name != "" {
		t.Fatalf("names = %q / %q", ps.floats[0].name, ps.floats[1].name)
	}
	if got := ps.floats[1].label(1); got != "Terminal 2 · build server" {
		t.Fatalf("label = %q", got)
	}
	m.runCommand("/terminals rename 1 tests")
	if ps.floats[0].name != "tests" {
		t.Fatalf("numbered rename missed: %q", ps.floats[0].name)
	}
	m.runCommand("/terminals rename 9 nope")
	if m.flash == "" || ps.floats[1].name != "build server" {
		t.Fatalf("out-of-range rename should refuse, flash %q", m.flash)
	}
	long := strings.Repeat("x", 60)
	m.runCommand("/terminals rename " + long)
	if n := len([]rune(ps.floats[1].name)); n != 40 {
		t.Fatalf("name not capped: %d runes", n)
	}
	m.runCommand("/terminals rename")
	if ps.floats[1].name != "" {
		t.Fatalf("bare rename should clear the name, got %q", ps.floats[1].name)
	}
	ps.floats = nil
}

// /editor access and /editor sync are saved to the global config, and the
// saved values are applied at the next start.
func TestEditorSettingsPersistAndApplyAtStart(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	t.Cleanup(func() {
		nvim.SetAccess(nvim.AccessOn)
		nvim.SetSyncOnEdit(true)
		nvim.SetStartConfig("user")
	})
	m, _ := newTestModel(t, 120, 40)
	m.runCommand("/editor access when-open")
	m.runCommand("/editor sync off")
	if nvim.GetAccess() != nvim.AccessWhenOpen || nvim.SyncOnEdit() {
		t.Fatalf("commands did not apply: access %v sync %v", nvim.GetAccess(), nvim.SyncOnEdit())
	}
	c, err := config.LoadFile(config.GlobalPath())
	if err != nil {
		t.Fatal(err)
	}
	if c.EditorAccess != "when-open" || c.EditorSync == nil || *c.EditorSync {
		t.Fatalf("saved config = access %q sync %v", c.EditorAccess, c.EditorSync)
	}

	// A fresh start reads the saved settings back, and an edited config mode too.
	nvim.SetAccess(nvim.AccessOn)
	nvim.SetSyncOnEdit(true)
	c.EditorConfig = "none"
	if err := config.SaveGlobal(c); err != nil {
		t.Fatal(err)
	}
	applyEditorSettings("")
	if nvim.GetAccess() != nvim.AccessWhenOpen || nvim.SyncOnEdit() || nvim.StartConfig() != "none" {
		t.Fatalf("start did not apply: access %v sync %v start %q", nvim.GetAccess(), nvim.SyncOnEdit(), nvim.StartConfig())
	}
	m.runCommand("/editor sync bogus")
	if m.flash == "" {
		t.Fatalf("bad sync value should flash a usage error")
	}
}
