// Package theme holds Scribe's colour palettes.
//
// A palette is a set of named colour tokens (37 UI tokens plus 9 syntax
// tokens). Built-in palettes, the Scribe default and user themes all reduce to
// the same string map, which Build turns into a Theme of lipgloss colours.
//
// Colour tokens are lipgloss.TerminalColor values. Under NO_COLOR every token is
// lipgloss.NoColor{}, so styles keep bold/underline but carry no colour.
package theme

import (
	"os"
	"sort"
	"sync"

	"github.com/charmbracelet/lipgloss"
)

// Theme is a fully resolved palette ready for styling.
type Theme struct {
	Name    string
	Label   string
	Variant string // "dark" or "light"

	// Brand
	Brand, BrandSecondary, BrandDim, BrandAlt lipgloss.TerminalColor

	// Semantic status
	Error, Success, Warning, Info, Amber lipgloss.TerminalColor

	// Text hierarchy
	TextPrimary, TextSecondary, TextMuted, TextDim, TextFaint, TextSubtle lipgloss.TerminalColor

	// Backgrounds
	BgApp, BgPopup, BgPopupHighlight, BgOverlay, BgPrimary, BgSecondary lipgloss.TerminalColor
	BgElevated, BgInput, BgBanner, BgBannerError, BgUser                lipgloss.TerminalColor

	// Borders
	Border, BorderFocused, BorderActive, BorderSlash lipgloss.TerminalColor

	// Diff
	DiffAddedBg, DiffRemovedBg, DiffAddedSign, DiffRemovedSign lipgloss.TerminalColor

	// Accents
	AccentUser, AccentAssistant, AccentSystem lipgloss.TerminalColor

	// Syntax highlighting
	SynKeyword, SynString, SynNumber, SynComment, SynFunction lipgloss.TerminalColor
	SynType, SynOperator, SynPunctuation, SynVariable         lipgloss.TerminalColor
}

// UITokens lists the 37 UI token keys (camelCase, the names used by Empryo's
// palette format so transcribed values line up with the spec table).
var UITokens = []string{
	"brand", "brandSecondary", "brandDim", "brandAlt",
	"error", "success", "warning", "info", "amber",
	"textPrimary", "textSecondary", "textMuted", "textDim", "textFaint", "textSubtle",
	"bgApp", "bgPopup", "bgPopupHighlight", "bgOverlay", "bgPrimary", "bgSecondary",
	"bgElevated", "bgInput", "bgBanner", "bgBannerError", "bgUser",
	"border", "borderFocused", "borderActive", "borderSlash",
	"diffAddedBg", "diffRemovedBg", "diffAddedSign", "diffRemovedSign",
	"accentUser", "accentAssistant", "accentSystem",
}

// SyntaxTokens lists the syntax token keys.
var SyntaxTokens = []string{
	"synKeyword", "synString", "synNumber", "synComment", "synFunction",
	"synType", "synOperator", "synPunctuation", "synVariable",
}

// palette is the string form of a theme: label, variant and token values.
type palette struct {
	Label   string
	Variant string
	Tokens  map[string]string
}

// ScribeID and ScribeLightID name Scribe's own palettes.
const (
	ScribeID      = "scribe"
	ScribeLightID = "scribe-light"
)

