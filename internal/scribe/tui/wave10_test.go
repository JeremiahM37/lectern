package tui

// Wave 10 (tui10 packet): picker controls, the Save-to bar, key source badges,
// the theme style and the inline read image. Each test pins what a person sees
// or what is saved.

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/agent"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/config"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tools"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/chat"
	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

func TestPickerToggleRowFlipsOnEnterAndSpace(t *testing.T) {
	p := NewPicker(theme.Default(), "T", "")
	p.SetItems([]PickItem{{Value: "vim", Label: "Vim", Kind: PickToggle, On: false}})
	var got []bool
	p.OnSelect = func(it PickItem) tea.Cmd { got = append(got, it.On); return nil }
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if len(got) != 2 || !got[0] || got[1] {
		t.Fatalf("enter then space should report on then off, got %v", got)
	}
	if p.Items[0].On {
		t.Fatal("the picker should keep the toggle's state on the item")
	}
	if out := ansi.Strip(strings.Join(p.renderRows(60), "\n")); !strings.Contains(out, "[ ]") {
		t.Fatalf("an off toggle should draw an empty box, got %q", out)
	}
}

func TestPickerSelectorStepsThroughOptions(t *testing.T) {
	p := NewPicker(theme.Default(), "T", "")
	p.SetItems([]PickItem{{Value: "mode", Label: "Mode", Kind: PickSelect, Options: []string{"a", "b", "c"}}})
	var last string
	p.OnSelect = func(it PickItem) tea.Cmd { last = it.Options[it.Choice]; return nil }
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	p.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if last != "a" {
		t.Fatalf("three steps over three options should wrap to the first, got %q", last)
	}
	if out := ansi.Strip(strings.Join(p.renderRows(60), "\n")); !strings.Contains(out, "‹ a ›") {
		t.Fatalf("the selector should show its value between arrows, got %q", out)
	}
}

func TestPickerScopeBarCyclesAndReportsScope(t *testing.T) {
	p := NewPicker(theme.Default(), "T", "")
	p.ScopeOptions = []string{"project", "global"}
	p.SetItems([]PickItem{{Value: "a", Label: "a"}})
	var seen []string
	p.OnScope = func(s string) tea.Cmd { seen = append(seen, s); return nil }
	if p.Scope() != "project" {
		t.Fatalf("the bar should start on project, got %q", p.Scope())
	}
	p.Update(tea.KeyMsg{Type: tea.KeyCtrlO})
	if p.Scope() != "global" || len(seen) != 1 || seen[0] != "global" {
		t.Fatalf("ctrl+o should move to global and report it, scope %q seen %v", p.Scope(), seen)
	}
	p.SetScope("project")
	if p.Scope() != "project" {
		t.Fatalf("SetScope should select project, got %q", p.Scope())
	}
	out := ansi.Strip(p.View(70, 12))
	mustContain(t, out, "Save to", "[project]", "global")
}

func TestToolsPickerRowsAreToggles(t *testing.T) {
	m, fb := newTestModel(t, 100, 30)
	m.openToolsModal()
	p, ok := m.topModal().(*Picker)
	if !ok {
		t.Fatalf("tools should open a picker, top modal is %T", m.topModal())
	}
	for _, it := range p.Items {
		if it.Kind != PickToggle {
			t.Fatalf("tool row %q should be a toggle", it.Value)
		}
	}
	it, _ := p.Selected()
	before := it.On
	fb.mu.Lock()
	prior := fb.toolOn[it.Value]
	fb.mu.Unlock()
	p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	fb.mu.Lock()
	saved := fb.toolOn[it.Value]
	fb.mu.Unlock()
	if saved == prior {
		t.Fatalf("space should save the tool %q, its state stayed %v", it.Value, prior)
	}
	if now, _ := p.Selected(); now.On == before {
		t.Fatal("space should flip the row")
	}
}

func TestKeySourceBadge(t *testing.T) {
	cases := map[string]string{"env:BRAVE_SEARCH_API_KEY": "[env]", "store": "[config]", "": "[none]"}
	for src, want := range cases {
		if got := keySourceBadge(src); got != want {
			t.Errorf("keySourceBadge(%q) = %q, want %q", src, got, want)
		}
	}
}

