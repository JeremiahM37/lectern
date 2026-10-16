package theme

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// specThemes are the 36 built-in names from behavioural spec section 11.
var specThemes = []string{
	"dark", "solarized-dark", "catppuccin", "catppuccin-frappe", "catppuccin-macchiato",
	"gruvbox-dark", "tokyo-night", "tokyonight-storm", "dracula", "nord", "one-dark",
	"rose-pine", "kanagawa", "github-dark", "everforest-dark", "ayu-dark", "nightfox",
	"proxysoul-main", "proxysoul-coffee", "proxysoul-water", "cyberdream", "oxocarbon",
	"sonokai", "moonfly", "melange", "solarized-osaka", "bamboo", "nordic", "synthwave",
	"iceberg", "ember", "vesper", "light", "catppuccin-latte", "github-light", "one-light",
}

func withCustomDir(t *testing.T, files map[string]string) {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := LoadUserThemes(dir); err != nil {
		t.Fatalf("LoadUserThemes: %v", err)
	}
	t.Cleanup(func() { _ = LoadUserThemes(t.TempDir()) })
}

func colorString(t *testing.T, c lipgloss.TerminalColor) string {
	t.Helper()
	lc, ok := c.(lipgloss.Color)
	if !ok {
		return ""
	}
	return string(lc)
}

func TestBuiltinCountAndNames(t *testing.T) {
	names := Names()
	have := map[string]bool{}
	for _, n := range names {
		have[n] = true
	}
	for _, want := range specThemes {
		if !have[want] {
			t.Errorf("built-in %q missing from Names()", want)
		}
	}
	if len(names) < 36 {
		t.Fatalf("Names() = %d entries, want >= 36", len(names))
	}
	if names[0] != ScribeID {
		t.Errorf("first name = %q, want %q", names[0], ScribeID)
	}
}

func TestEveryThemeHasEveryTokenNonEmpty(t *testing.T) {
	for _, name := range Names() {
		th, ok := Lookup(name)
		if !ok {
			t.Fatalf("Lookup(%q) failed", name)
		}
		for key, c := range th.Tokens() {
			if colorString(t, c) == "" {
				t.Errorf("theme %q token %q is empty", name, key)
			}
		}
	}
}

func TestBuiltinValuesAreTranscribed(t *testing.T) {
	// Spot-check values against the spec's default table (section 11).
	dark := Get("dark")
	checks := map[string]string{
		colorString(t, dark.Brand):         "#9B30FF",
		colorString(t, dark.Error):         "#f44",
		colorString(t, dark.BgUser):        "#0a1218",
		colorString(t, dark.DiffRemovedBg): "#1a0a0a",
	}
	for got, want := range checks {
		if got != want {
			t.Errorf("dark token = %q, want %q", got, want)
		}
	}
	light := Get("light")
	if got := colorString(t, light.TextPrimary); got != "#222" {
		t.Errorf("light textPrimary = %q, want #222", got)
	}
	if light.Variant != "light" || dark.Variant != "dark" {
		t.Errorf("variants: light=%q dark=%q", light.Variant, dark.Variant)
	}
}

func TestDefaultIsScribeDark(t *testing.T) {
	d := Default()
	if d.Name != ScribeID || d.Variant != "dark" {
		t.Fatalf("Default() = %q/%q, want scribe dark", d.Name, d.Variant)
	}
	if got := colorString(t, d.Brand); got != "#C9A227" {
		t.Errorf("scribe brand = %q, want #C9A227", got)
	}
	if got := Get("no-such-theme"); got.Name != ScribeID {
		t.Errorf("unknown name fell back to %q, want scribe", got.Name)
	}
}

func TestCustomThemePartialOverride(t *testing.T) {
	withCustomDir(t, map[string]string{
		"mine.json": `{"_extends":"nord","_label":"Mine","brand":"#123456","bg-app":"#000000","nonsense":"x","text-muted":5}`,
	})
	mine := Get("mine")
	if mine.Label != "Mine" {
		t.Errorf("label = %q", mine.Label)
	}
	if got := colorString(t, mine.Brand); got != "#123456" {
		t.Errorf("brand override = %q", got)
	}
	if got := colorString(t, mine.BgApp); got != "#000000" {
		t.Errorf("bg-app override = %q", got)
	}
	nord := Get("nord")
	if colorString(t, mine.Error) != colorString(t, nord.Error) {
		t.Errorf("non-overridden token did not come from nord")
	}
	found := false
	for _, n := range Names() {
		if n == "mine" {
			found = true
		}
	}
	if !found {
		t.Error("user theme not listed")
	}
}

func TestCustomThemeCannotShadowBuiltin(t *testing.T) {
	before := colorString(t, Get("nord").Brand)
	withCustomDir(t, map[string]string{"nord.json": `{"brand":"#ffffff"}`})
	if got := colorString(t, Get("nord").Brand); got != before {
		t.Errorf("built-in nord shadowed: brand %q", got)
	}
}

func TestCustomThemeBadFileReported(t *testing.T) {
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, "broken.json"), []byte("{not json"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "good.json"), []byte(`{"_extends":"dark"}`), 0o600)
	err := LoadUserThemes(dir)
	t.Cleanup(func() { _ = LoadUserThemes(t.TempDir()) })
	if err == nil {
		t.Fatal("want an error naming the broken file")
	}
	if _, ok := Lookup("good"); !ok {
		t.Error("good theme dropped because another file was broken")
	}
}

func TestMissingUserDirIsNotAnError(t *testing.T) {
	if err := LoadUserThemes(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Fatalf("missing dir: %v", err)
	}
}

func TestNoColorStripsColours(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	th := Resolve(ScribeID)
	for key, c := range th.Tokens() {
		if _, ok := c.(lipgloss.NoColor); !ok {
			t.Errorf("NO_COLOR token %q is %T, want NoColor", key, c)
		}
	}
	if th.Name != ScribeID {
		t.Errorf("NO_COLOR changed name to %q", th.Name)
	}
}

func TestResolveWithoutNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	if got := colorString(t, Resolve(ScribeID).Brand); got != "#C9A227" {
		t.Errorf("brand = %q without NO_COLOR", got)
	}
}