// scribePalettes is Scribe's own design: blue-black ink, warm parchment-white
// text, a muted gold brand and two accents (soft violet for the assistant,
// teal for the user).
var scribePalettes = map[string]palette{
	ScribeID: {Label: "Scribe", Variant: "dark", Tokens: map[string]string{
		"brand": "#C9A227", "brandSecondary": "#D9893A", "brandDim": "#5B4A1A", "brandAlt": "#D8B865",
		"error": "#E06C60", "success": "#8DB66B", "warning": "#E3A24E", "info": "#7FB2D6", "amber": "#C99A4B",
		"textPrimary": "#E7E1D3", "textSecondary": "#ABA496", "textMuted": "#7F7A6E",
		"textDim": "#7C7767", "textFaint": "#67635A", "textSubtle": "#55514A",
		"bgApp": "#0B0E15", "bgPopup": "#121621", "bgPopupHighlight": "#1D2433", "bgOverlay": "#070910",
		"bgPrimary": "#0B0E15", "bgSecondary": "#0F131C", "bgElevated": "#171D2A", "bgInput": "#0E1420",
		"bgBanner": "#1A1F2E", "bgBannerError": "#3A1A1C", "bgUser": "#111A26",
		"border": "#2D3446", "borderFocused": "#C9A227", "borderActive": "#8E7FD0", "borderSlash": "#6F8FB8",
		"diffAddedBg": "#0F2319", "diffRemovedBg": "#2A1417", "diffAddedSign": "#8DB66B", "diffRemovedSign": "#D2706A",
		"accentUser": "#3FB5AC", "accentAssistant": "#A891E8", "accentSystem": "#6B675C",
		"synKeyword": "#B9A0E6", "synString": "#A5C77E", "synNumber": "#E3A56B", "synComment": "#6E7587",
		"synFunction": "#86B5E0", "synType": "#E6CB7C", "synOperator": "#9AA3B5", "synPunctuation": "#8B91A0",
		"synVariable": "#E7E1D3",
	}},
	ScribeLightID: {Label: "Scribe Light", Variant: "light", Tokens: map[string]string{
		"brand": "#8A6A10", "brandSecondary": "#B5602A", "brandDim": "#E0D3A8", "brandAlt": "#6C5BB0",
		"error": "#B3362C", "success": "#4D7A2E", "warning": "#A8680F", "info": "#2F6F9A", "amber": "#8F6420",
		"textPrimary": "#2A2823", "textSecondary": "#55514A", "textMuted": "#7C776B",
		"textDim": "#75705F", "textFaint": "#948E7E", "textSubtle": "#A9A392",
		"bgApp": "#F7F4EC", "bgPopup": "#EFEBDF", "bgPopupHighlight": "#E2DCCB", "bgOverlay": "#E9E4D6",
		"bgPrimary": "#F7F4EC", "bgSecondary": "#F1EDE2", "bgElevated": "#ECE7D8", "bgInput": "#FBF9F3",
		"bgBanner": "#EDE6D6", "bgBannerError": "#F6DEDB", "bgUser": "#E9F1EF",
		"border": "#C9C2AF", "borderFocused": "#8A6A10", "borderActive": "#6C5BB0", "borderSlash": "#2F6F9A",
		"diffAddedBg": "#E3F0DC", "diffRemovedBg": "#F6E1DE", "diffAddedSign": "#4D7A2E", "diffRemovedSign": "#B3362C",
		"accentUser": "#237F78", "accentAssistant": "#6C5BB0", "accentSystem": "#8F8A7D",
		"synKeyword": "#6C3FA8", "synString": "#3F7A2A", "synNumber": "#A85C1A", "synComment": "#8A8576",
		"synFunction": "#2D5F8C", "synType": "#8A6A10", "synOperator": "#55514A", "synPunctuation": "#6B675C",
		"synVariable": "#2A2823",
	}},
}

// derivedSyntax gives syntax colours to palettes that do not define them,
// taken from the palette's own UI tokens.
var derivedSyntax = map[string]string{
	"synKeyword": "brandAlt", "synString": "success", "synNumber": "amber", "synComment": "textMuted",
	"synFunction": "info", "synType": "brand", "synOperator": "textSecondary",
	"synPunctuation": "textSecondary", "synVariable": "textPrimary",
}

// color converts a token value to a lipgloss colour. An empty value means no
// colour (used by the NO_COLOR palette).
func color(v string) lipgloss.TerminalColor {
	if v == "" {
		return lipgloss.NoColor{}
	}
	return lipgloss.Color(v)
}

