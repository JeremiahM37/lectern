package theme

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestLerpEndpointsAndMidpoint(t *testing.T) {
	a, b := lipgloss.Color("#000000"), lipgloss.Color("#ffffff")
	if got := colorString(t, Lerp(a, b, 0)); got != "#000000" {
		t.Errorf("t=0: %s", got)
	}
	if got := colorString(t, Lerp(a, b, 1)); got != "#ffffff" {
		t.Errorf("t=1: %s", got)
	}
	if got := colorString(t, Lerp(a, b, 0.5)); got != "#808080" && got != "#7f7f7f" {
		t.Errorf("t=0.5: %s", got)
	}
	// Out-of-range t is clamped, not extrapolated.
	if got := colorString(t, Lerp(a, b, 3)); got != "#ffffff" {
		t.Errorf("t=3 not clamped: %s", got)
	}
}

func TestLerpShortHexAndNonHex(t *testing.T) {
	if got := colorString(t, Lerp(lipgloss.Color("#fff"), lipgloss.Color("#000"), 0.5)); got != "#808080" && got != "#7f7f7f" {
		t.Errorf("#rgb blend: %s", got)
	}
	// An ANSI index cannot blend: it switches at the midpoint.
	a, b := lipgloss.Color("12"), lipgloss.Color("#123456")
	if got := colorString(t, Lerp(a, b, 0.2)); got != "12" {
		t.Errorf("below midpoint: %s", got)
	}
	if got := colorString(t, Lerp(a, b, 0.8)); got != "#123456" {
		t.Errorf("above midpoint: %s", got)
	}
}

func TestBreathCyclesAndPeaksAtQuarterPeriod(t *testing.T) {
	a, b := lipgloss.Color("#000000"), lipgloss.Color("#ffffff")
	const period = 20
	start := colorString(t, Breath(a, b, 0, period))
	if again := colorString(t, Breath(a, b, period, period)); again != start {
		t.Errorf("period %d not a full cycle: %s vs %s", period, again, start)
	}
	// sin peaks a quarter of the way round, at the far colour.
	if peak := colorString(t, Breath(a, b, period/4, period)); peak != "#ffffff" {
		t.Errorf("peak = %s, want #ffffff", peak)
	}
	if neg := colorString(t, Breath(a, b, -period/4, period)); neg != "#000000" {
		t.Errorf("negative tick trough = %s, want #000000", neg)
	}
}

func TestPingIsATriangleAndReturns(t *testing.T) {
	a, b := lipgloss.Color("#000000"), lipgloss.Color("#ffffff")
	if got := colorString(t, Ping(a, b, 0, 6)); got != "#000000" {
		t.Errorf("start %s", got)
	}
	if got := colorString(t, Ping(a, b, 3, 6)); got != "#ffffff" {
		t.Errorf("middle %s", got)
	}
	if got := colorString(t, Ping(a, b, 6, 6)); got != "#000000" {
		t.Errorf("end %s", got)
	}
}

func TestAdjustTransparencyClearsAppBackground(t *testing.T) {
	th := Get(ScribeID)
	out := Adjust(th, Style{Transparent: true, UserMessageOpacity: 100, DiffOpacity: 100})
	if _, ok := out.BgApp.(lipgloss.NoColor); !ok {
		t.Errorf("transparent BgApp = %#v, want NoColor", out.BgApp)
	}
	if colorString(t, out.BgUser) != colorString(t, th.BgUser) {
		t.Errorf("100%% opacity should leave bgUser unchanged")
	}
	// Without transparency the app background stays.
	if colorString(t, Adjust(th, DefaultStyle()).BgApp) != colorString(t, th.BgApp) {
		t.Errorf("opaque style changed BgApp")
	}
}

func TestOpacityBlendsTowardBlackOnlyWhenTransparent(t *testing.T) {
	th := Theme{BgUser: lipgloss.Color("#ff8000"), DiffAddedBg: lipgloss.Color("#202020"), DiffRemovedBg: lipgloss.Color("#101010")}
	half := Adjust(th, Style{Transparent: true, UserMessageOpacity: 50, DiffOpacity: 50})
	if got := colorString(t, half.BgUser); got != "#804000" {
		t.Errorf("50%% bgUser = %s, want #804000", got)
	}
	if got := colorString(t, half.DiffAddedBg); got != "#101010" {
		t.Errorf("50%% diffAddedBg = %s, want #101010", got)
	}
	zero := Adjust(th, Style{Transparent: true, UserMessageOpacity: 0, DiffOpacity: 100})
	if _, ok := zero.BgUser.(lipgloss.NoColor); !ok {
		t.Errorf("0%% bgUser should be transparent, got %#v", zero.BgUser)
	}
	// Opacity has no effect when the background is opaque.
	opaque := Adjust(th, Style{UserMessageOpacity: 0, DiffOpacity: 0})
	if colorString(t, opaque.BgUser) != "#ff8000" {
		t.Errorf("opacity applied without transparency: %s", colorString(t, opaque.BgUser))
	}
}

