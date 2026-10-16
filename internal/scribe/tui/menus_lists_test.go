package tui

import (
	"strings"
	"testing"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/llm"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/session"
)

// topLive returns the open live picker, failing the test if it is not one.
func topLive(t *testing.T, m *Model) *livePicker {
	t.Helper()
	lp, ok := m.topModal().(*livePicker)
	if !ok {
		t.Fatalf("top modal is %T, want *livePicker", m.topModal())
	}
	return lp
}

func TestPaletteSuggestsStaticCommandsWithoutHistory(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	m.openPalette()
	p := topLive(t, m).Picker
	if len(p.Items) == 0 || p.Items[0].Group != "Suggested" || p.Items[0].Value != "help" {
		t.Fatalf("first palette row = %+v, want the Suggested group led by help", p.Items[0])
	}
}

func TestPaletteSuggestsFrecentCommands(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	bumpFrecency("mode")
	bumpFrecency("mode")
	bumpFrecency("tools")
	m.openPalette()
	p := topLive(t, m).Picker
	if p.Items[0].Group != "Suggested" || p.Items[0].Value != "mode" || p.Items[1].Value != "tools" {
		t.Fatalf("suggested = %q, %q; want mode then tools", p.Items[0].Value, p.Items[1].Value)
	}
	for _, it := range p.Items[2:] {
		if it.Group == "Suggested" {
			t.Fatalf("suggested command %q repeated in a category group", it.Value)
		}
	}
}

func TestPaletteBlurbAndTabAcrossCategories(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	m.openPalette()
	lp := topLive(t, m)
	if !strings.Contains(lp.Blurb, "commands") || !strings.Contains(lp.Footer(), "Tab next category") {
		t.Fatalf("unfiltered blurb %q footer %q", lp.Blurb, lp.Footer())
	}
	before := lp.Picker.Items[lp.Picker.visible[lp.Picker.cursor]].Group
	send(m, "tab")
	after := topLive(t, m).Picker.Items[topLive(t, m).Picker.visible[topLive(t, m).Picker.cursor]].Group
	if before == after {
		t.Fatalf("tab stayed in group %q", before)
	}
	typeText(m, "ver")
	lp = topLive(t, m)
	if !strings.Contains(lp.Blurb, "matches") || !strings.Contains(lp.Footer(), "Esc clear") {
		t.Fatalf("filtered blurb %q footer %q", lp.Blurb, lp.Footer())
	}
	for _, it := range lp.Picker.Items {
		if it.Group != "Results" {
			t.Fatalf("filtered rows must be one Results group, got %q", it.Group)
		}
	}
	if !strings.Contains(lp.Picker.Items[0].Desc, " · ") {
		t.Fatalf("filtered row lacks category prefix: %q", lp.Picker.Items[0].Desc)
	}
}

func TestPaletteRunRecordsFrecency(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	m.openPalette()
	typeText(m, "help")
	send(m, "enter")
	names, _ := session.TopPicks(session.PickCommand, 0)
	if len(names) == 0 || names[0] != "help" {
		t.Fatalf("frecency after running help from the palette = %v", names)
	}
}

func TestSessionPickerClearAllDeletesEverySession(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, fb := newTestModel(t, 100, 40)
	m.openSessionPicker()
	lp := topLive(t, m)
	if !strings.Contains(lp.Blurb, "2 sessions") {
		t.Fatalf("blurb = %q, want 2 sessions", lp.Blurb)
	}
	lp.Picker.OnKey("ctrl+x")
	if _, ok := m.topModal().(*Confirm); !ok {
		t.Fatalf("Ctrl+X should ask first, top modal is %T", m.topModal())
	}
	c := m.topModal().(*Confirm)
	c.yesCmd()()
	if n := len(fb.Sessions()); n != 0 {
		t.Fatalf("%d sessions left after clear all", n)
	}
}

func TestErrorLogShowsWholeErrorInPreview(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 120, 40)
	m.errLog = append(m.errLog, errEntry{Tab: "main", Text: "shell failed: exit status 2 with a long tail of detail"})
	m.openErrorLog()
	lp := topLive(t, m)
	if len(lp.Picker.Items) != 1 {
		t.Fatalf("error rows = %d, want 1", len(lp.Picker.Items))
	}
	preview := lp.Picker.Preview(lp.Picker.Items[0])
	if !strings.Contains(strings.Join(preview, "\n"), "exit status 2") {
		t.Fatalf("preview lacks the error text: %q", preview)
	}
	if !strings.Contains(lp.Footer(), "^Y copy") {
		t.Fatalf("footer %q should name the copy key", lp.Footer())
	}
}