// Build resolves a palette into a Theme. Missing syntax tokens are derived from
// the UI tokens; missing UI tokens are left empty (NO_COLOR).
func Build(name string, p palette) Theme {
	tok := make(map[string]string, len(p.Tokens)+len(SyntaxTokens))
	for k, v := range p.Tokens {
		tok[k] = v
	}
	for _, k := range SyntaxTokens {
		if tok[k] == "" {
			tok[k] = tok[derivedSyntax[k]]
		}
	}
	t := Theme{Name: name, Label: p.Label, Variant: p.Variant}
	if t.Label == "" {
		t.Label = name
	}
	if t.Variant != "light" {
		t.Variant = "dark"
	}
	c := func(k string) lipgloss.TerminalColor { return color(tok[k]) }
	t.Brand, t.BrandSecondary, t.BrandDim, t.BrandAlt = c("brand"), c("brandSecondary"), c("brandDim"), c("brandAlt")
	t.Error, t.Success, t.Warning, t.Info, t.Amber = c("error"), c("success"), c("warning"), c("info"), c("amber")
	t.TextPrimary, t.TextSecondary, t.TextMuted = c("textPrimary"), c("textSecondary"), c("textMuted")
	t.TextDim, t.TextFaint, t.TextSubtle = c("textDim"), c("textFaint"), c("textSubtle")
	t.BgApp, t.BgPopup, t.BgPopupHighlight, t.BgOverlay = c("bgApp"), c("bgPopup"), c("bgPopupHighlight"), c("bgOverlay")
	t.BgPrimary, t.BgSecondary, t.BgElevated, t.BgInput = c("bgPrimary"), c("bgSecondary"), c("bgElevated"), c("bgInput")
	t.BgBanner, t.BgBannerError, t.BgUser = c("bgBanner"), c("bgBannerError"), c("bgUser")
	t.Border, t.BorderFocused, t.BorderActive, t.BorderSlash = c("border"), c("borderFocused"), c("borderActive"), c("borderSlash")
	t.DiffAddedBg, t.DiffRemovedBg, t.DiffAddedSign, t.DiffRemovedSign = c("diffAddedBg"), c("diffRemovedBg"), c("diffAddedSign"), c("diffRemovedSign")
	t.AccentUser, t.AccentAssistant, t.AccentSystem = c("accentUser"), c("accentAssistant"), c("accentSystem")
	t.SynKeyword, t.SynString, t.SynNumber, t.SynComment, t.SynFunction = c("synKeyword"), c("synString"), c("synNumber"), c("synComment"), c("synFunction")
	t.SynType, t.SynOperator, t.SynPunctuation, t.SynVariable = c("synType"), c("synOperator"), c("synPunctuation"), c("synVariable")
	return t
}

// Tokens returns the palette's string tokens, the inverse of Build for the
// fields Build reads. Used to check completeness and to merge overrides.
func (t Theme) Tokens() map[string]lipgloss.TerminalColor {
	return map[string]lipgloss.TerminalColor{
		"brand": t.Brand, "brandSecondary": t.BrandSecondary, "brandDim": t.BrandDim, "brandAlt": t.BrandAlt,
		"error": t.Error, "success": t.Success, "warning": t.Warning, "info": t.Info, "amber": t.Amber,
		"textPrimary": t.TextPrimary, "textSecondary": t.TextSecondary, "textMuted": t.TextMuted,
		"textDim": t.TextDim, "textFaint": t.TextFaint, "textSubtle": t.TextSubtle,
		"bgApp": t.BgApp, "bgPopup": t.BgPopup, "bgPopupHighlight": t.BgPopupHighlight, "bgOverlay": t.BgOverlay,
		"bgPrimary": t.BgPrimary, "bgSecondary": t.BgSecondary, "bgElevated": t.BgElevated, "bgInput": t.BgInput,
		"bgBanner": t.BgBanner, "bgBannerError": t.BgBannerError, "bgUser": t.BgUser,
		"border": t.Border, "borderFocused": t.BorderFocused, "borderActive": t.BorderActive, "borderSlash": t.BorderSlash,
		"diffAddedBg": t.DiffAddedBg, "diffRemovedBg": t.DiffRemovedBg, "diffAddedSign": t.DiffAddedSign, "diffRemovedSign": t.DiffRemovedSign,
		"accentUser": t.AccentUser, "accentAssistant": t.AccentAssistant, "accentSystem": t.AccentSystem,
		"synKeyword": t.SynKeyword, "synString": t.SynString, "synNumber": t.SynNumber, "synComment": t.SynComment,
		"synFunction": t.SynFunction, "synType": t.SynType, "synOperator": t.SynOperator,
		"synPunctuation": t.SynPunctuation, "synVariable": t.SynVariable,
	}
}