func TestBorderStrengthBrightensWithClamp(t *testing.T) {
	th := Theme{Border: lipgloss.Color("#2d3446"), TextFaint: lipgloss.Color("#202020"), TextSubtle: lipgloss.Color("#a0a0a0")}
	strong := Adjust(th, Style{UserMessageOpacity: 100, DiffOpacity: 100, BorderStrength: "strong"})
	if got := colorString(t, strong.Border); got != "#515e7e" { // 45*1.8=81, 52*1.8=94, 70*1.8=126
		t.Errorf("strong border = %s, want #515e7e", got)
	}
	if got := colorString(t, strong.TextSubtle); got != "#ffffff" { // 0xa0*1.8 clamps at 255
		t.Errorf("strong subtle = %s, want clamp to #ffffff", got)
	}
	op := Adjust(th, Style{BorderStrength: "op"})
	if got := colorString(t, op.TextFaint); got != "#5a5a5a" { // 32*2.8=89.6, rounds to 90
		t.Errorf("op faint = %s", got)
	}
	// Default strength is the identity, and border strength applies without transparency.
	if colorString(t, Adjust(th, Style{BorderStrength: "default"}).Border) != "#2d3446" {
		t.Errorf("default strength changed the border")
	}
}

func TestAdjustLeavesANSIIndexAlone(t *testing.T) {
	th := Theme{Border: lipgloss.Color("8")}
	if got := colorString(t, Adjust(th, Style{BorderStrength: "op"}).Border); got != "8" {
		t.Errorf("ANSI index was rewritten: %s", got)
	}
}

func TestAdjustDoesNotMutateInput(t *testing.T) {
	th := Get(ScribeID)
	before := colorString(t, th.Border)
	Adjust(th, Style{Transparent: true, BorderStrength: "op", UserMessageOpacity: 30, DiffOpacity: 30})
	if colorString(t, th.Border) != before {
		t.Errorf("Adjust changed its input")
	}
}

func TestVariantIcon(t *testing.T) {
	if got := (Theme{Variant: "light"}).VariantIcon(); got != "☀" {
		t.Errorf("light icon %q", got)
	}
	if got := (Theme{Variant: "dark"}).VariantIcon(); got != "☾" {
		t.Errorf("dark icon %q", got)
	}
}

func TestDefaultStyleOpacityStepsAreOpaque(t *testing.T) {
	d := DefaultStyle()
	if d.UserMessageOpacity != 100 || d.DiffOpacity != 100 || d.BorderFactor() != 1 {
		t.Errorf("DefaultStyle = %+v", d)
	}
	if OpacitySteps[len(OpacitySteps)-1] != 100 {
		t.Errorf("last opacity step should be opaque")
	}
}

func TestLegacyThemesFileMergesWithPerIDFile(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "themes.json")
	themes := filepath.Join(dir, "themes")
	if err := os.Mkdir(themes, 0o755); err != nil {
		t.Fatal(err)
	}
	must := func(p, s string) {
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must(legacy, `{"olive": {"_label": "Olive legacy", "_description": "from the combined file", "brand": "#112233", "bg-app": "#010203"}}`)
	must(filepath.Join(themes, "olive.json"), `{"_label": "Olive", "brand": "#445566"}`)
	t.Cleanup(func() { _ = LoadUserThemes(filepath.Join(dir, "none")) })

	if err := LoadUserThemesFrom(themes, legacy); err != nil {
		t.Fatalf("LoadUserThemesFrom: %v", err)
	}
	th, ok := Lookup("olive")
	if !ok {
		t.Fatal("olive not loaded")
	}
	if th.Label != "Olive" {
		t.Errorf("label %q: the per-id file should win", th.Label)
	}
	if th.Description != "from the combined file" {
		t.Errorf("description %q: legacy key lost in the merge", th.Description)
	}
	if colorString(t, th.Brand) != "#445566" {
		t.Errorf("brand %s: per-id file should win", colorString(t, th.Brand))
	}
	if colorString(t, th.BgApp) != "#010203" {
		t.Errorf("bgApp %s: legacy-only key lost", colorString(t, th.BgApp))
	}
}

func TestLegacyThemesFileMissingIsFine(t *testing.T) {
	t.Cleanup(func() { _ = LoadUserThemes(filepath.Join(t.TempDir(), "none")) })
	if err := LoadUserThemesFrom(t.TempDir(), filepath.Join(t.TempDir(), "themes.json")); err != nil {
		t.Errorf("missing legacy file should not be an error: %v", err)
	}
}

func TestLegacyThemesFileBadEntryReported(t *testing.T) {
	dir := t.TempDir()
	legacy := filepath.Join(dir, "themes.json")
	if err := os.WriteFile(legacy, []byte(`{"ok": {"brand": "#101010"}, "bad": "nope"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = LoadUserThemes(filepath.Join(dir, "none")) })
	err := LoadUserThemesFrom(filepath.Join(dir, "none"), legacy)
	if err == nil {
		t.Fatal("a non-object legacy entry should be reported")
	}
	if _, ok := Lookup("ok"); !ok {
		t.Error("the valid legacy entry should still load")
	}
}
