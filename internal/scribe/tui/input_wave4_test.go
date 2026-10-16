package tui

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
)

func TestSlashRankPrefixBeatsLooseMatch(t *testing.T) {
	got := rankSlash("ver")
	if len(got) == 0 || got[0].Name != "verbose" {
		names := []string{}
		for _, c := range got {
			names = append(names, c.Name)
		}
		t.Fatalf("rankSlash(\"ver\") = %v, want verbose first", names)
	}
}

func TestSlashRankExactNameFirst(t *testing.T) {
	got := rankSlash("verbose")
	if len(got) == 0 || got[0].Name != "verbose" {
		t.Fatalf("exact name should rank first")
	}
}

func TestSlashRowsFollowTerminalHeight(t *testing.T) {
	cases := []struct{ rows, want int }{{12, 4}, {40, 8}, {200, 8}, {0, 4}}
	for _, c := range cases {
		if got := slashPopupRows(c.rows); got != c.want {
			t.Errorf("slashPopupRows(%d) = %d, want %d", c.rows, got, c.want)
		}
	}
	if got := historyPopupRows(10); got != 3 {
		t.Errorf("historyPopupRows(10) = %d, want 3", got)
	}
	if got := historyPopupRows(60); got != 8 {
		t.Errorf("historyPopupRows(60) = %d, want 8", got)
	}
}

func TestFrecencyCountsPersistAcrossReload(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	bumpFrecency("verbose")
	bumpFrecency("verbose")
	bumpFrecency("help")
	// a fresh read of the shared store still ranks the most used command first
	names, err := session.TopPicks(session.PickCommand, 0)
	if err != nil || len(names) != 2 || names[0] != "verbose" {
		t.Fatalf("TopPicks = %v, %v; want verbose first", names, err)
	}
	if r := frecencyRanks()["verbose"]; r != 0 {
		t.Fatalf("rank of verbose = %d, want 0", r)
	}
}