var (
	mu      sync.RWMutex
	customs = map[string]palette{} // user themes by id, already validated
)

// paletteFor finds a palette by id: Scribe, then built-in, then user theme.
// Built-ins win over user themes with the same id.
func paletteFor(id string) (palette, bool) {
	if p, ok := scribePalettes[id]; ok {
		return p, true
	}
	if p, ok := builtinPalettes[id]; ok {
		return p, true
	}
	mu.RLock()
	defer mu.RUnlock()
	p, ok := customs[id]
	return p, ok
}

// Names lists every selectable theme id: Scribe first, then the rest sorted.
// User themes that reuse a built-in id are not listed.
func Names() []string {
	seen := map[string]bool{ScribeID: true, ScribeLightID: true}
	names := []string{ScribeID, ScribeLightID}
	var rest []string
	for id := range builtinPalettes {
		rest = append(rest, id)
	}
	mu.RLock()
	for id := range customs {
		if _, builtin := builtinPalettes[id]; !builtin && !seen[id] {
			rest = append(rest, id)
		}
	}
	mu.RUnlock()
	sort.Strings(rest)
	for _, id := range rest {
		if !seen[id] {
			seen[id] = true
			names = append(names, id)
		}
	}
	return names
}

// Default is Scribe's dark palette.
func Default() Theme { return Get(ScribeID) }

// Lookup returns the named theme. ok is false for an unknown name. The name
// "auto" picks Scribe or Scribe Light from the terminal background.
func Lookup(name string) (Theme, bool) {
	if name == "auto" {
		return Auto(), true
	}
	p, ok := paletteFor(name)
	if !ok {
		return Theme{}, false
	}
	return Build(name, p), true
}

// Get returns the named theme, or Default for an unknown name.
func Get(name string) Theme {
	if t, ok := Lookup(name); ok {
		return t
	}
	return Build(ScribeID, scribePalettes[ScribeID])
}

// Auto picks a Scribe variant from the terminal background. lipgloss queries
// the terminal once and caches the answer; when it cannot tell, dark is used.
func Auto() Theme {
	if lipgloss.HasDarkBackground() {
		return Get(ScribeID)
	}
	return Get(ScribeLightID)
}

// Resolve applies the environment on top of a name: NO_COLOR (any non-empty
// value, per no-color.org) gives the plain palette for that theme's variant.
func Resolve(name string) Theme {
	t := Get(name)
	if name == "auto" {
		t = Auto()
	}
	if os.Getenv("NO_COLOR") != "" {
		return Plain(t)
	}
	return t
}

// Plain returns t with every colour token cleared. Bold, underline and italic
// still work, so structure survives.
func Plain(t Theme) Theme {
	p := palette{Label: t.Label, Variant: t.Variant, Tokens: map[string]string{}}
	for _, k := range append(append([]string{}, UITokens...), SyntaxTokens...) {
		p.Tokens[k] = ""
	}
	out := Build(t.Name, p)
	return out
}