func TestOSC52EncodesClipboardText(t *testing.T) {
	if got, want := osc52("hi"), "\x1b]52;c;aGk=\a"; got != want {
		t.Fatalf("osc52 = %q, want %q", got, want)
	}
}

func TestStashPickerRestoresNewestIntoEmptyPrompt(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	t0 := m.cur()
	t0.in.stashes = []string{"older draft", "newer\ndraft"}
	t0.in.setValue("")
	m.openStashPicker()
	lp := topLive(t, m)
	it, _ := lp.Picker.Selected()
	if it.Value != "1" {
		t.Fatalf("newest stash should be first, selected value %q", it.Value)
	}
	lp.Picker.OnSelect(it)
	if got := t0.in.value(); got != "newer\ndraft" {
		t.Fatalf("prompt = %q, want the newest stash", got)
	}
	if len(t0.in.stashes) != 1 || t0.in.stashes[0] != "older draft" {
		t.Fatalf("stashes after restore = %q", t0.in.stashes)
	}
}

func TestStashPickerKeepsDraftWhenPromptHasText(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	t0 := m.cur()
	t0.in.stashes = []string{"keep me"}
	t0.in.setValue("typing")
	m.openStashPicker()
	it, _ := topLive(t, m).Picker.Selected()
	topLive(t, m).Picker.OnSelect(it)
	if len(t0.in.stashes) != 1 || t0.in.value() != "typing" {
		t.Fatalf("a stash must not replace a typed prompt: stashes %q, prompt %q", t0.in.stashes, t0.in.value())
	}
}

func TestStashPickerDeleteAndClearAll(t *testing.T) {
	t.Setenv("SCRIBE_HOME", t.TempDir())
	m, _ := newTestModel(t, 100, 40)
	t0 := m.cur()
	t0.in.stashes = []string{"a", "b", "c"}
	m.openStashPicker()
	lp := topLive(t, m)
	lp.Picker.OnKey("d")
	if len(t0.in.stashes) != 2 || len(lp.Picker.Items) != 2 {
		t.Fatalf("after d: stashes %q, rows %d", t0.in.stashes, len(lp.Picker.Items))
	}
	lp.Picker.OnKey("X")
	c, ok := m.topModal().(*Confirm)
	if !ok || !c.Danger() {
		t.Fatalf("X should open a danger confirm, got %T", m.topModal())
	}
	c.yesCmd()()
	if len(t0.in.stashes) != 0 {
		t.Fatalf("clear all left %q", t0.in.stashes)
	}
}

func TestModelDescMarksFreeCatalogModels(t *testing.T) {
	free := llm.ModelInfo{ID: "m", ContextWindow: 128000, CatalogPriced: true}
	if got := modelDesc(free, false); got != "128k ctx · FREE" {
		t.Fatalf("free model desc = %q", got)
	}
	estimated := llm.ModelInfo{ID: "m", ContextWindow: 128000}
	if got := modelDesc(estimated, true); strings.Contains(got, "FREE") || !strings.HasPrefix(got, "local") {
		t.Fatalf("unpriced local model desc = %q", got)
	}
}

func TestRankModelsPutsFrecentFirstAndKeepsOrder(t *testing.T) {
	ms := []llm.ModelInfo{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	got := rankModels("p", ms, map[string]int{"p/c": 0, "p/a": 1})
	if got[0].ID != "c" || got[1].ID != "a" || got[2].ID != "b" {
		t.Fatalf("rank order = %v %v %v", got[0].ID, got[1].ID, got[2].ID)
	}
	if ms[0].ID != "a" {
		t.Fatal("rankModels must not reorder its input")
	}
}

func TestHelpListsForgeModes(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	m.openHelp()
	v, ok := m.topModal().(*InfoView)
	if !ok {
		t.Fatalf("help top modal is %T", m.topModal())
	}
	if !strings.Contains(strings.Join(v.lines, "\n"), "Forge modes") {
		t.Fatal("help should list the forge modes")
	}
}