func TestLegacyFrecencyObjectIsMigrated(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	legacyFrecencyOnce = sync.Once{}
	if err := os.WriteFile(filepath.Join(config.Home(), "frecency.json"), []byte(`{"help": 3, "mode": 1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	migrateLegacyFrecency()
	names, err := session.TopPicks(session.PickCommand, 0)
	if err != nil || len(names) != 2 || names[0] != "help" {
		t.Fatalf("after migration TopPicks = %v, %v; want help first", names, err)
	}
	bumpFrecency("mode")
	if r := frecencyRanks()["help"]; r != 0 {
		t.Fatalf("help should keep the top rank after a pick of mode, got %d", r)
	}
}

func TestSubmittingSlashCommandBumpsFrecency(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	m.cur().in.setValue("/verbose")
	m.submit("/verbose")
	names, _ := session.TopPicks(session.PickCommand, 0)
	if len(names) == 0 || names[0] != "verbose" {
		t.Fatalf("frecency after /verbose = %v, want verbose first", names)
	}
}

func TestPastePlaceholderShowsFirstLineAndCount(t *testing.T) {
	s := newInputState()
	got := s.collapsePaste("def handle_request(self, req):\n  pass\n  pass\n  pass")
	want := "<pasted (def handle_request(self, req): +4 lines)>"
	if got != want {
		t.Fatalf("placeholder = %q, want %q", got, want)
	}
	if s.expand(got) != "def handle_request(self, req):\n  pass\n  pass\n  pass" {
		t.Fatalf("expand did not restore the paste")
	}
}

func TestPasteUpToThreeLinesIsInsertedAsIs(t *testing.T) {
	s := newInputState()
	if got := s.collapsePaste("a\nb\nc"); got != "a\nb\nc" {
		t.Fatalf("three-line paste = %q, want it inserted as typed", got)
	}
}

func TestPasteFirstLineIsCutAtThirtyCharacters(t *testing.T) {
	long := strings.Repeat("x", 40)
	got := pastePlaceholder(long + "\nsecond")
	want := "<pasted (" + strings.Repeat("x", 30) + "... +2 lines)>"
	if got != want {
		t.Fatalf("placeholder = %q, want %q", got, want)
	}
}

func TestEditedPlaceholderIsSentLiterally(t *testing.T) {
	s := newInputState()
	ph := s.collapsePaste("a\nb\nc\nd")
	edited := strings.Replace(ph, "+4", "+5", 1)
	if got := s.expand(edited); got != edited {
		t.Fatalf("an edited token expanded to %q, want it sent as typed", got)
	}
}

func TestInputHeightGrowsWithVisualLinesAndCapsAtFortyPercent(t *testing.T) {
	s := newInputState()
	s.resize(40)
	s.setValue("one line")
	if got := s.inputHeight(30); got != 1 {
		t.Fatalf("one line: height %d, want 1", got)
	}
	s.setValue(strings.Repeat("x", 100)) // wraps to 3 rows at width 40
	if got := s.visualLines(); got != 3 {
		t.Fatalf("visual lines = %d, want 3", got)
	}
	if got := s.inputHeight(30); got != 3 {
		t.Fatalf("three visual lines: height %d, want 3 (the text height, under the cap)", got)
	}
	s.setValue(strings.Repeat("line\n", 40))
	if got := s.inputHeight(30); got != 12 {
		t.Fatalf("40 lines in 30 rows: height %d, want 12 (40%%)", got)
	}
	if got := s.inputHeight(10); got != 4 {
		t.Fatalf("small terminal: height %d, want the floor of 4", got)
	}
}

func TestPlaceholderSaysSteerWhileBusy(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	th := m.th
	text, _ := inputPlaceholder(m.cur(), th)
	if !strings.HasPrefix(text, "Ask about your code") {
		t.Fatalf("idle placeholder = %q", text)
	}
	m.cur().busy = true
	text, _ = inputPlaceholder(m.cur(), th)
	if text != "'/' for commands · or steer by sending a new message" {
		t.Fatalf("busy placeholder = %q", text)
	}
}

func TestCtrlRShowsMatchesWithCountAndSelection(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	in := m.cur().in
	in.history = []string{"alpha one", "beta two", "Alpha three"}
	in.openSearch()
	for _, r := range "alp" {
		in.search.query = append(in.search.query, r)
	}
	in.refreshSearch()

	lines := stripLines(in.popupLines(m, 60))
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "◷ history") || !strings.Contains(joined, "2 matches") {
		t.Fatalf("popup lacks the header and count:\n%s", joined)
	}
	if !strings.Contains(joined, "› Alpha three") || !strings.Contains(joined, "  alpha one") {
		t.Fatalf("popup rows wrong:\n%s", joined)
	}
	for i, l := range lines {
		if w := ansi.StringWidth(l); w != 60 {
			t.Fatalf("popup line %d is %d wide, want 60: %q", i, w, l)
		}
	}
}

func TestCtrlRWindowFollowsSelection(t *testing.T) {
	m, _ := newTestModel(t, 100, 20) // history popup shows 4 rows here
	in := m.cur().in
	for i := 0; i < 10; i++ {
		in.history = append(in.history, "entry "+string(rune('a'+i)))
	}
	in.openSearch()
	in.search.sel = 9
	joined := strings.Join(stripLines(in.popupLines(m, 60)), "\n")
	// matches run newest first, so selection 9 is the oldest entry, "entry a"
	if !strings.Contains(joined, "› entry a") {
		t.Fatalf("selection at the bottom is not visible:\n%s", joined)
	}
	if strings.Contains(joined, "entry j") {
		t.Fatalf("the window should have scrolled past the first entry:\n%s", joined)
	}
}

func TestHighlightMatchIsCaseInsensitive(t *testing.T) {
	if got := indexFoldRunes([]rune("Hello World"), []rune("world")); got != 6 {
		t.Fatalf("indexFoldRunes = %d, want 6", got)
	}
	if got := indexFoldRunes([]rune("abc"), []rune("")); got != -1 {
		t.Fatalf("empty needle = %d, want -1", got)
	}
}

func TestStashSurvivesRestartPerDirectory(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	t.Setenv("SCRIBE_HOME", t.TempDir())
	in := m.cur().in
	in.cwd = "/work/project"
	in.setValue("half written thought")

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("s"), Alt: true})

	if got := loadStashes("/work/project"); len(got) != 1 || got[0] != "half written thought" {
		t.Fatalf("stored stashes = %q, want the draft", got)
	}
	if got := loadStashes("/work/other"); len(got) != 0 {
		t.Fatalf("another directory sees the stash: %q", got)
	}

	// a new tab in the same directory restores the newest stash and removes it
	fresh := newInputState()
	fresh.mountStash("/work/project")
	if fresh.value() != "half written thought" {
		t.Fatalf("restored draft = %q", fresh.value())
	}
	if got := loadStashes("/work/project"); len(got) != 0 {
		t.Fatalf("restored stash was not removed from disk: %q", got)
	}
}

func TestStashFileLivesInScribeHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SCRIBE_HOME", home)
	if err := storeStashes("/x", []string{"d"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "stashes.json")); err != nil {
		t.Fatalf("stash file not in the Scribe home: %v", err)
	}
}

func TestEnterWaitsForClipboardRead(t *testing.T) {
	m, rec := newImageModel(t)
	withClipboard(m, t)
	typeText(m, "describe this")

	_, cmd := m.Update(ctrlV())
	if !m.cur().in.probing {
		t.Fatal("Ctrl+V did not mark the read as in flight")
	}
	if !strings.Contains(boxText(m), "reading image") {
		t.Fatalf("box does not show the read:\n%s", boxText(m))
	}
	m.Update(key("enter"))
	if rec.submits != 0 || m.cur().in.value() != "describe this" {
		t.Fatalf("Enter during the read submitted (%d) or cleared the input", rec.submits)
	}

	finishProbe(t, m, cmd)
	if m.cur().in.probing {
		t.Fatal("read still marked in flight after its result")
	}
	if strings.Contains(boxText(m), "reading image") {
		t.Fatal("box still shows the read after it finished")
	}
	m.Update(key("enter"))
	if rec.submits != 1 {
		t.Fatalf("Enter after the read submitted %d times, want 1", rec.submits)
	}
}

func TestSecondCtrlVDuringReadStartsNothing(t *testing.T) {
	m, _ := newImageModel(t)
	fc := withClipboard(m, t)
	_, first := m.Update(ctrlV())
	_, second := m.Update(ctrlV())
	if second != nil {
		t.Fatal("a second Ctrl+V during a read started another read")
	}
	finishProbe(t, m, first)
	if fc.reads != 1 {
		t.Fatalf("clipboard read %d times, want 1", fc.reads)
	}
}
