package tui

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/JeremiahM37/lectern/v2/internal/scribe/tui/theme"
)

// The Appearance tab previews a theme as the cursor moves and reverts it on
// Esc. Both must carry the saved transparency and border, the way /theme does,
// or a preview shows a palette the user never chose.
func TestAppearancePreviewAndRevertKeepTransparencyAndBorder(t *testing.T) {
	m, fb := newTestModel(t, 120, 40)
	if err := fb.SetField7("theme_transparent", true, false); err != nil {
		t.Fatal(err)
	}
	if err := fb.SetField7("theme_border", "strong", false); err != nil {
		t.Fatal(err)
	}
	m.applyThemeStyle()

	saved := m.themeName
	style := styleFromConfig(fb.MergedConfig7())
	base, ok := theme.Lookup(saved)
	if !ok {
		t.Fatalf("saved theme %q not found", saved)
	}
	wantSaved := theme.Adjust(base, style)

	other := ""
	for _, n := range theme.Names() {
		if n != saved {
			other = n
			break
		}
	}
	if other == "" {
		t.Fatal("need a second theme to preview")
	}
	otherBase, _ := theme.Lookup(other)
	wantPreview := theme.Adjust(otherBase, style)

	s := &settings{m: m, be: m.cur().be, saved: saved,
		styles: map[string]modelStyle{}, loading: map[string]bool{}}
	s.tab = stAppearance
	row := -1
	for i, r := range s.rows() {
		if r.id == other {
			row = i
		}
	}
	if row < 0 {
		t.Fatalf("no Appearance row for %q", other)
	}

	s.cur[stAppearance] = row
	s.previewTheme()
	if m.themeName != other {
		t.Fatalf("preview did not switch to %q (got %q)", other, m.themeName)
	}
	if _, transparent := m.th.BgApp.(lipgloss.NoColor); !transparent {
		t.Fatalf("preview lost transparency: BgApp = %#v", m.th.BgApp)
	}
	if !reflect.DeepEqual(m.th, wantPreview) {
		t.Fatal("preview is not the other theme with the saved style applied")
	}

	s.revertTheme()
	if m.themeName != saved {
		t.Fatalf("revert did not restore %q (got %q)", saved, m.themeName)
	}
	if !reflect.DeepEqual(m.th, wantSaved) {
		t.Fatal("revert did not restore the saved theme with its transparency and border")
	}
}
