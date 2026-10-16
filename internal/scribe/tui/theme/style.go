package theme

import (
	"fmt"
	"math"

	"github.com/charmbracelet/lipgloss"
)

// Style is the display options the theme picker sets on top of a palette
// (spec 11, the config object form {transparent, userMessageOpacity,
// diffOpacity, borderStrength}). Use DefaultStyle as the starting point: the
// opacities are percentages, so a zero value means fully faded, not unset.
type Style struct {
	Transparent        bool   // the app background is left to the terminal
	UserMessageOpacity int    // percent of bgUser kept, applied only when transparent
	DiffOpacity        int    // percent of the diff backgrounds kept, only when transparent
	BorderStrength     string // "", "default", "strong" or "op"
}

// The picker's opacity steps (M and D cycle through them).
var OpacitySteps = []int{0, 30, 70, 100}

// DefaultStyle is an opaque theme with the default border strength.
func DefaultStyle() Style {
	return Style{UserMessageOpacity: 100, DiffOpacity: 100, BorderStrength: "default"}
}

// BorderFactor is how far the border and faint text colours are brightened.
func (s Style) BorderFactor() float64 {
	switch s.BorderStrength {
	case "strong":
		return 1.8
	case "op":
		return 2.8
	}
	return 1.0
}

// Adjust returns t with the style applied. It does not change t. Transparency
// clears the app background, and the opacities blend the user message and diff
// backgrounds toward black. Border strength applies with or without
// transparency. Colours that are not #rgb or #rrggbb are left as they are.
func Adjust(t Theme, s Style) Theme {
	if s.Transparent {
		t.BgApp = lipgloss.NoColor{}
		t.BgUser = scale(t.BgUser, s.UserMessageOpacity)
		t.DiffAddedBg = scale(t.DiffAddedBg, s.DiffOpacity)
		t.DiffRemovedBg = scale(t.DiffRemovedBg, s.DiffOpacity)
	}
	if f := s.BorderFactor(); f != 1.0 {
		t.Border = brighten(t.Border, f)
		t.TextFaint = brighten(t.TextFaint, f)
		t.TextSubtle = brighten(t.TextSubtle, f)
	}
	return t
}

// scale blends c toward black, keeping pct percent of its brightness. 0 gives
// no colour (transparent) and 100 leaves c unchanged.
func scale(c lipgloss.TerminalColor, pct int) lipgloss.TerminalColor {
	if pct >= 100 {
		return c
	}
	if pct <= 0 {
		return lipgloss.NoColor{}
	}
	r, g, b, ok := hexRGB(c)
	if !ok {
		return c
	}
	k := float64(pct) / 100
	return hex(int(math.Round(float64(r)*k)), int(math.Round(float64(g)*k)), int(math.Round(float64(b)*k)))
}

// brighten multiplies each channel by f, clamped to 255.
func brighten(c lipgloss.TerminalColor, f float64) lipgloss.TerminalColor {
	r, g, b, ok := hexRGB(c)
	if !ok {
		return c
	}
	ch := func(v int) int { return int(math.Min(255, math.Round(float64(v)*f))) }
	return hex(ch(r), ch(g), ch(b))
}

func hex(r, g, b int) lipgloss.TerminalColor {
	return lipgloss.Color(fmt.Sprintf("#%02x%02x%02x", r, g, b))
}

// VariantIcon marks light and dark themes in the picker: ☀ for light, ☾ for
// dark (spec 11). It is an icon only; the label is unchanged.
func (t Theme) VariantIcon() string {
	if t.Variant == "light" {
		return "☀"
	}
	return "☾"
}
