package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// A palette picked in /theme keeps the saved transparency and border.
func TestThemePickerKeepsSavedStyle(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	if err := fb.SetField7("theme_transparent", true, false); err != nil {
		t.Fatal(err)
	}
	if err := fb.SetField7("theme_border", "strong", false); err != nil {
		t.Fatal(err)
	}
	m.openThemePicker()
	p := m.topModal().(*Picker)
	p.SelectValue("dracula")
	p.OnCursor(PickItem{Value: "dracula"})
	want := m.styledTheme(mustTheme(t, "dracula"))
	if m.th.Border != want.Border || m.th.BgApp != want.BgApp {
		t.Fatalf("cursor preview lost the saved style: border %v want %v", m.th.Border, want.Border)
	}
	if m.th.BgApp == mustTheme(t, "dracula").BgApp {
		t.Fatalf("transparent background was not applied to the preview")
	}
}

// Tab, M, D and B change the saved style and the picker's state line.
func TestThemePickerKeysChangeStyle(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	m.openThemePicker()
	p := m.topModal().(*Picker)

	p.Update(tea.KeyMsg{Type: tea.KeyTab})
	if !config.Bool(fb.MergedConfig7().ThemeTransparent, false) {
		t.Fatal("tab did not turn transparency on")
	}
	p.Update(dkey("m"))
	if got := fb.MergedConfig7().ThemeMessageOpacity; got == nil || *got != 0 {
		t.Fatalf("m did not step message opacity to the first step: %v", got)
	}
	p.Update(dkey("d"))
	if got := fb.MergedConfig7().ThemeDiffOpacity; got == nil || *got != 0 {
		t.Fatalf("d did not step diff opacity: %v", got)
	}
	p.Update(dkey("b"))
	if got := fb.MergedConfig7().ThemeBorder; got != "strong" {
		t.Fatalf("b did not cycle the border: %q", got)
	}
	if !strings.Contains(p.Blurb, "transparent on") || !strings.Contains(p.Blurb, "border strong") {
		t.Fatalf("state line not refreshed: %q", p.Blurb)
	}
}

// While the picker is filtering, letters are search text, not style keys.
func TestThemePickerLettersSearchWhenFiltering(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	m.openThemePicker()
	p := m.topModal().(*Picker)
	p.Update(dkey("/"))
	p.Update(dkey("b"))
	if fb.MergedConfig7().ThemeBorder != "" {
		t.Fatal("a letter typed into the search changed the border")
	}
	if p.query != "b" {
		t.Fatalf("query = %q, want b", p.query)
	}
}

// Each palette row says whether it is light or dark.
func TestThemePickerRowsShowVariant(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.openThemePicker()
	p := m.topModal().(*Picker)
	found := false
	for _, it := range p.Items {
		if strings.Contains(it.Desc, "☾ dark") || strings.Contains(it.Desc, "☀ light") {
			found = true
		}
	}
	if !found {
		t.Fatal("no theme row shows its variant icon")
	}
}

// A failed checkpoint restore opens a dismissable error alert.
func TestCheckpointFailureOpensAlert(t *testing.T) {
	m, _ := newTestModel(t, 100, 30)
	m.showAlert("Restore failed", "boom")
	a, ok := m.topModal().(*Alert)
	if !ok {
		t.Fatalf("top modal = %T, want *Alert", m.topModal())
	}
	if a.level != "error" || a.Title() != "Restore failed" {
		t.Fatalf("alert = %+v", a)
	}
}

// The footer names the browsed checkpoint and the keys that leave it.
func TestFooterNamesBrowsedCheckpoint(t *testing.T) {
	m, _ := newTestModel(t, 120, 30)
	tb := m.cur()
	tb.cps = []CheckpointInfo{{Turn: 3}, {Turn: 5}}
	tb.browse = 1
	got := ansi.Strip(m.viewFooter())
	if !strings.Contains(got, "turn 5") || !strings.Contains(got, "Ctrl+F forward") {
		t.Fatalf("footer while browsing = %q", got)
	}
	tb.browse = -1
	if strings.Contains(ansi.Strip(m.viewFooter()), "Ctrl+F forward") {
		t.Fatal("live view still shows the browse legend")
	}
}

func mustTheme(t *testing.T, name string) theme.Theme {
	t.Helper()
	th, ok := theme.Lookup(name)
	if !ok {
		t.Fatalf("no theme %q", name)
	}
	return th
}