func TestThemeStyleFromConfigAppliesToPalette(t *testing.T) {
	c := config.Config{}
	on := true
	msg, diff := 30, 0
	c.ThemeTransparent, c.ThemeMessageOpacity, c.ThemeDiffOpacity, c.ThemeBorder = &on, &msg, &diff, "strong"
	s := styleFromConfig(c)
	if !s.Transparent || s.UserMessageOpacity != 30 || s.DiffOpacity != 0 || s.BorderStrength != "strong" {
		t.Fatalf("style from config = %+v", s)
	}
	base := theme.Resolve("scribe")
	adj := theme.Adjust(base, s)
	if adj.BgApp == nil {
		t.Fatal("transparent should clear the app background")
	}
	if styleFromConfig(config.Config{}) != theme.DefaultStyle() {
		t.Fatal("an empty config should give the default style")
	}
}

func TestThemeStyleKeysRoundTripThroughConfig(t *testing.T) {
	var c config.Config
	if _, err := c.SetField("theme_transparent", "on"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetField("theme_message_opacity", 70); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetField("theme_diff_opacity", "30"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetField("theme_border", "op"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.SetField("theme_message_opacity", 101); err == nil {
		t.Fatal("an opacity over 100 should be refused")
	}
	s := styleFromConfig(c)
	if !s.Transparent || s.UserMessageOpacity != 70 || s.DiffOpacity != 30 || s.BorderStrength != "op" {
		t.Fatalf("saved style did not round trip: %+v", s)
	}
	merged := config.Merge(config.Config{}, c)
	if styleFromConfig(merged) != s {
		t.Fatal("merging the global file must keep the theme style")
	}
}

func TestStyleRowsCycleAndSave(t *testing.T) {
	m, _ := newTestModel(t, 100, 40)
	s := &settings{m: m, be: m.cur().be}
	rows := s.styleRows()
	if len(rows) != 4 {
		t.Fatalf("expected four style rows, got %d", len(rows))
	}
	if rows[1].value != "100%" {
		t.Fatalf("message background should start at 100%%, got %q", rows[1].value)
	}
	if got := theme.OpacitySteps; got[len(got)-1] != 100 {
		t.Fatal("opacity steps should end at 100")
	}
}

// writeTestPNG writes a w x h image to path.
func writeTestPNG(t *testing.T, path string, w, h int) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 128, A: 255})
		}
	}
	fh, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer fh.Close()
	if err := png.Encode(fh, img); err != nil {
		t.Fatal(err)
	}
}

func readImageEvents(dir, rel string) []agent.Event {
	return []agent.Event{
		{Kind: agent.EvToolStart, CallID: "r1", Tool: "read"},
		{Kind: agent.EvToolRun, CallID: "r1", Tool: "read", Args: []byte(`{"path":"` + rel + `"}`)},
		{Kind: agent.EvToolResult, CallID: "r1", Tool: "read", Result: &tools.Result{
			Output:  "Image " + rel + " (png).",
			Summary: "image",
			Meta:    map[string]any{"image": rel, "cols": 60},
		}},
	}
}

func TestReadImageShownInlineWideOnly(t *testing.T) {
	dir := t.TempDir()
	writeTestPNG(t, filepath.Join(dir, "shot.png"), 40, 20)
	render := func(width int) string {
		v := chat.New(chat.Options{Theme: theme.Resolve("scribe"), Workdir: dir})
		v.SetSize(width, 60)
		for _, e := range readImageEvents(dir, "shot.png") {
			v.Apply(e)
		}
		return ansi.Strip(v.View())
	}
	wide := render(80)
	if !strings.Contains(wide, "shot.png") || !strings.Contains(wide, "40×20") {
		t.Fatalf("an 80-column transcript should show the image path and size:\n%s", wide)
	}
	if !strings.Contains(wide, "▀") && !strings.Contains(wide, "▄") {
		t.Fatalf("the image should be drawn as half-block art:\n%s", wide)
	}
	narrow := render(50)
	if strings.Contains(narrow, "40×20") {
		t.Fatalf("a 50-column transcript must not draw the image:\n%s", narrow)
	}
}
